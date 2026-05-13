package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const annotationKey = "configmap-test/last-mutated"

func main() {
	var (
		namespace  string
		total      int
		qps        int
		numClients int
		kubeconfig string
	)

	flag.StringVar(&namespace, "namespace", "mengqiyu-configmap", "namespace of configmaps to patch")
	flag.IntVar(&total, "total", 200000, "number of configmaps (index 0 to total-1)")
	flag.IntVar(&qps, "qps", 100, "patches per second")
	flag.IntVar(&numClients, "num-clients", 10, "number of independent clientsets for load distribution")
	defaultKubeconfig := os.Getenv("KUBECONFIG")
	if defaultKubeconfig == "" {
		defaultKubeconfig = homedir.HomeDir() + "/.kube/config"
	}
	flag.StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig, "path to kubeconfig")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received signal, shutting down...")
		cancel()
	}()

	config, err := rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to build config: %v", err)
		}
	}
	config.QPS = float32(qps)
	config.Burst = qps * 2

	clientsets := make([]*kubernetes.Clientset, numClients)
	for i := 0; i < numClients; i++ {
		cs, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("Failed to create clientset %d: %v", i, err)
		}
		clientsets[i] = cs
	}
	log.Printf("Created %d clientset(s), target QPS=%d, patching cm-000000 to cm-%06d", numClients, qps, total-1)

	limiter := rate.NewLimiter(rate.Limit(qps), qps)

	var patched atomic.Int64
	var failed atomic.Int64
	startTime := time.Now()

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p := patched.Load()
				f := failed.Load()
				elapsed := time.Since(startTime).Seconds()
				actualRate := float64(p) / elapsed
				log.Printf("Progress: %d patched, %d failed, %.1f patches/sec (target %d)", p, f, actualRate, qps)
			}
		}
	}()

	concurrency := qps
	if concurrency < 10 {
		concurrency = 10
	}
	work := make(chan int, concurrency*2)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		cs := clientsets[i%numClients]
		go func() {
			defer wg.Done()
			for idx := range work {
				name := fmt.Sprintf("cm-%06d", idx)
				if err := patchAnnotation(ctx, cs, namespace, name); err != nil {
					if ctx.Err() != nil {
						return
					}
					failed.Add(1)
					log.Printf("Failed to patch %s: %v", name, err)
				} else {
					patched.Add(1)
				}
			}
		}()
	}

	idx := 0
	for {
		if err := limiter.Wait(ctx); err != nil {
			break
		}
		work <- idx
		idx = (idx + 1) % total
	}
	close(work)
	wg.Wait()

	elapsed := time.Since(startTime)
	log.Printf("Stopped. Patched: %d, Failed: %d, Duration: %v", patched.Load(), failed.Load(), elapsed)
}

func patchAnnotation(ctx context.Context, cs *kubernetes.Clientset, namespace, name string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				annotationKey: now,
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = cs.CoreV1().ConfigMaps(namespace).Patch(reqCtx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}
