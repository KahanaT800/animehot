# gRPC Architecture Upgrade Plan

## Deliverables

No code changes. This document serves as the implementation plan.

## Goal

Introduce a gRPC control plane so the Analyzer gains observability into the crawler cluster; introduce private queues + smart dispatch to upgrade blind-push mode to health-weighted assignment per node. Two phases, each independently deployable, fully backward-compatible throughout.

---

## Phase 1: gRPC Control Plane (Crawler <-> Analyzer Management Channel)

### 1.1 New Proto Definition

**New file: `proto/control.proto`**

```protobuf
syntax = "proto3";
package control;
option go_package = "animetop/proto/pb";

service CrawlerControl {
  // Bidirectional stream: connect = register, disconnect = deregister
  rpc Session(stream CrawlerStatus) returns (stream ControlCommand);
}

message CrawlerStatus {
  string node_id          = 1;
  int32  max_concurrency  = 2;
  int32  active_tasks     = 3;
  int32  queue_depth      = 4;
  float  success_rate     = 5;
  float  avg_latency_sec  = 6;
  bool   is_proxy_mode    = 7;
  bool   is_draining      = 8;
  int64  uptime_sec       = 9;
  int64  total_processed  = 10;
  int64  timestamp        = 11;
}

message ControlCommand {
  oneof command {
    ConfigUpdate          config_update   = 1;
    PauseCommand          pause           = 2;
    ResumeCommand         resume          = 3;
    DrainCommand          drain           = 4;
    RestartBrowserCommand restart_browser = 5;
  }
}

message ConfigUpdate {
  int32 max_concurrency = 1;
  float rate_limit      = 2;
}
message PauseCommand  { string reason = 1; }
message ResumeCommand {}
message DrainCommand  { string reason = 1; }
message RestartBrowserCommand { string reason = 1; }
```

**Modify `Makefile` proto target**: add `control.proto` compilation, requires additional `protoc-gen-go-grpc` installation

```makefile
proto:
	protoc --go_out=. --go_opt=module=animetop \
	       --go-grpc_out=. --go-grpc_opt=module=animetop \
	       proto/crawler.proto proto/control.proto
```

**Modify `go.mod`**: add `google.golang.org/grpc` dependency

### 1.2 Node Registry

**New file: `internal/controlplane/node_registry.go`**

```go
type NodeInfo struct {
    NodeID        string
    Status        *pb.CrawlerStatus    // latest heartbeat
    ConnectedAt   time.Time
    LastHeartbeat time.Time
    HealthScore   float64              // weighted calculation
    Stream        pb.CrawlerControl_SessionServer
}

type NodeRegistry struct {
    nodes  map[string]*NodeInfo
    mu     sync.RWMutex
    logger *slog.Logger
}
```

Key methods:
- `Register(nodeID, stream)` / `Unregister(nodeID)`
- `UpdateStatus(nodeID, *CrawlerStatus)` — called on each heartbeat, updates HealthScore
- `GetHealthyNodes() []*NodeInfo` — sorted by HealthScore descending, excludes draining nodes
- `SendCommand(nodeID, *ControlCommand)` / `BroadcastCommand(*ControlCommand)`

Health score formula:
```
Score = SuccessRate * 0.4
      + (1 - ActiveTasks/MaxConcurrency) * 0.3
      + (1 - min(AvgLatency/300, 1)) * 0.2
      + (1 - min(QueueDepth/20, 1)) * 0.1
// IsDraining -> Score = 0
```

### 1.3 gRPC Server (Analyzer Side)

**New file: `internal/controlplane/grpc_server.go`**

- Implement `Session(stream)` method
- First `CrawlerStatus` message acts as registration, extract `node_id`
- Loop receiving subsequent heartbeats, call `registry.UpdateStatus()`
- On stream disconnect call `registry.Unregister(nodeID)`
- **Bug lesson #2 (a5de0b0)**: all error paths must use WARN-level logging, no `_ =`

### 1.4 gRPC Client (Crawler Side)

**New file: `internal/controlplane/grpc_client.go`**

```go
type ControlPlaneClient struct {
    nodeID       string
    analyzerAddr string
    conn         *grpc.ClientConn
    commandCh    chan *pb.ControlCommand
    logger       *slog.Logger
    statusFn     func() *pb.CrawlerStatus  // callback to collect current status
}
```

- `Connect(ctx)` — exponential backoff reconnect (base 5s, max 60s)
- `StartHeartbeat(ctx, interval)` — periodically send CrawlerStatus
- `receiveCommands(ctx)` — background goroutine receives ControlCommand
- `Commands() <-chan *pb.ControlCommand` — expose read-only channel
- **Bug lesson #5 (b07ad76)**: auto-recover after disconnect/reconnect, no state loss

### 1.5 Configuration

**Modify `internal/config/config.go`**

Add `GRPC GRPCConfig` to Config struct:

```go
type GRPCConfig struct {
    Enabled             bool          `json:"enabled"`              // default: false
    ListenAddr          string        `json:"listen_addr"`          // default: ":50051"
    AnalyzerAddr        string        `json:"analyzer_addr"`        // default: "localhost:50051"
    NodeID              string        `json:"node_id"`              // default: hostname
    HeartbeatInterval   time.Duration `json:"heartbeat_interval"`   // default: 10s
    ReconnectBackoff    time.Duration `json:"reconnect_backoff"`    // default: 5s
    MaxReconnectBackoff time.Duration `json:"max_reconnect_backoff"` // default: 60s
}
```

Environment variables: `GRPC_ENABLED`, `GRPC_LISTEN_ADDR`, `GRPC_ANALYZER_ADDR`, `GRPC_NODE_ID`, `GRPC_HEARTBEAT_INTERVAL`

**Bug lesson #6 (d51bafb)**: all new config items must have explicit defaults in `DefaultConfig()` and support env var overrides in `applyEnvOverrides`.

### 1.6 Prometheus Metrics

**Modify `internal/pkg/metrics/metrics.go`**

```go
GRPCConnectedNodes    Gauge                       // online node count
GRPCNodeHealthScore   GaugeVec{node_id}           // per-node health score
GRPCHeartbeatTotal    CounterVec{node_id}         // heartbeat count
GRPCStreamErrors      CounterVec{node_id, type}   // stream error count
```

### 1.7 Integration Entry Points

**Modify `cmd/analyzer/main.go`**

Conditionally start gRPC server (`cfg.GRPC.Enabled == true`):
1. Create `NodeRegistry`
2. Create `ControlPlaneServer`, register to `grpc.NewServer()`
3. `go grpcServer.Serve(listener)`
4. On graceful shutdown: `grpcServer.GracefulStop()`

**Modify `cmd/crawler/main.go`**

Conditionally connect gRPC (`cfg.GRPC.AnalyzerAddr != ""`):
1. Create `ControlPlaneClient`
2. `go cpClient.Connect(ctx)`
3. `go cpClient.StartHeartbeat(ctx, interval)`
4. `go handleControlCommands(ctx, cpClient.Commands(), crawlerSvc)` — handle pause/drain etc.

**Modify `internal/crawler/service.go`**

Add `CollectStatus() *pb.CrawlerStatus` method, collecting data from existing `Stats()` + `activePages` + `isUsingProxy()` + `draining`.

### 1.8 Phase 1 Implementation Order

1. `proto/control.proto` + Makefile changes + `go mod tidy`
2. `internal/controlplane/node_registry.go` + `_test.go`
3. `internal/controlplane/grpc_server.go` + `_test.go` (test with `bufconn`)
4. `internal/controlplane/grpc_client.go` + `_test.go`
5. `internal/config/config.go` — add `GRPCConfig` + defaults + env vars
6. `internal/pkg/metrics/metrics.go` — add gRPC metrics
7. `internal/crawler/service.go` — add `CollectStatus()`
8. `cmd/analyzer/main.go` — conditionally start gRPC server
9. `cmd/crawler/main.go` — conditionally connect gRPC client
10. Integration test: start analyzer + crawler, verify registration/heartbeat/metrics

---

## Phase 2: Private Queues + Smart Dispatch

### 2.1 Queue Key Parameterization

**Modify `internal/pkg/redisqueue/client.go`**

New struct:
```go
type NodeQueueKeys struct {
    TaskQueue       string // animetop:queue:tasks:node:<node_id>
    ProcessingQueue string // animetop:queue:tasks:node:<node_id>:processing
    StartedHash     string // animetop:queue:tasks:node:<node_id>:started
    DeadLetter      string // animetop:queue:tasks:node:<node_id>:dead
}
```

**Key design: dedup set (pending set) remains global**

```
Global: animetop:queue:tasks:pending  <- unchanged, ensures only one task per IP system-wide
Per-node: animetop:queue:tasks:node:<node_id>  <- new, private per node
Per-node: animetop:queue:tasks:node:<node_id>:processing  <- new
```

This means the `pushTaskScript` Lua script doesn't need changes — `KEYS[1]` still takes the global pending set, `KEYS[2]` changes to the node's private queue. Ack script follows the same pattern: `KEYS[1]` takes the node processing queue, `KEYS[2]` takes the global pending set.

New methods (same signature as existing methods, plus a `keys` parameter):
- `PushTaskToNode(ctx, task, keys)` — push to specific node queue
- `PopTaskFromNode(ctx, timeout, keys)` — pop from specific node queue
- `AckTaskOnNode(ctx, task, keys)` — ack on specific node's queue
- `RescueStuckTasksOnNode(ctx, timeout, keys)` — clean up specific node
- `RecoverOrphanedTasksOnNode(ctx, keys)` — recover on startup for specific node
- `GetNodeQueueStats(ctx, keys)` — get node queue statistics

**Lua scripts don't need modification** — they are already parameterized via `KEYS[]`; only the Go call sites pass different key values.

**Bug lesson #1 (ef7d32f)**: node_id may contain hyphens (e.g. UUID format). Lua scripts already use `string.find(..., 1, true)` for plain text matching, which is safe. Registration should still validate node_id format (alphanumeric + hyphens).

### 2.2 Task Dispatcher

**New file: `internal/controlplane/dispatcher.go`**

```go
type Dispatcher struct {
    registry *NodeRegistry
    queue    *redisqueue.Client
    logger   *slog.Logger
}

func (d *Dispatcher) Dispatch(ctx context.Context, task *pb.CrawlRequest) (nodeID string, err error)
```

Dispatch logic:
1. `registry.GetHealthyNodes()` — get nodes sorted by health score descending
2. No nodes -> fall back to shared queue `queue.PushTask()` (backward compatible)
3. Iterate nodes, skip those with `QueueDepth >= MaxConcurrency*2` (full)
4. Weighted random selection: higher Score nodes have higher selection probability
5. `queue.PushTaskToNode(ctx, task, NodeQueueKeys{nodeID})` — push to private queue
6. All nodes full -> fall back to shared queue

**Bug lesson #4 (7c105f6)**: `GRPCDispatchTotal` metric increments at Dispatch entry, not inside conditional branches.

### 2.3 Scheduler Changes

**Modify `internal/scheduler/ip_scheduler.go`**

IPScheduler gets new optional fields:
```go
dispatcher   *controlplane.Dispatcher  // nil = Phase 2 not enabled
nodeRegistry *controlplane.NodeRegistry // nil = Phase 2 not enabled
```

`pushTasksForIP` changes:
```go
if s.dispatcher != nil {
    nodeID, err := s.dispatcher.Dispatch(ctx, task)
    // ...
} else {
    s.queue.PushTask(ctx, task)  // original logic unchanged
}
```

`waitForQueueDrain` changes:
```go
if s.nodeRegistry != nil && s.nodeRegistry.NodeCount() > 0 {
    // Aggregate (capacity, load) across all nodes, throttle when load > 80%
    totalCapacity := sum(node.MaxConcurrency * 2)
    totalLoad := sum(node.QueueDepth + node.ActiveTasks)
    if float64(totalLoad)/float64(totalCapacity) <= 0.8 { return }
} else {
    // Original logic: check shared queue depth
}
```

### 2.4 Janitor Changes

**Modify `internal/scheduler/ip_scheduler.go`** janitorLoop

```go
// Existing: clean shared queue (keep for backward compatibility)
s.queue.RescueStuckTasks(ctx, timeout)
s.queue.RescueStuckResults(ctx, timeout)

// New: clean each node's private queue
if s.nodeRegistry != nil {
    for _, node := range s.nodeRegistry.GetAllNodes() {
        keys := redisqueue.NewNodeQueueKeys(node.NodeID)
        s.queue.RescueStuckTasksOnNode(ctx, timeout, keys)
    }
    // Clean up residual queues from disconnected nodes
    s.rescueDeadNodeQueues(ctx)
}
```

**Disconnected node task reassignment** — triggered in `NodeRegistry.Unregister` callback:
- Use `RPOPLPUSH` to move tasks from dead node's private queue back to shared queue one by one
- Clean up that node's started hash
- Do not increment RetryCount (**Bug lesson #5 (b07ad76)**: non-punitive recovery)

### 2.5 Crawler Side Changes

**Modify `internal/crawler/crawl.go`** StartWorker

```go
// If node_id is configured, prefer pulling from private queue
if s.nodeQueueKeys != nil {
    task, err = s.redisQueue.PopTaskFromNode(ctx, 1*time.Second, s.nodeQueueKeys)
}
// Fall back to shared queue when private queue is empty
if task == nil || errors.Is(err, redisqueue.ErrNoTask) {
    task, err = s.redisQueue.PopTask(ctx, 2*time.Second)
}
```

**Modify `internal/crawler/service.go`**

Service struct adds `nodeQueueKeys *redisqueue.NodeQueueKeys`, initialized in `NewService` based on `cfg.GRPC.NodeID`.

Ack must also route to the correct processing queue:
```go
if s.nodeQueueKeys != nil {
    s.redisQueue.AckTaskOnNode(ctx, task, s.nodeQueueKeys)
} else {
    s.redisQueue.AckTask(ctx, task)
}
```

### 2.6 Phase 2 New Metrics

```go
GRPCDispatchTotal      CounterVec{target}    // target: node_id or "shared_queue"
GRPCNodeQueueDepth     GaugeVec{node_id}     // per-node queue depth
GRPCDispatchFallback   Counter               // fallback to shared queue count
```

### 2.7 Phase 2 Implementation Order

1. `internal/pkg/redisqueue/client.go` — `NodeQueueKeys` + parameterized methods + `_test.go`
2. `internal/controlplane/dispatcher.go` + `_test.go` (with mock registry)
3. `internal/scheduler/ip_scheduler.go` — dispatcher integration + backpressure changes
4. `internal/scheduler/ip_scheduler.go` — janitor multi-node scanning + disconnected node reassignment
5. `internal/crawler/service.go` — `nodeQueueKeys` field
6. `internal/crawler/crawl.go` — StartWorker private queue + Ack routing
7. `cmd/analyzer/main.go` — create Dispatcher and pass to Scheduler
8. Integration test: multi-node dispatch + disconnect reassignment + shared queue fallback

---

## Result Queue Unchanged

The result queue (`animetop:queue:results`) remains globally shared, no privatization. Reasons:
- Only one consumer (Pipeline), no routing needed
- Pipeline is an in-process component of Analyzer, no multi-node competition
- Modifying the result queue has minimal benefit with high complexity

---

## File Inventory

### New Files

| File | Purpose | Phase |
|------|---------|-------|
| `proto/control.proto` | gRPC service + message definitions | 1 |
| `internal/controlplane/node_registry.go` | Node registration, health tracking | 1 |
| `internal/controlplane/node_registry_test.go` | Unit tests | 1 |
| `internal/controlplane/grpc_server.go` | gRPC Session implementation | 1 |
| `internal/controlplane/grpc_server_test.go` | bufconn tests | 1 |
| `internal/controlplane/grpc_client.go` | Client + reconnect + heartbeat | 1 |
| `internal/controlplane/grpc_client_test.go` | Unit tests | 1 |
| `internal/controlplane/dispatcher.go` | Smart task dispatch | 2 |
| `internal/controlplane/dispatcher_test.go` | Unit tests | 2 |

### Modified Files

| File | Change | Phase |
|------|--------|-------|
| `go.mod` | Add `google.golang.org/grpc` | 1 |
| `Makefile` | Proto target adds `--go-grpc_out` and `control.proto` | 1 |
| `internal/config/config.go` | Add `GRPCConfig` + defaults + env vars | 1 |
| `internal/pkg/metrics/metrics.go` | Add gRPC-related metrics | 1+2 |
| `internal/crawler/service.go` | Add `CollectStatus()`, `nodeQueueKeys` field | 1+2 |
| `cmd/analyzer/main.go` | Conditionally start gRPC server, create Dispatcher | 1+2 |
| `cmd/crawler/main.go` | Conditionally connect gRPC client | 1 |
| `internal/pkg/redisqueue/client.go` | `NodeQueueKeys` + parameterized methods | 2 |
| `internal/pkg/redisqueue/client_test.go` | New parameterized method tests | 2 |
| `internal/scheduler/ip_scheduler.go` | Dispatcher integration + janitor multi-node + backpressure | 2 |
| `internal/crawler/crawl.go` | StartWorker private queue + Ack routing | 2 |

---

## Bug Lesson Checklist

Must be checked before merging each PR:

| Bug | Lesson | Check Item |
|-----|--------|------------|
| ef7d32f: Lua string.find pattern matching | UUID hyphens are special chars in Lua | All Lua `string.find` must pass `1, true`; validate node_id format on registration |
| a5de0b0: Silent ACK failure | Errors swallowed by `_ =` | All gRPC/Redis error paths must use WARN logging; no `_ =` for critical operations |
| f9803ed: State machine asymmetric handling | First crawl inflow/outflow asymmetry | New features must handle edge state symmetry (e.g. first heartbeat vs subsequent, registration vs reconnection) |
| 7c105f6: Counter only increments on success | MaxTasks never triggers under high failure rate | Metrics/counters increment at entry point, not inside conditional branches |
| b07ad76: No recovery on startup | Processing queue residuals after restart | Non-punitive recovery on node reconnect (don't increment RetryCount); disconnected node queue tasks recovered to shared queue |
| d51bafb: Inconsistent config values | MaxFetchCount has different defaults in different places | All new configs get defaults in `DefaultConfig()`, documented in `.env.example` |

---

## Verification Plan

### Phase 1 Verification

1. `go test ./internal/controlplane/...` — all pass
2. `go test -v -race ./...` — CI-level full test suite, no races
3. Local `make docker-light-up` + manually start crawler with `GRPC_ANALYZER_ADDR`
4. Check Prometheus `/metrics` shows `animetop_grpc_connected_nodes = 1`
5. Check analyzer logs show `"node registered"` + periodic `"heartbeat received"`

### Phase 2 Verification

1. `go test ./internal/pkg/redisqueue/...` — parameterized method tests pass
2. `go test ./internal/controlplane/...` — dispatcher tests pass
3. Locally start analyzer + 2 crawlers (different node_id)
4. Check `redis-cli LLEN animetop:queue:tasks:node:<node_id>` shows assigned tasks
5. Disconnect one crawler, verify its queue tasks are recovered to shared queue
6. Check Prometheus `animetop_grpc_dispatch_total{target="node-xxx"}` has data

### Regression Verification

- Disable `GRPC_ENABLED`, full flow uses shared queue, behavior identical to pre-upgrade
- Crawler without `GRPC_ANALYZER_ADDR` runs in legacy mode, works normally
