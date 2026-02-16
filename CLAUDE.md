# Claude Code Instructions

## Build & Test

```bash
go build ./...    # compile all packages
go test ./...     # run all tests (also: just test)
just server       # build server + start locally (local Docker provisioner)
just run          # build client + run hack/test-job.yaml against localhost
just build        # build both server (bin/alfred-server) and client (bin/alfred)
```

## Proto Regeneration

**Do not run `just proto` without installing the correct plugin versions first.**
The system protoc plugins are newer than what go.mod expects, and will generate incompatible code
(newer plugins produce `ServerStreamingClient` interfaces and other breaking changes).

```bash
GOBIN=$HOME/go/bin go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.31.0
GOBIN=$HOME/go/bin go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0
PATH="$HOME/go/bin:$PATH" just proto
```

After regeneration, restore the protoc version in the header comments of both
`proto/alfred.pb.go` and `proto/alfred_grpc.pb.go` from `v6.31.1` back to `v4.24.4`.

## Project Structure

```
alfred/
├── client/                  # CLI client (Cobra)
│   ├── main.go             #   Entry point, gRPC connection, root command
│   ├── run.go              #   Schedule jobs from jobfiles
│   ├── watch.go            #   Real-time job status monitoring
│   ├── artifact.go         #   Download task artifacts
│   ├── ps.go               #   List all jobs
│   ├── top.go              #   Server dashboard (tview TUI)
│   ├── version.go          #   Client + server version display
│   ├── self_update.go      #   Binary self-update from GitHub releases
│   ├── jobfile/             #   YAML jobfile parsing, validation, Docker build
│   ├── sossh/               #   SSH+socat tunneling for gRPC
│   └── ui/                  #   Terminal UI helpers (spinner, colors)
├── server/                  # gRPC server
│   ├── main.go             #   Entry point, lifecycle, graceful shutdown
│   ├── server.go           #   gRPC service registration, Ping handler
│   ├── server_jobs.go      #   ScheduleJob, WatchJob, WatchJobs RPCs
│   ├── server_artifacts.go #   DownloadArtifact streaming RPC
│   ├── server_load.go      #   LoadImage bidirectional streaming RPC
│   ├── status.go           #   Event-driven state reconstruction + client broadcasting
│   ├── scheduler.go        #   Scheduler factory, artifact/secret callbacks
│   ├── docker.go           #   Docker client initialization
│   ├── config/              #   Constants (MaxPacketSize = 50MB)
│   ├── flags/               #   CLI flags + viper env bindings
│   └── log/                 #   Structured slog logging setup
├── scheduler/               # Event-driven task scheduler
│   ├── scheduler.go        #   Main loop, node pool, task scheduling, subscriptions
│   ├── events.go           #   Event types (11 types: node/job/task lifecycle)
│   ├── job.go              #   Job wrapper with unique ID and completion tracking
│   ├── task.go             #   Task model, FQN generation
│   ├── node.go             #   Node interface, RunTaskConfig, node status constants
│   ├── provisioner.go      #   Provisioner interface
│   ├── config.go           #   Scheduler configuration
│   └── internal/            #   NbNodesToCreate() provisioning calculator
├── provisioner/
│   ├── internal/
│   │   ├── docker.go       #   RunContainer() - full Docker execution engine
│   │   └── workspacefs.go  #   WorkspaceFS interface + ScopedFS
│   ├── local/               #   Local Docker provisioner (thin wrapper)
│   └── openstack/           #   OpenStack VM provisioner (SSH + Docker)
├── proto/
│   ├── alfred.proto         #   gRPC service + message definitions
│   ├── alfred.pb.go         #   Generated protobuf code
│   └── alfred_grpc.pb.go   #   Generated gRPC stubs
├── namegen/                 #   Random name generation for jobs/nodes
├── hack/                    #   Test jobfiles, dev scripts
│   ├── test-job.yaml       #   Minimal test job (3 tasks)
│   └── job.yaml            #   Full example with services and templating
└── var/                     #   Runtime data (gitignored)
    ├── node-workspace/      #   Local provisioner workspace
    └── server-data/         #   Artifacts, secrets storage
```

## Architecture Overview

Alfred is a CI/CD task runner with client/server architecture. The client reads YAML
jobfiles, builds Docker images locally, ships them to the server, and watches execution.
The server schedules tasks across provisioned nodes (local Docker or OpenStack VMs).

### Data Flow

```
Client                          Server                       Scheduler
  │ LoadImage (stream)             │                             │
  ├───────────────────────────────>│ docker load                 │
  │ ScheduleJob                    │                             │
  ├───────────────────────────────>│ Schedule() ────────────────>│
  │ WatchJob (stream)              │                             │
  ├───────────────────────────────>│ Subscribe() <── broadcast ──┤
  │                <── event ──────┤                              │
  │                                │                    Provisioner
  │                                │                        │
  │                                │               Node.RunTask()
  │                                │                        │
  │                                │              RunContainer()
  │                                │           (network, services,
  │                                │            steps, artifacts)
```

### Key Components

**Client** (`client/main.go`): Cobra CLI. Establishes gRPC connection in `PersistentPreRunE`
(validates `bash`, `docker`, `ssh`, `zstd` are installed). Remote address from `--remote`
flag or `ALFRED_REMOTE` env var (default: `alfred.tipee.dev:25373`). SSH tunneling enabled
automatically for non-localhost remotes via sossh (SSH + socat).

**Server** (`server/main.go`): gRPC server on port 25373. Initializes Docker client,
creates scheduler with chosen provisioner, subscribes to scheduler events. Status state
reconstructed entirely from event stream in `status.go`. Thread-safe via `serverStatusMutex`.
Client watchers get filtered event channels via `addClientListener()`.

**Scheduler** (`scheduler/scheduler.go`): Single-goroutine main loop processes jobs from
input channel, handles tick requests for scheduling decisions. Event broadcasting via
`Subscribe()` returning buffered channel (1024) + unsubscribe function. Node pool auto-scales
based on queued tasks vs capacity. Live artifact access via `ArchiveLiveArtifact()`.

**Provisioners**: Implement `Provisioner` interface (`Provision`, `Shutdown`, `Wait`) and
`Node` interface (`Name`, `RunTask`, `Terminate`). Local provisioner wraps Docker on localhost.
OpenStack provisioner creates VMs, connects via SSH, transfers images compressed with zstd.

### RunContainer() Flow (`provisioner/internal/docker.go`)

The core execution engine for every task:

1. **Network**: Create bridge network `alfred-{task.FQN()}`
2. **Workspace**: Create dirs (`/output`, `/shared`), register live artifact archiver
3. **Services**: Pull images, create containers with tmpfs mounts, start in goroutines
   with health check retry loops (configurable interval/timeout/retries)
4. **Steps**: Run sequentially. Load secrets (base64-encoded env vars), bind-mount
   workspace to `/alfred`, stream logs to `/output/{task}-step-{i}.log`
5. **Artifacts**: Archive `/output` as tar.zst via `ArtifactPreserver` callback
6. **Cleanup**: Deferred in LIFO order (workspace, containers, network)

Special env vars injected into step containers: `ALFRED_TASK`, `ALFRED_TASK_FQN`,
`ALFRED_NB_SLOTS_FOR_TASK`, `ALFRED_SHARED`, `ALFRED_OUTPUT`.

### Event System

The scheduler broadcasts 11 event types. The server's `listenEvents()` goroutine consumes
them to maintain `serverStatus` and forward filtered events to connected client watchers.

| Category | Events |
|----------|--------|
| Node | `EventNodeCreated`, `EventNodeStatusUpdated`, `EventNodeSlotUpdated`, `EventNodeTerminated` |
| Job | `EventJobScheduled`, `EventJobCompleted` |
| Task | `EventTaskQueued`, `EventTaskRunning`, `EventTaskAborted`, `EventTaskFailed`, `EventTaskCompleted` |

Node statuses: `QUEUED -> PROVISIONING -> ONLINE -> TERMINATING -> TERMINATED`
(with `FAILED_PROVISIONING -> DISCARDED` error path).

Task statuses: `QUEUED -> RUNNING -> COMPLETED | FAILED | ABORTED`.

### gRPC API (`proto/alfred.proto`)

| RPC | Type | Purpose |
|-----|------|---------|
| `Ping` | Unary | Health check, returns server version |
| `ScheduleJob` | Unary | Submit job, returns assigned name |
| `WatchJob` | Server stream | Stream single job's status events |
| `WatchJobs` | Server stream | Stream all jobs list (job-level events only) |
| `DownloadArtifact` | Server stream | Chunked artifact download (completed or live) |
| `LoadImage` | Bidi stream | Upload compressed Docker images (zstd) |

## Code Conventions

- **gRPC-free commands**: Override `PersistentPreRunE` with a no-op (see `self_update.go`).
- **Versioning**: `version`, `commit`, `repository` set via ldflags. Defaults: `"dev"`, `"n/a"`, `"tipee-sa/alfred"`.
- **Jobfile paths**: Context and dockerfile paths are relative to the jobfile's directory.
- **Service containers**: Run in goroutines with health checks. Step containers run sequentially.
- **Callbacks**: `RunTaskConfig` carries `ArtifactPreserver`, `SecretLoader`,
  `OnWorkspaceReady`, `OnWorkspaceTeardown`. This is the extension point for task execution hooks.
- **FQN naming**: `jobName-randomId` (job) / `jobName-randomId-taskName` (task). Used for
  workspace dirs, Docker networks, and artifact paths.
- **Thread safety**: Server status protected by `sync.RWMutex`. Scheduler main loop is
  single-goroutine; node provisioning and task execution spawn goroutines.
- **YAML parsing**: `gopkg.in/yaml.v3` handles trailing commas in flow sequences cleanly.
- **Docker tmpfs**: `HostConfig.Tmpfs` is `map[string]string` (key=path, value=mount options).

## Jobfile Format

```yaml
version: "1"
name: "my-job"                    # Must match: ^[a-z][a-z0-9_-]+$
steps:
  - dockerfile: ./Dockerfile      # Relative to jobfile dir
    context: .
    options: ["--build-arg", "X=1"]
env:
  KEY: value                      # Must match: ^[A-Z][A-Z0-9_]+$
secrets:
  SECRET_NAME: placeholder        # Loaded from server-data/secrets/
services:
  redis:
    image: redis:7
    command: ["redis-server", "--save", ""]
    tmpfs: ["/tmp"]
    env:
      REDIS_PASS: test
    health:
      cmd: ["redis-cli", "ping"]
      timeout: 5s
      interval: 1s
      retries: 10
tasks:
  - task-name-1
  - { name: heavy-task, slots: 8 }
  - task-name-2
```

Tasks can be plain strings (defaulting to 1 slot) or objects with `name` and `slots` fields.
Each task consumes N slots on a node (default 1). The server's `--slots-per-node` flag sets
the total capacity per node.

Jobfiles support Go templates with slim-sprig functions. Available data: `.Env` (map),
`.Args` ([]string), `.Params` (map from `--param KEY=VALUE`). Custom functions:
`error "msg"`, `exec "cmd" "arg"...`, `shell "bash script"`.

## Server Flags

| Flag | Default | Env | Purpose |
|------|---------|-----|---------|
| `--listen` | `:25373` | `ALFRED_LISTEN` | gRPC address |
| `--log-level` | `INFO` | `ALFRED_LOG_LEVEL` | DEBUG, INFO, WARN, ERROR |
| `--log-format` | `json` | `ALFRED_LOG_FORMAT` | json or text |
| `--provisioner` | `local` | `ALFRED_PROVISIONER` | local or openstack |
| `--max-nodes` | `(CPU+1)/2` | `ALFRED_MAX_NODES` | Max provisioned nodes |
| `--slots-per-node` | `2` | `ALFRED_SLOTS_PER_NODE` | Slot capacity per node |
| `--task-startup-delay` | `1s` | `ALFRED_TASK_STARTUP_DELAY` | Delay between starting tasks on the same node |
| `--provisioning-delay` | `20s` | `ALFRED_PROVISIONING_DELAY` | Delay between node creations |
| `--node-workspace` | `var/node-workspace` | `ALFRED_NODE_WORKSPACE` | Workspace directory |
| `--server-data` | `var/server-data` | `ALFRED_SERVER_DATA` | Artifacts + secrets directory |

OpenStack flags: `--openstack-{docker-host,flavor,image,networks,security-groups,ssh-username,server-group}`.

## Deployment

- **Server**: Runs on `alfred.tipee.dev` as systemd unit (`alfred.service`), deployed via
  GitHub Actions `workflow_dispatch`. Binary SCP'd to `/opt/alfred/alfred-server`.
- **Client releases**: Triggered by git tags (`YY.MM.DD` format). Builds 3 binaries:
  `alfred-linux-amd64`, `alfred-darwin-amd64`, `alfred-darwin-arm64`. Uploaded to GitHub releases.
- **CI**: `go test -v ./...` on every push (Go 1.21, ubuntu-latest).
- **Logging**: Datadog Agent on the server host tails journald for log shipping (not in this codebase).
- **Self-update**: Client detects latest version from GitHub release redirect URL, downloads
  and atomically replaces its own binary.

## Concurrency Model

The codebase uses several recurring async patterns (all annotated with inline comments):

- **Scheduler main loop** (`scheduler/scheduler.go:Run`): Single-goroutine event loop using
  `select` on 4 channels (input, tickRequests, deferred, stop). All scheduler state mutations
  happen here — no mutexes needed for tasksQueue, nodes, nodesQueue.

- **Watcher goroutines** (`watchTaskExecution`, `watchNodeProvisioning`, `watchNodeTermination`):
  Background goroutines that block on long-running operations (RunTask, Provision, Terminate),
  then signal back to the main loop via `requestTick()` and `broadcast()`.

- **Tick coalescing** (`requestTick`): Non-blocking send on buffered channel (capacity 1).
  Multiple goroutines can request scheduling passes; they coalesce into one.

- **Deferred execution** (`after`): `time.AfterFunc` sends closures to a channel read by
  the main loop, ensuring delayed actions run on the main goroutine (safe state mutation).

- **Event broadcasting** (`forwardEvents`): Dedicated goroutine drains events channel and
  distributes to all subscriber channels. Non-blocking send (drops if subscriber is full).

- **Server state reconstruction** (`status.go:listenEvents`): Single goroutine consumes
  scheduler events, updates `serverStatus` under write lock, forwards to client watchers.
  Lock ordering: always `serverStatusMutex` before `clientListenersMutex`.

- **gRPC streaming handlers** (`server_jobs.go`): Each connected client gets its own goroutine
  (managed by gRPC framework). Registers filtered listener, loops with `select` on event
  channel + `srv.Context().Done()` (client disconnect).

- **Client watch** (`client/watch.go`): Recv goroutine decouples blocking `c.Recv()` from
  main select loop (which also handles a 1-second ticker for display refresh).

- **Service startup** (`docker.go`): WaitGroup + goroutines for parallel service startup with
  health checks. Buffered error channel collects failures.

- **Log streaming** (`docker.go`): Background goroutine streams container logs with context
  cancellation. Monitor goroutine pattern for health check timeouts (closes reader to unblock
  `io.Copy`).

- **Shutdown cascade**: Signal handler → `cancel()` → `ctx.Done()` propagates to scheduler
  (Shutdown → Run returns) and gRPC server (GracefulStop). Server WaitGroup ensures both
  complete before exit. Double Ctrl+C forces immediate exit.

## Gotchas & Lessons Learned

- **Proto plugin versions are critical**: `protoc-gen-go@v1.31.0` and `protoc-gen-go-grpc@v1.3.0`
  match go.mod's protobuf/grpc versions. Newer plugins generate incompatible interfaces.
- **Service container logs**: Service containers now have log collection on startup failure
  (step containers always had log streaming).
- **MySQL `--innodb-fast-shutdown=2`**: Breaks MySQL's init sequence (temporary server and
  real server disagree on redo logs). Don't use it in service health benchmarks.
- **Workspace defer order matters**: Artifacts must be archived BEFORE workspace deletion
  (LIFO defer order). Getting this wrong loses artifacts.
- **Exit code 42**: Treated as "completed with warnings" (⚠️) by the watch command display.
  Task status is `FAILED` with exit code 42. Triggers `--abort-on-failure` but not `--abort-on-error`.
- **Exit code 43**: Treated as "skipped — prerequisite failed" (⏭️). For infrastructure problems
  (e.g., missing/corrupted database backups) where retrying or aborting other tasks won't help.
  Task status is `SKIPPED`. Excluded from both `--abort-on-failure` and `--abort-on-error`.
- **Local provisioner != OpenStack provisioner**: The local provisioner doesn't create N nodes
  running Y tasks each. Instead it creates N*Y Docker containers each running 1 task. Every
  task gets its own Docker network, workspace, and service containers — even though they all
  share the same Docker daemon. This means `max-nodes * slots-per-node` networks are created
  concurrently.
- **Docker network limit**: Docker can only create ~31 default bridge networks (due to
  available /16 address pools). With the local provisioner and an ambitious config (e.g.,
  `--max-nodes=8 --slots-per-node=4`), you'll hit "could not find an available, non-overlapping
  IPv4 address pool among the defaults to assign to the network". Keep local configs small
  or configure Docker with additional address pools.
