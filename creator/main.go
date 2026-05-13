package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const (
	totalConfigMaps    = 200000
	configMapSizeBytes = 50 * 1024 // 50KB
	kvPairSizeBytes    = 200
	kvPairsPerCM       = configMapSizeBytes / kvPairSizeBytes // 256 pairs per configmap
	labelGroupSize     = 10
)

func main() {
	var (
		namespace   string
		concurrency int
		numClients  int
		kubeconfig  string
	)

	flag.StringVar(&namespace, "namespace", "mengqiyu-configmap", "namespace to create configmaps in")
	flag.IntVar(&concurrency, "concurrency", 500, "number of concurrent workers")
	defaultKubeconfig := os.Getenv("KUBECONFIG")
	if defaultKubeconfig == "" {
		defaultKubeconfig = homedir.HomeDir() + "/.kube/config"
	}
	flag.IntVar(&numClients, "num-clients", 12, "number of independent clientsets for load distribution")
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
	config.QPS = 500
	config.Burst = 1000

	clientsets := make([]*kubernetes.Clientset, numClients)
	for i := 0; i < numClients; i++ {
		cs, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("Failed to create clientset %d: %v", i, err)
		}
		clientsets[i] = cs
	}
	log.Printf("Created %d clientset(s) with QPS=%.0f, Burst=%d", numClients, config.QPS, config.Burst)

	var created atomic.Int64
	var failed atomic.Int64
	startTime := time.Now()

	// Progress reporter
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c := created.Load()
			f := failed.Load()
			elapsed := time.Since(startTime).Seconds()
			rate := float64(c) / elapsed
			log.Printf("Progress: %d/%d created, %d failed, %.1f configmaps/sec", c, totalConfigMaps, f, rate)
		}
	}()

	work := make(chan int, concurrency*2)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		cs := clientsets[i%numClients]
		go func() {
			defer wg.Done()
			for idx := range work {
				cm := buildConfigMap(idx, namespace)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := cs.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
				cancel()
				if err != nil {
					failed.Add(1)
					log.Printf("Failed to create configmap %d: %v", idx, err)
				} else {
					created.Add(1)
				}
			}
		}()
	}

	for i := 0; i < totalConfigMaps; i++ {
		work <- i
	}
	close(work)
	wg.Wait()

	elapsed := time.Since(startTime)
	log.Printf("Done. Created: %d, Failed: %d, Duration: %v", created.Load(), failed.Load(), elapsed)
}

func buildConfigMap(index int, namespace string) *corev1.ConfigMap {
	labelGroup := index / labelGroupSize
	data := make(map[string]string, kvPairsPerCM)

	for i := 0; i < kvPairsPerCM; i++ {
		key := fmt.Sprintf("key-%04d", i)          // ~8 bytes
		valueLen := kvPairSizeBytes - len(key) - 2 // subtract key length and overhead
		data[key] = generateValue(index, i, valueLen)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("cm-%06d", index),
			Namespace: namespace,
			Labels: map[string]string{
				"app":         "configmap-test",
				"batch-group": fmt.Sprintf("group-%06d", labelGroup),
			},
		},
		Data: data,
	}
}

func generateValue(cmIndex, kvIndex, length int) string {
	prefix := fmt.Sprintf("cm%d-kv%d-", cmIndex, kvIndex)
	if length <= len(prefix) {
		return prefix[:length]
	}
	var sb strings.Builder
	sb.WriteString(prefix)
	padding := length - len(prefix)
	chunk := "abcdefghijklmnopqrstuvwxyz0123456789"
	for sb.Len() < len(prefix)+padding {
		remaining := len(prefix) + padding - sb.Len()
		if remaining >= len(chunk) {
			sb.WriteString(chunk)
		} else {
			sb.WriteString(chunk[:remaining])
		}
	}
	return sb.String()
}
