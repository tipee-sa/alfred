# Claude Code Instructions

## Dev Environment

The Nix devshell (`flake.nix`, `nix/devshell.nix`, loaded by direnv via `.envrc`) provides Go,
just, protoc, reflex, zstd, and the pinned protoc plugins (`nix/packages/`). Docker and SSH come
from the host. Outside direnv, prefix commands with `nix develop -c`.

`.mcp.json` declares the `telemetry` MCP server (ClickHouse logs and traces at
`telemetry.tipee.dev`), as tipee's does; it reads `TIPEE_TELEMETRY_TOKEN` from the environment.

## Build & Test

```bash
go build ./...    # compile all packages
go test ./...     # run all tests (also: just test)
just server       # build server + start locally (local Docker provisioner)
just run          # build client + run hack/test-job.yaml against localhost
just build        # build both server (bin/alfred-server) and client (bin/alfred)
```

## Proto Regeneration

Run `just proto` inside the devshell. Plugins other than `protoc-gen-go@v1.31.0` and
`protoc-gen-go-grpc@v1.3.0` (e.g. from `go install ...@latest`) generate different code
(`ServerStreamingClient` interfaces and other changes).

After regeneration, restore the protoc version in the header comments of both
`proto/alfred.pb.go` and `proto/alfred_grpc.pb.go` back to `v4.24.4`.

## Project Structure

```
alfred/
├── client/                  # CLI client (Cobra)
│   ├── main.go             #   Entry point, root command, global flags
│   ├── connect.go          #   gRPC connection + Ping within --connect-timeout
│   ├── run.go              #   Schedule jobs from jobfiles
│   ├── watch.go            #   Real-time job status monitoring
│   ├── artifact.go         #   Download task artifacts
│   ├── help_scripting.go   #   `alfred help scripting`: the guide for scripts and agents
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
│   ├── job_history.go      #   Completed jobs saved to disk, reloaded at startup
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
├── deploy/telemetry/        #   Server's telemetry agent: OTel Collector + node-exporter (Compose)
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
automatically for non-localhost remotes via sossh (SSH + socat). gRPC dials lazily, so
`client/connect.go` pings the server within `--connect-timeout` to surface a stuck ssh (a locked
SSH agent waits silently). That timeout bounds only the Ping, never later RPCs or streams, and
gRPC's `MinConnectTimeout` must stay above it, or gRPC kills ssh first.

**Server** (`server/main.go`): gRPC server on port 25373. Initializes Docker client,
creates scheduler with chosen provisioner, subscribes to scheduler events. Status state
reconstructed entirely from event stream in `status.go`, on top of the job history loaded at
startup (`job_history.go`). Thread-safe via `serverStatusMutex`.
Client watchers get filtered event channels via `addClientListener()`.

**Scheduler** (`scheduler/scheduler.go`): Single-goroutine main loop processes jobs from
input channel, handles tick requests for scheduling decisions. Event broadcasting via
`Subscribe()` returning a lossless channel (unbounded queue per subscriber) + unsubscribe
function. Node pool auto-scales based on queued tasks vs capacity. Live artifact access via
`ArchiveLiveArtifact()`.

**Provisioners**: Implement `Provisioner` interface (`Provision`, `Shutdown`, `Wait`) and
`Node` interface (`Name`, `RunTask`, `Terminate`). Local provisioner wraps Docker on localhost.
OpenStack provisioner creates VMs, connects via SSH, and sends each node the images its tasks need
as zstd archives (`provisioner/openstack/archive.go`).

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
| `WatchJobs` | Server stream | Stream the jobs list (job-level events only; `limit` latest + running) |
| `DownloadArtifact` | Server stream | Chunked artifact download (completed or live) |
| `LoadImage` | Bidi stream | Upload compressed Docker images (zstd) |

## Code Conventions

- **gRPC-free commands**: Override `PersistentPreRunE` with a no-op (see `self_update.go`).
- **Help texts are the CLI's documentation**: every command has a `Long` (a test checks it), and
  `alfred help scripting` (`client/help_scripting.go`) is the guide for scripts and agents. A
  change to a flag, a JSON field or status, or an exit code updates them in the same commit.
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
  GitHub Actions `workflow_dispatch`. Binary SCP'd to `/opt/alfred/alfred-server`. The unit runs
  as `alfred` with `EnvironmentFile=/opt/alfred/env` (holds credentials: never print it). Admin
  access is `debian@alfred.tipee.dev` (passwordless sudo); the `alfred` account refuses the personal key.
- **Client releases**: Triggered by git tags (`YY.MM.DD` format). Builds 3 binaries:
  `alfred-linux-amd64`, `alfred-darwin-amd64`, `alfred-darwin-arm64`. Uploaded to GitHub releases.
- **CI**: `go test -v ./...` on every push (Go 1.21, ubuntu-latest). `nix.yaml` runs `nix build`
  on every push.
- **Telemetry**: the server writes JSON logs to journald. `deploy/telemetry/` (Docker Compose in
  `/opt/telemetry`, `just deploy-telemetry`) runs an OpenTelemetry Collector and node-exporter,
  which send the host's metrics and its journal (Alfred's JSON lines parsed) to
  `telemetry.tipee.dev`, i.e. ClickHouse (the `telemetry` MCP server reads it). Alfred's
  telemetry is its logs: everything it sends is in the journal too. An OTLP receiver listens on
  `127.0.0.1:4317/4318`, used only by the collector's own telemetry. Its token is only in `/opt/telemetry/.env` on the host. It replaces the Datadog
  Agent, stopped and masked on 2026-11-01 (Linear PF-182). Parts are copied from the `platform`
  repository's node collector: `deploy/telemetry/README.md` says which.
- **Self-update**: Client detects latest version from GitHub release redirect URL, downloads
  and atomically replaces its own binary.
- **Nix**: The flake's default package (`nix/packages/alfred.nix`) builds the client from source;
  consumers take `github:tipee-sa/alfred` as a flake input. Its version is `YYMMDD.HHMM` from the
  source's last commit time, and its commit the flake revision (`nix/build-info.nix`, as in tipee).
  A binary running from `/nix/store` skips the update check and refuses `self-update`.

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
  queues each event for every subscriber (`scheduler/subscriber.go`), whose own goroutine drains
  it. Lossless and never blocks: the server's state is rebuilt from these events, so never
  drop one.

- **Server state reconstruction** (`status.go:listenEvents`): Single goroutine consumes
  scheduler events, updates `serverStatus` under write lock, forwards to client watchers.
  Lock ordering: always `serverStatusMutex` before `clientListenersMutex`.

- **gRPC streaming handlers** (`server_jobs.go`): Each connected client gets its own goroutine
  (managed by gRPC framework). Registers filtered listener, loops with `select` on event
  channel + `srv.Context().Done()` (client disconnect).

- **Client watch** (`client/watch.go`): Recv goroutine decouples blocking `c.Recv()` from
  main select loop (which, on a terminal, also handles a 1-second ticker for display refresh).

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
  match the checked-in generated code (pinned in `nix/packages/`). Newer plugins generate different interfaces.
- **`vendorHash` follows `go.sum`**: Any `go.mod`/`go.sum` change breaks the Nix package (and
  `nix.yaml`) until `vendorHash` in `nix/packages/alfred.nix` is updated. A failed `nix build`
  prints the new hash, or reports inconsistent vendoring if the old modules are still in the store.
- **Step images travel as tags, not IDs**: the client tags each built image `alfred-image:<id>`
  and sends that tag. The ID depends on the image store (classic: config digest, containerd:
  manifest digest), so after `docker save | docker load` across stores it changes; the tag
  survives. The OpenStack node still re-tags bare `sha256:` IDs for old clients.
  The client builds with `BUILDX_NO_DEFAULT_ATTESTATIONS=1`: on the containerd store, the default
  provenance attestation (it records the build's time) gave each build of an unchanged image a new
  ID, so the server and every node took it for a new image. It reads the ID with `docker image
  inspect` of a temporary build tag, never from `--iidfile`: without attestations, the containerd
  store's `--iidfile` holds a digest the daemon does not know ("No such image" on `docker tag`,
  client 26.11.04). `TestBuildImageTagsAnImageTheDaemonKnows` runs a real build to catch that.
- **One image archive for all nodes**: the server runs `docker save | zstd` once per image, into
  `<server-data>/image-archives` (emptied at startup and shutdown), and every node's transfer
  streams that file. An archive unused for an hour is deleted: every job brings new images (up to
  ~50 a day, 140-320 MB each), which would fill the disk between restarts. When a job
  lands on a running fleet, all nodes want its images at once, and 15 concurrent `docker save` of
  one 1.1 GB image took 73-97 s each, against 11 s alone. The archives are built when the job
  arrives (the scheduler calls the optional `ImagePreparer`), not at the first transfer: a new VM
  takes a minute or more to come online, and the build (11-13 s per image) runs meanwhile. Bare
  `sha256:` IDs from old clients are not prepared: they need their server-side tag first.
- **OpenStack keypair**: one per host (`alfred-server-<hostname>`), replaced at startup and
  deleted at shutdown, so an unclean exit leaves nothing new against the project's quota.
- **SIGTERM is a shutdown like SIGINT**: the unit's `KillSignal=SIGINT` does not cover
  `systemctl kill`.
- **Job history survives restarts**: each completed job is saved as
  `<server-data>/artifacts/<job>/job.json` (protojson of `JobStatus`) and the latest
  `maxCompletedJobs` are loaded at startup, so `ps`, `watch` and `artifact` work on them. It
  shares the artifacts' `--artifact-retention`. A shutdown's aborted jobs are saved too: the
  scheduler closes its subscriptions after the last event, and `main` waits (5 s at most) for
  `listenEvents` to apply them. Lists never send the whole history: `jobsForList` gives `ps`
  its `--limit` (default 20) and `top` 50 latest jobs, plus the running ones, without jobfile
  and command line (131 jobs were 4.6 MB without them; the client takes 50 MB at most).
  The jobs from 2026-09-10 to 2026-11-06 were rebuilt from the journal: no `StartedBy`, `About`,
  jobfile or command line, and `UNKNOWN` for the ~2% of tasks whose end journald dropped.
- **Host timers** (not in this repository): `alfred-docker-prune.timer` runs `docker system
  prune --all --force --filter until=24h`, then `docker volume prune --force`, every Sunday at
  03:30: only images older than a day, so a job loaded but not started yet keeps its own
  (`until` is not supported with `--volumes`, hence two commands). A weekly `systemctl kill alfred` timer used to work around
  OpenStack tokens expiring; `AllowReauth` fixed that (2026-10-11), and the timer was disabled on
  2026-11-06.
- **Service images are pulled once per daemon**: tasks take a lock per Docker daemon and image
  (`servicePulls` in `provisioner/internal/docker.go`), and a pull's progress is decoded for the
  error a failed pull ends with (the daemon answers it with a success). The OpenStack node image
  ships with the service images, so pulls only happen when a jobfile's service image changes.
- **Service container logs**: collected only on startup failure; step containers always stream
  theirs.
- **MySQL `--innodb-fast-shutdown=2`**: Breaks MySQL's init sequence (temporary server and
  real server disagree on redo logs). Don't use it in service health benchmarks.
- **Workspace defer order matters**: Artifacts must be archived BEFORE workspace deletion
  (LIFO defer order). Getting this wrong loses artifacts.
- **The server's logs are its telemetry**: they reach ClickHouse through journald, and a job's
  timeline is read from them, so a record's message and attribute keys are what queries match.
  Durations are seconds (`durationSeconds`, `queuedSeconds`, ...). At `ALFRED_LOG_LEVEL=debug`
  (the host's), a 2000-task job outruns journald's rate limit, which drops lines ("Suppressed N
  messages from alfred.service", 68 times from 2026-09-10 to 2026-11-06), so they never reach
  ClickHouse either. A task's records carry
  `task.job`, `task.name` and, once scheduled, `node.name`; the stages log their end with a
  duration ("Running task", "Task completed", "Step started"/"Step finished", "Service started",
  "Preserved artifacts", "Node is online", "Job completed", "Loaded image from client", "Image
  transferred to node"). "Step finished" adds the container's peak memory and CPU
  (`provisioner/internal/usage.go`), and each OpenStack node logs "Node state" every 15 s, read
  over its SSH connection (`provisioner/openstack/state.go`). Events, not metrics, by choice:
  what matters is what a given job went through, not trends.
- **Exit code 42**: Treated as "completed with warnings" (⚠️) by the watch command display.
  Task status is `FAILED` with exit code 42, `"warning"` in the JSON. Triggers
  `--abort-on-warning` but not `--abort-on-error` (which is for `"failed"` and `"timed_out"`).
- **Deprecated flags warn on stderr themselves** (see hidden `--abort-on-failure`), not with
  `MarkDeprecated`: cobra prints that to stdout, the client's cobra output, and corrupts `--json`.
- **Exit code 43**: Treated as "skipped — prerequisite failed" (⏭️). For infrastructure problems
  (e.g., missing/corrupted database backups) where retrying or aborting other tasks won't help.
  Task status is `SKIPPED`. Excluded from both `--abort-on-warning` and `--abort-on-error`.
- **`run` follows its job through `watchJob`**: both commands register the watch flags with
  `addWatchFlags` and read them with `watchOptionsFromFlags`. Add a watch flag there, not to
  `watchCmd` alone.
- **stdout carries only a command's output** (JSON lines, file paths, `-n` YAML): spinners,
  headers, messages and `docker build` output go to stderr.
- **`watch --json` is a script contract** (`client/watch_json.go`): only ever add fields or
  statuses. Exit codes are part of it: 0 once the job's completion was seen (or `--once` printed
  its snapshot), whatever the tasks' outcome; never derive them from task statuses.
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
