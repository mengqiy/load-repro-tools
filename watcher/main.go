package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func main() {
	var kubeconfig *string
	var workers *int
	var namespace *string
	var watchTimeout *int
	var rvOffset *int

	// Initialize klog flags
	klog.InitFlags(nil)

	// Determine default kubeconfig path
	var defaultKubeconfig string
	if kubeconfigEnv := os.Getenv("KUBECONFIG"); kubeconfigEnv != "" {
		defaultKubeconfig = kubeconfigEnv
	} else if home := os.Getenv("HOME"); home != "" {
		defaultKubeconfig = filepath.Join(home, ".kube", "config")
	}

	kubeconfig = flag.String("kubeconfig", defaultKubeconfig, "absolute path to the kubeconfig file")
	workers = flag.Int("workers", 1, "number of parallel watch workers")
	namespace = flag.String("namespace", "", "namespace to watch (empty = all namespaces)")
	watchTimeout = flag.Int("watch-timeout", 10, "watch timeout in seconds (watch will restart after this duration)")
	rvOffset = flag.Int("rv-offset", 1000, "resource version offset to subtract when starting a watch")
	flag.Parse()

	// Ensure klog flushes on exit
	defer klog.Flush()

	// Build config
	var config *rest.Config
	var err error

	// Try in-cluster config first
	config, err = rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			klog.Fatalf("Failed to build config: %v", err)
		}
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create clientset: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		klog.Info("Shutting down...")
		cancel()
	}()

	// Bootstrap: Get initial resource version once before starting all workers
	klog.Info("Bootstrapping: Listing configmaps to get initial resource version...")
	initialRV, err := getInitialResourceVersion(ctx, clientset)
	if err != nil {
		klog.Fatalf("Failed to get initial resource version: %v", err)
	}
	klog.Infof("Initial resource version obtained: %d", initialRV)

	// Start multiple watch workers to create more load
	for i := 0; i < *workers; i++ {
		go watchConfigMaps(ctx, clientset, *namespace, *watchTimeout, i, *rvOffset, initialRV)
	}

	// Wait for context to be cancelled
	<-ctx.Done()
	klog.Info("All workers stopped")
}

func getInitialResourceVersion(ctx context.Context, clientset *kubernetes.Clientset) (int64, error) {
	// List configmaps from all namespaces with RV=0 and limit=1 to get current resource version
	listOptions := metav1.ListOptions{
		// ResourceVersion: "0",
		Limit: 1,
	}

	// Always list from all namespaces ("") for bootstrap to get the cluster-wide RV
	cmList, err := clientset.CoreV1().ConfigMaps("").List(ctx, listOptions)
	if err != nil {
		return 0, fmt.Errorf("failed to list configmaps from all namespaces: %w", err)
	}

	rv, err := strconv.ParseInt(cmList.ResourceVersion, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse resource version %q: %w", cmList.ResourceVersion, err)
	}

	return rv, nil
}

func watchConfigMaps(ctx context.Context, clientset *kubernetes.Clientset, namespace string, watchTimeout, workerID, rvOffset int, initialRV int64) {
	klog.Infof("[Worker %d] Starting configmap watcher with bootstrap RV=%d", workerID, initialRV)

	// Use the bootstrapped initial RV for the first watch cycle
	rv := initialRV
	firstRun := true

	for {
		select {
		case <-ctx.Done():
			klog.Infof("[Worker %d] Context cancelled, stopping", workerID)
			return
		default:
		}

		// Only list to get current RV if this is not the first run
		if !firstRun {
			var err error
			rv, err = getCurrentResourceVersion(ctx, clientset, namespace, workerID)
			if err != nil {
				klog.Errorf("[Worker %d] Failed to get current resource version: %v", workerID, err)
				time.Sleep(5 * time.Second)
				continue
			}
		}
		firstRun = false

		// Step 2: Calculate the starting resource version by subtracting the offset
		startRV := rv - int64(rvOffset)
		if startRV < 0 {
			startRV = 0
		}
		klog.Infof("[Worker %d] Starting watch from RV=%d (current=%d, offset=%d)", workerID, startRV, rv, rvOffset)

		// Step 3: Start watching from the calculated resource version
		lastRV, err := watchFromResourceVersion(ctx, clientset, namespace, watchTimeout, workerID, startRV)
		if err != nil {
			klog.Errorf("[Worker %d] Watch error: %v", workerID, err)
			// Brief pause before restarting the watch cycle
			time.Sleep(1 * time.Second)
			continue
		}

		// Watch timed out normally - restart from (lastRV - offset)
		if lastRV > 0 {
			newStartRV := lastRV - int64(rvOffset)
			if newStartRV < 0 {
				newStartRV = 0
			}
			klog.Infof("[Worker %d] Watch timeout, restarting from RV=%d (last=%d, offset=%d)", workerID, newStartRV, lastRV, rvOffset)

			// Continue watching from the calculated RV
			for {
				select {
				case <-ctx.Done():
					klog.Infof("[Worker %d] Context cancelled, stopping", workerID)
					return
				default:
				}

				lastRV, err = watchFromResourceVersion(ctx, clientset, namespace, watchTimeout, workerID, newStartRV)
				if err != nil {
					klog.Errorf("[Worker %d] Watch error: %v", workerID, err)
					// Break to outer loop to restart from step 1
					break
				}

				// Watch timed out again, calculate new starting RV
				if lastRV > 0 {
					newStartRV = lastRV - int64(rvOffset)
					if newStartRV < 0 {
						newStartRV = 0
					}
					klog.Infof("[Worker %d] Watch timeout, restarting from RV=%d (last=%d, offset=%d)", workerID, newStartRV, lastRV, rvOffset)
				}
			}
		}

		// Brief pause before restarting the watch cycle
		time.Sleep(1 * time.Second)
	}
}

func getCurrentResourceVersion(ctx context.Context, clientset *kubernetes.Clientset, namespace string, workerID int) (int64, error) {
	listOptions := metav1.ListOptions{
		ResourceVersion: "0",
		Limit:           1,
	}

	cmList, err := clientset.CoreV1().ConfigMaps(namespace).List(ctx, listOptions)
	if err != nil {
		return 0, fmt.Errorf("failed to list configmaps: %w", err)
	}

	rv, err := strconv.ParseInt(cmList.ResourceVersion, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse resource version %q: %w", cmList.ResourceVersion, err)
	}

	klog.V(4).Infof("[Worker %d] Current resource version: %d", workerID, rv)
	return rv, nil
}

func watchFromResourceVersion(ctx context.Context, clientset *kubernetes.Clientset, namespace string, watchTimeout, workerID int, startRV int64) (int64, error) {
	jitter := 1.0 + (rand.Float64()*0.4 - 0.2)
	timeout := int64(float64(watchTimeout) * jitter)
	watchOptions := metav1.ListOptions{
		ResourceVersion: fmt.Sprintf("%d", startRV),
		TimeoutSeconds:  &timeout,
		Watch:           true,
	}

	for {
		select {
		case <-ctx.Done():
			return 0, nil
		default:
		}

		watcher, err := clientset.CoreV1().ConfigMaps(namespace).Watch(ctx, watchOptions)
		if err != nil {
			// Check if it's a 410 Gone error (resource version too old)
			if isGoneError(err) {
				klog.Warningf("[Worker %d] Received 410 Gone error, resource version %d too old, listing to get latest RV", workerID, startRV)
				freshRV, listErr := getCurrentResourceVersion(ctx, clientset, namespace, workerID)
				if listErr != nil {
					return 0, fmt.Errorf("failed to list after 410: %w", listErr)
				}
				klog.Infof("[Worker %d] Got fresh RV=%d from list after 410 Gone", workerID, freshRV)
				startRV = freshRV
				watchOptions.ResourceVersion = fmt.Sprintf("%d", startRV)
				continue
			}

			// Check if it's a 429 Too Many Requests error
			if isTooManyRequestsError(err) {
				klog.Warningf("[Worker %d] Received 429 Too Many Requests, retrying with backoff", workerID)
				time.Sleep(wait.Jitter(5*time.Second, 0.1))
				continue
			}

			return 0, fmt.Errorf("failed to start watch: %w", err)
		}

		err = processWatchEvents(ctx, watcher, workerID, &watchOptions)
		watcher.Stop()

		if err != nil {
			// Check error type and handle accordingly
			if isGoneError(err) {
				klog.Warningf("[Worker %d] Received 410 Gone error during watch, listing to get latest RV", workerID)
				freshRV, listErr := getCurrentResourceVersion(ctx, clientset, namespace, workerID)
				if listErr != nil {
					return 0, fmt.Errorf("failed to list after 410: %w", listErr)
				}
				klog.Infof("[Worker %d] Got fresh RV=%d from list after 410 Gone", workerID, freshRV)
				startRV = freshRV
				watchOptions.ResourceVersion = fmt.Sprintf("%d", startRV)
				continue
			}

			if isTooManyRequestsError(err) {
				klog.Warningf("[Worker %d] Received 429 Too Many Requests during watch, retrying", workerID)
				time.Sleep(wait.Jitter(5*time.Second, 0.1))
				continue
			}

			return 0, err
		}

		// Watch ended normally (timeout), return the last seen RV
		lastRV, parseErr := strconv.ParseInt(watchOptions.ResourceVersion, 10, 64)
		if parseErr != nil {
			klog.Warningf("[Worker %d] Failed to parse last RV %q: %v", workerID, watchOptions.ResourceVersion, parseErr)
			return 0, nil
		}

		klog.V(4).Infof("[Worker %d] Watch timeout reached, last RV=%d", workerID, lastRV)
		return lastRV, nil
	}
}

func processWatchEvents(ctx context.Context, watcher watch.Interface, workerID int, watchOptions *metav1.ListOptions) error {
	eventCount := 0
	resultChan := watcher.ResultChan()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-resultChan:
			if !ok {
				klog.V(4).Infof("[Worker %d] Watch channel closed, processed %d events", workerID, eventCount)
				return nil
			}

			eventCount++

			// Handle different event types
			switch event.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				if obj, ok := event.Object.(metav1.Object); ok {
					watchOptions.ResourceVersion = obj.GetResourceVersion()
					if eventCount%1000 == 0 {
						klog.V(4).Infof("[Worker %d] Processed %d events, current RV=%s", workerID, eventCount, watchOptions.ResourceVersion)
					}
				}
			case watch.Bookmark:
				if obj, ok := event.Object.(metav1.Object); ok {
					watchOptions.ResourceVersion = obj.GetResourceVersion()
					klog.V(4).Infof("[Worker %d] Bookmark received, RV=%s", workerID, watchOptions.ResourceVersion)
				}
			case watch.Error:
				status, ok := event.Object.(*metav1.Status)
				if ok {
					if status.Code == 410 {
						return fmt.Errorf("resource version expired (410)")
					}
					if status.Code == 429 {
						return fmt.Errorf("too many requests (429)")
					}
					return fmt.Errorf("watch error: %s (code %d)", status.Message, status.Code)
				}
				return fmt.Errorf("unknown watch error event")
			}
		}
	}
}

func isGoneError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains "410" or "resource version too old" or "Expired"
	errMsg := err.Error()
	return contains(errMsg, "410") || contains(errMsg, "too old") || contains(errMsg, "Expired") || contains(errMsg, "expired")
}

func isTooManyRequestsError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains "429" or "too many requests"
	errMsg := err.Error()
	return contains(errMsg, "429") || contains(errMsg, "Too Many Requests") || contains(errMsg, "too many requests")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
