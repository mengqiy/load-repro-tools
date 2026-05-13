package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	var (
		namespace     string
		labelSelector string
		qps           float64
		burst         int
		interval      time.Duration
		kubeconfig    string
		limit         int64
		numClients    int
	)

	flag.StringVar(&namespace, "namespace", "mengqiyu-configmap", "namespace to list configmaps from")
	flag.StringVar(&labelSelector, "label-selector", "batch-group=group-000000", "label selector for listing configmaps")
	flag.Float64Var(&qps, "qps", 2000, "queries per second to the API server")
	flag.IntVar(&burst, "burst", 2000, "burst allowance above QPS")
	flag.DurationVar(&interval, "interval", 100*time.Millisecond, "interval between list calls")
	flag.Int64Var(&limit, "limit", 0, "max items per list call (0 = no limit, server decides)")
	flag.IntVar(&numClients, "num-clients", 1, "number of independent clientsets")
	defaultKubeconfig := os.Getenv("KUBECONFIG")
	if defaultKubeconfig == "" {
		defaultKubeconfig = homedir.HomeDir() + "/.kube/config"
	}
	flag.StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig, "path to kubeconfig")
	flag.Parse()

	if numClients < 1 {
		log.Fatalf("-num-clients must be >= 1, got %d", numClients)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to build config: %v", err)
		}
	}
	config.QPS = float32(qps)
	config.Burst = burst

	clientsets := make([]*kubernetes.Clientset, numClients)
	for i := 0; i < numClients; i++ {
		cs, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("Failed to create clientset %d: %v", i, err)
		}
		clientsets[i] = cs
	}

	log.Printf("Starting lister: namespace=%s, selector=%q, QPS=%.0f, burst=%d, interval=%v, clients=%d",
		namespace, labelSelector, qps, burst, interval, numClients)

	var totalLists atomic.Int64
	var totalItems atomic.Int64
	startTime := time.Now()

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			lists := totalLists.Load()
			items := totalItems.Load()
			elapsed := time.Since(startTime).Seconds()
			log.Printf("Stats: %d lists, %d total items returned, %.2f lists/sec, %.0f items/sec",
				lists, items, float64(lists)/elapsed, float64(items)/elapsed)
		}
	}()

	for i := 0; i < numClients; i++ {
		cs := clientsets[i]
		go func(clientID int) {
			for {
				listOpts := metav1.ListOptions{
					LabelSelector: labelSelector,
				}
				if limit > 0 {
					listOpts.Limit = limit
				}

				ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
				start := time.Now()
				result, err := cs.CoreV1().ConfigMaps(namespace).List(ctx, listOpts)
				latency := time.Since(start)
				cancel()

				if err != nil {
					log.Printf("[client %d] List failed (latency=%v): %v", clientID, latency, err)
				} else {
					count := len(result.Items)
					totalLists.Add(1)
					totalItems.Add(int64(count))
					remaining := ""
					if result.Continue != "" {
						remaining = fmt.Sprintf(", has more (continue token present)")
					}
					log.Printf("[client %d] Listed %d configmaps in %v%s", clientID, count, latency, remaining)
				}

				time.Sleep(interval)
			}
		}(i)
	}

	select {}
}
