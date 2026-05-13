package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	var (
		namespace  string
		kubeconfig string
		interval   time.Duration
		iterations int
		verbose    bool
	)

	flag.StringVar(&namespace, "namespace", "mengqiyu-configmap", "namespace to watch configmaps in")
	defaultKubeconfig := os.Getenv("KUBECONFIG")
	if defaultKubeconfig == "" {
		defaultKubeconfig = homedir.HomeDir() + "/.kube/config"
	}
	flag.StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig, "path to kubeconfig")
	flag.DurationVar(&interval, "interval", 2*time.Second, "pause between WatchList iterations")
	flag.IntVar(&iterations, "iterations", 0, "number of WatchList iterations (0 = infinite)")
	flag.BoolVar(&verbose, "verbose", false, "print details for each event")
	flag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to build config: %v", err)
		}
	}
	config.QPS = 100
	config.Burst = 200

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create clientset: %v", err)
	}

	for i := 1; iterations == 0 || i <= iterations; i++ {
		log.Printf("=== WatchList iteration %d ===", i)
		count, err := doWatchList(clientset, namespace, verbose)
		if err != nil {
			log.Printf("WatchList failed: %v", err)
		} else {
			log.Printf("Iteration %d complete: streamed %d events", i, count)
		}

		if iterations != 0 && i >= iterations {
			break
		}
		time.Sleep(interval)
	}
}

func doWatchList(clientset *kubernetes.Clientset, namespace string, verbose bool) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sendInitialEvents := true
	watcher, err := clientset.CoreV1().ConfigMaps(namespace).Watch(ctx, metav1.ListOptions{
		SendInitialEvents:    &sendInitialEvents,
		AllowWatchBookmarks:  true,
		ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan,
		ResourceVersion:      "",
	})
	if err != nil {
		return 0, fmt.Errorf("starting watch: %w", err)
	}
	defer watcher.Stop()

	var count int64
	var bookmarkSeen bool
	start := time.Now()

	for event := range watcher.ResultChan() {
		switch event.Type {
		case watch.Added, watch.Modified, watch.Deleted:
			count++
			if verbose {
				if cm, ok := event.Object.(*corev1.ConfigMap); ok {
					log.Printf("  event #%d: type=%s name=%s rv=%s", count, event.Type, cm.Name, cm.ResourceVersion)
				} else {
					log.Printf("  event #%d: type=%s object=%T", count, event.Type, event.Object)
				}
			}
			if count%2000 == 0 {
				elapsed := time.Since(start)
				log.Printf("  progress: %d events in %v (%.0f events/sec)", count, elapsed, float64(count)/elapsed.Seconds())
			}
		case watch.Bookmark:
			bookmarkSeen = true
			log.Printf("  bookmark received after %d events (initial list complete)", count)
			elapsed := time.Since(start)
			rate := float64(count) / elapsed.Seconds()
			log.Printf("  finished: %d events, bookmark=%v, duration=%v, rate=%.0f events/sec", count, bookmarkSeen, elapsed, rate)
			return count, nil
		case watch.Error:
			return count, fmt.Errorf("watch error after %d events: %v", count, event.Object)
		}
	}

	elapsed := time.Since(start)
	rate := float64(count) / elapsed.Seconds()
	log.Printf("  finished: %d events, bookmark=%v, duration=%v, rate=%.0f events/sec", count, bookmarkSeen, elapsed, rate)
	return count, nil
}
