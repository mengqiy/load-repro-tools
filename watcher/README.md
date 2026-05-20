# ConfigMap Watcher - API Server Load Generator

A Go program that watches all configmaps in a Kubernetes cluster **without any local caching** to generate load on the API server.

## Features

- **No caching**: Uses raw Kubernetes watch API without informers or any caching mechanism
- **Multiple workers**: Can spawn multiple parallel watch connections to increase load
- **Namespace filtering**: Watch all namespaces or filter by specific namespace
- **Auto-reconnect**: Automatically reconnects if watch connection drops

## Building

```bash
go build -o configmap-watcher main.go
```

## Usage

### Basic usage (1 worker, all namespaces)
```bash
./configmap-watcher
```

### Multiple workers for increased load
```bash
./configmap-watcher -workers=10
```

### Watch specific namespace
```bash
./configmap-watcher -namespace=default
```

### Custom kubeconfig (via flag)
```bash
./configmap-watcher -kubeconfig=/path/to/kubeconfig
```

### Custom kubeconfig (via environment variable)
```bash
export KUBECONFIG=/path/to/kubeconfig
./configmap-watcher
```

### Custom watch timeout (more frequent reconnections)
```bash
./configmap-watcher -workers=10 -watch-timeout=30
```

### Combined options
```bash
./configmap-watcher -workers=20 -namespace=kube-system -watch-timeout=60
```

## Command Line Flags

### Application Flags
- `-kubeconfig`: Path to kubeconfig file (default: `$KUBECONFIG` env var, or `~/.kube/config`, or in-cluster config if running in a pod)
- `-workers`: Number of parallel watch workers (default: `1`)
- `-namespace`: Namespace to watch (default: empty = all namespaces)
- `-watch-timeout`: Watch timeout in seconds before reconnecting (default: `70`)

### Logging Flags (klog)
- `-v`: Log level verbosity (0-10, default: 0)
  - `0`: Only errors and basic info
  - `2`: Shows watch lifecycle events (starting, closing)
  - `3`: Shows detailed event processing (every 100 events)
  - `4+`: More verbose debugging information
- `-logtostderr`: Log to stderr instead of files (default: true)
- `-alsologtostderr`: Log to both stderr and files
- `-log_dir`: Directory for log files (when not logging to stderr)
- `-vmodule`: Per-file log level settings

### Examples with Verbosity

```bash
# Default logging (minimal)
./configmap-watcher -workers=5

# Show watch lifecycle events
./configmap-watcher -workers=5 -v=2

# Show detailed event processing
./configmap-watcher -workers=5 -v=3

# Maximum verbosity
./configmap-watcher -workers=5 -v=5
```

## How It Creates Load

1. Each worker creates a direct watch connection to the API server
2. **LIST then WATCH pattern**:
   - First does a minimal LIST (Limit=1) to get current ResourceVersion
   - Then watches from that ResourceVersion (long-lived connection)
3. **Uses `watchtools.UntilWithoutRetry`** - standard Kubernetes watch pattern without automatic retries
4. **No informers, no SharedIndexInformer, no local client cache**
5. Watch only receives updates (ADDED/MODIFIED/DELETED) that occur after connection
6. Events are processed and immediately discarded (no caching)
7. Watch connections stay alive for the full timeout (default: 90 seconds)
8. After timeout, watch closes and immediately reconnects (new LIST + WATCH)
9. This creates continuous watch reconnection load on the API server
10. Lower timeout values create more frequent reconnections = higher load
11. Minimal memory usage - only stores current ResourceVersion
12. Logs every 100 events processed per worker (at -v=3)

### Load Scaling Strategies

- **Increase workers**: More parallel watch connections per pod
- **Increase replicas**: More pods running workers
- **Decrease timeout**: More frequent watch reconnections (e.g., `-watch-timeout=30`)
- **Total watch connections** = `replicas × workers`
- **Reconnections per second** ≈ `(replicas × workers) / watch-timeout`

### LIST then WATCH Pattern

This watcher uses the **LIST then WATCH** pattern that real Kubernetes controllers use:

**On each reconnection:**
1. **LIST with Limit=1**: Gets current ResourceVersion using minimal memory
   - Only fetches 1 configmap object to minimize API server load
   - Extracts ResourceVersion from metadata
   - Immediately discards the list result (no caching)

2. **WATCH from ResourceVersion**: Creates long-lived watch connection
   - Watches from the ResourceVersion obtained from LIST
   - Only receives new events (no historical replay)
   - Stays connected for full timeout period (90s default)
   - Processes events and discards them immediately

**Benefits:**
- **Minimal memory**: Only 1 configmap fetched per LIST, no caching
- **Long-lived watches**: Stays connected for full timeout (realistic production behavior)
- **Current state**: Always watches from latest state, no missed events
- **Clean reconnection**: Each cycle starts with fresh ResourceVersion

With 10 workers and 90s timeout:
- **Connection pattern**: 10 concurrent long-lived watches
- **Reconnection rate**: ~0.11 reconnections/second (10 workers / 90s)
- **LIST load**: 10 minimal LIST operations per 90s
- **Watch load**: 10 continuous watch streams processing live updates

## Example Output

```
2026/02/27 01:55:00 Starting 5 watch workers for configmaps in namespace '' (empty = all namespaces)
2026/02/27 01:55:00 Worker 0: starting new watch
2026/02/27 01:55:00 Worker 1: starting new watch
2026/02/27 01:55:00 Worker 2: starting new watch
2026/02/27 01:55:00 Worker 3: starting new watch
2026/02/27 01:55:00 Worker 4: starting new watch
2026/02/27 01:55:05 Worker 0: processed 100 events (latest: MODIFIED default/my-pod)
2026/02/27 01:55:06 Worker 2: processed 100 events (latest: MODIFIED kube-system/coredns-abc123)
...
```

## Graceful Shutdown

Press `Ctrl+C` to gracefully shut down all watch workers.

## Building the Docker Image

### Standard Build

```bash
docker build -t configmap-watcher:latest .
```


## Deploying to Kubernetes

### Quick Deploy

The easiest way to build and deploy:

```bash
# Set your registry (optional, defaults to localhost:5000)
export REGISTRY=your-registry.io
export IMAGE_TAG=v1.0.0

# Build and deploy using the script
./build-and-deploy.sh
```

Or use the Makefile:

```bash
# Build Docker image
make docker-build

# Push to registry
make REGISTRY=your-registry.io docker-push

# Deploy to cluster
make deploy

# Or do all in one command
make REGISTRY=your-registry.io IMAGE_TAG=v1.0.0 all
```

### Manual Deploy Steps

1. **Build the Docker image:**
   ```bash
   docker build -t configmap-watcher:latest .
   ```

2. **Tag and push to your registry:**
   ```bash
   docker tag configmap-watcher:latest your-registry.io/configmap-watcher:latest
   docker push your-registry.io/configmap-watcher:latest
   ```

3. **Update the image in deployment.yaml:**
   Edit `deployment.yaml` and update the image field:
   ```yaml
   image: your-registry.io/configmap-watcher:latest
   ```

4. **Deploy to cluster:**
   ```bash
   kubectl apply -f deployment.yaml
   ```

### Managing the Deployment

```bash
# Check pod status
kubectl get pods -n mengqiyu-watcher -l app=configmap-watcher

# View logs
kubectl logs -n mengqiyu-watcher -l app=configmap-watcher -f

# Scale up workers (increase replicas)
kubectl scale deployment configmap-watcher -n mengqiyu-watcher --replicas=5

# Update worker count per pod
kubectl set env deployment/configmap-watcher -n mengqiyu-watcher WORKERS=20

# Delete deployment
kubectl delete -f deployment.yaml
```

### Makefile Targets

- `make build` - Build Go binary locally
- `make docker-build` - Build Docker image (standard)
- `make docker-push` - Build and push to registry
- `make deploy` - Deploy to Kubernetes
- `make delete` - Remove from cluster
- `make logs` - Follow logs from all pods
- `make scale` - Interactively scale deployment
- `make status` - Get pod status
- `make clean` - Remove local build artifacts
- `make all` - Build, push, and deploy in one command

