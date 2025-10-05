# Orco

Orco is a single-node orchestration engine that delivers most of the ergonomics of a modern orchestrator without the overhead of running a full Kubernetes control plane. It focuses on dependency-aware startup, health-gated restarts, structured logging, and progressive rollout strategies for services defined in a declarative `stack.yaml` file.

## Getting started

The repository now includes a Go implementation of the Orco CLI. To experiment with the parser and planning utilities:

1. Build the binary: `go build ./cmd/orco`
2. Run commands against the sample manifest in `examples/demo-stack.yaml`, for example:

   ```bash
   ./orco --file examples/demo-stack.yaml status
   ./orco --file examples/demo-stack.yaml graph --dot
   ./orco --file examples/demo-stack.yaml up
   ```

These commands currently validate the stack definition, construct the dependency DAG, and display planning information while the execution engine is developed. Future work will add real runtimes, health gating, and supervisors on top of this foundation.

## Problem statement

Traditional tooling such as `docker-compose` is optimized for "bring up these services" but leaves orchestration concerns to operators. This leads to awkward limitations:

- Dependencies are expressed with loose ordering semantics instead of a proper graph.
- Services launch even when their dependencies are unhealthy.
- Restarts happen in bulk, creating stampedes across the stack.
- There is no first-class support for canary rollouts on a single machine.
- Logs are unstructured and difficult to aggregate by service or replica.

Kubernetes addresses these issues, but at the cost of significantly more moving parts than most single-machine deployments require. Orco aims to provide roughly 90% of the orchestration experience with 10% of the complexity.

## Mental model

- **Stack**: The entire application described by `stack.yaml`.
- **Service**: A runnable unit (container or local process) that can expose one or more replicas.
- **Dependency**: Directed edges between services that include readiness requirements.
- **Probe**: A health check that determines whether a service instance is ready.
- **Policy**: Restart rules, timeouts, exponential backoff settings, and update strategies.
- **Engine**: Constructs a DAG, manages supervisors, and gates startup on readiness conditions.
- **Runtime**: Pluggable backends such as Docker containers or local processes.

## Stack specification (`stack.yaml`)

Version 0.1 of the schema focuses on explicit configuration with orchestration-first semantics:

```yaml
version: 0.1

stack:
  name: demo
  workdir: ./

defaults:
  restartPolicy:
    maxRetries: 5
    backoff:
      min: 1s
      max: 30s
      factor: 2.0
  health:
    gracePeriod: 5s
    interval: 2s
    timeout: 1s
    failureThreshold: 3

services:
  db:
    image: postgres:16
    runtime: docker
    env:
      POSTGRES_PASSWORD: example
    ports: [ "5432:5432" ]
    health:
      http:
        url: http://localhost:5432/healthz
        expectStatus: [200,204]
    update:
      strategy: rolling
      maxUnavailable: 0
      maxSurge: 1
    replicas: 1

  api:
    image: ghcr.io/acme/api:latest
    runtime: docker
    envFromFile: ./secrets/api.env
    dependsOn:
      - target: db
        require: ready
        timeout: 60s
    health:
      http:
        url: http://localhost:8080/health
        expectStatus: [200]
      log:
        pattern: "ready"
        sources: ["stderr"]
      expression: http || log
    ports: [ "8080:8080" ]
    replicas: 2
    restartPolicy:
      maxRetries: 10
      backoff:
        min: 500ms
        max: 20s
        factor: 1.8

  web:
    runtime: process
    command: [ "./web", "--port=3000" ]
    env:
      API_URL: http://localhost:8080
    dependsOn:
      - target: api
        require: ready
    health:
      http:
        url: http://localhost:3000/healthz
        expectStatus: [200]
    ports: [ "3000:3000" ]
    replicas: 1
```

Key behaviors:

- `dependsOn.require` controls whether Orco waits for dependencies to start, exist, or reach readiness.
- Probes support HTTP, TCP, command, and log checks, and an optional `expression` enables simple `OR` combinations.
- `runtime` can be either `docker` or `process`, enabling mixed workloads.

## Engine design

### Dependency graph

The engine constructs a DAG that contains each service (or service replica) as a node and validates it with a topological sort. Cycles are rejected with clear diagnostics.

### Supervisors

Each service is managed by a supervisor goroutine that controls lifecycle transitions (`Pending → Starting → Running → Stopping → Stopped/Failed`). Supervisors interact with the runtime backend to start and stop instances, monitor health, apply restart policies, and emit structured events such as `ServiceReady` or `ServiceFailed` onto an internal event bus. Supervisors only begin startup once their dependencies satisfy the required state.

### Health probes

HTTP, TCP, command, and log probes can be mixed per service. Each probe has configurable grace periods, intervals, timeouts, and success/failure thresholds. When multiple probes are configured, the optional `expression` field allows simple `OR` logic between probe aliases (`http`, `tcp`, `cmd`, `log`). Probes determine when a service transitions between Ready and Unready, ensuring downstream dependencies observe accurate status.

### Progressive restarts & rolling updates

Rolling updates update one replica at a time. Parameters such as `maxUnavailable` and `maxSurge` constrain concurrency, ensuring availability while updates proceed. Future versions plan to add canary support for targeted rollouts.

### Backoff strategy

Failed services restart using exponential backoff with jitter, bounded by configurable minimum and maximum delays. After exceeding `maxRetries`, the service transitions to `Failed`, and dependents remain blocked until the failure is resolved.

## Runtime backends

- **Docker runtime**: Uses the Docker Engine API to pull images, create containers, manage their lifecycle, and stream logs while relying on Orco's own health probes for consistency.
- **Process runtime**: Launches local processes via `exec.Cmd`, managing process groups, stdout/stderr capture, and optional working directory or environment settings. Resource limits are deferred to future releases.

## Structured logging

All runtime outputs are normalized into structured NDJSON with fields such as timestamp, service, replica, level, message, source, and arbitrary metadata. The logging layer supports fan-in for commands like `orco logs`, drives the TUI log pane, and can optionally persist to disk. Backpressure is controlled via bounded channels that emit "dropped" meta-events when overwhelmed.

## TUI

The terminal UI presents a live status table and tailing logs:

```
SERVICE  STATE     READY  REPL  RESTARTS  AGE    MESSAGE
db       Running   true   1/1   0         1m12s  listening on 5432
api      Running   true   2/2   1         58s    200 OK /health (avg 25ms)
web      Starting  false  1/1   0         2s     waiting for api (ready)
```

Users can navigate services, focus the log pane, and filter by level or regex. Libraries such as Bubble Tea or tview provide the foundation.

## CLI commands

```
orco up [-f stack.yaml]
orco down
orco status
orco logs [service] [--since DURATION] [-f]
orco restart [service]
orco graph [--dot]
orco apply
```

Use `--since` to restrict streaming to recent activity, for example `orco logs --since 10m` only emits log records from the last ten minutes.

Command behavior emphasizes descriptive errors, for example: `api blocked: dependency db not Ready (timeout 60s, probe failing tcp connect)`.

## Comparison

- **Docker Compose**: Orco adds health gating, progressive restarts, rolling updates, structured logs, and a first-class DAG.
- **systemd**: Offers multi-service DAG orchestration, container/process abstraction, built-in logging fan-in, and rolling semantics without bespoke unit files.
- **Kubernetes**: Provides similar orchestration ergonomics for single-node workloads without the overhead of a distributed control plane.

## Roadmap

### v0.1 must-haves

- Parse and validate `stack.yaml`.
- Build the dependency DAG and gate startup on readiness requirements.
- Implement Docker and process runtimes.
- Support HTTP, TCP, command, and log health probes with optional `OR` expressions.
- Supervisors with exponential backoff and restart limits.
- Implement `orco up`, `orco down`, `orco status`, and `orco logs -f`.
- Ship the TUI with status and log panes.
- Provide `orco graph --dot`, `orco restart`, and `orco apply` with rolling updates.

### Deferred for v0.2

- Canary update workflows with traffic splitting.
- Secrets templating, volumes, and resource controls.
- Reverse proxy management for traffic shaping.
- Automated dampening for dependency failure cascades.

