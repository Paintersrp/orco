# Orco CLI reference

The Orco CLI exposes commands for inspecting orchestration state and streaming
structured events. This guide documents the available output formats with an
emphasis on machine-readable JSON payloads and their stability guarantees.

## Global conventions

Many commands support the `--output, -o` flag to select the rendering format.
Unless noted otherwise, additional fields may be introduced in future releases
while preserving existing keys and their types. Clients should therefore treat
unknown properties as optional extensions and continue to rely on the documented
fields remaining stable.

All timestamps are emitted in RFC 3339 format with UTC offsets (e.g.
`2024-03-22T18:41:09.547123Z`). Durations are encoded as Go-style strings such
as `30s` or `1m30s` when present.

## `orco status`

Displays the orchestrator snapshot for the current stack. The command accepts:

- `--output text` (default): tabular terminal output with optional history
  sections when `--history` is provided.
- `--output json`: machine-readable status report with stable field names.

### JSON schema

The JSON payload mirrors `internal/api.StatusReport`:

```json
{
  "stack": "string",
  "version": "string",
  "generated_at": "RFC3339 timestamp",
  "services": {
    "<service-name>": {
      "name": "string",
      "state": "string enum of engine.EventType",
      "ready": true,
      "restarts": 0,
      "replicas": 1,
      "ready_replicas": 1,
      "message": "string",
      "first_seen": "RFC3339 timestamp",
      "last_event": "RFC3339 timestamp",
      "resources": {
        "cpu": "string",
        "memory": "string"
      },
      "history": [
        {
          "timestamp": "RFC3339 timestamp",
          "type": "string enum of engine.EventType",
          "reason": "string",
          "message": "string"
        }
      ],
      "last_reason": "string"
    }
  }
}
```

Field stability expectations:

- Keys listed above are maintained for compatibility. The orchestrator may add
  new keys to `services` entries (for example, hints about future rollouts), but
  existing keys will not be removed or change type without a major release.
- Additional services may appear in the map when the tracker has data for
  services that are no longer defined in the current manifest.

### Example

```bash
orco status --output json --history 5
```

Sample output (truncated for brevity):

```json
{
  "stack": "demo",
  "version": "0.1",
  "generated_at": "2024-03-22T18:41:09.547123Z",
  "services": {
    "api": {
      "name": "api",
      "state": "ready",
      "ready": true,
      "restarts": 1,
      "replicas": 2,
      "ready_replicas": 2,
      "message": "",
      "first_seen": "2024-03-22T18:36:02.104311Z",
      "last_event": "2024-03-22T18:40:55.993214Z",
      "resources": {"cpu": "500m", "memory": "512MB"},
      "history": [
        {
          "timestamp": "2024-03-22T18:40:55.993214Z",
          "type": "ready",
          "reason": "probe: http",
          "message": "service reported readiness"
        }
      ],
      "last_reason": "probe: http"
    }
  }
}
```

## `orco logs`

Streams structured log events from the active deployment. Flags include:

- `--follow, -f`: Continue streaming until interrupted.
- `--since <duration>`: Only emit entries newer than the provided duration.
- `--output json` (default and only supported format): Emit newline-delimited
  JSON records.

### JSON schema

Each log entry matches `internal/cliutil.LogRecord`:

```json
{
  "ts": "RFC3339 timestamp",
  "service": "string",
  "replica": 0,
  "level": "info|warn|error|<custom>",
  "msg": "string",
  "source": "stdout|stderr|system|<runtime-specific>"
}
```

Stability notes:

- All keys are stable. Future releases may add additional optional properties
  (for example, structured context) but existing keys and their meanings are not
  expected to change.
- Messages pass through the CLI's redaction filters, ensuring secrets masked in
  upstream logs remain hidden.

### Example

```bash
orco logs --since 5m --output json
```

Sample output (two events):

```json
{"ts":"2024-03-22T18:41:04.112394Z","service":"api","replica":0,"level":"info","msg":"starting HTTP listener","source":"stdout"}
{"ts":"2024-03-22T18:41:05.223501Z","service":"api","replica":0,"level":"info","msg":"probe succeeded","source":"system"}
```

## `orco graph`

Renders the dependency graph for the stack. Supported outputs are:

- `--output text` (default): ASCII tree including readiness summaries.
- `--output dot`: Graphviz DOT with dependency metadata and blocking reasons.
- `--output json`: Machine-readable representation suitable for visualization.

### JSON schema

The JSON structure mirrors the internal graph serialization types:

```json
{
  "stack": "string",
  "version": "string",
  "nodes": [
    {
      "name": "string",
      "state": "string enum of engine.EventType",
      "ready": true,
      "message": "string",
      "restarts": 0,
      "replicas": 1,
      "readyReplicas": 1
    }
  ],
  "edges": [
    {
      "from": "string",
      "to": "string",
      "require": "ready|started|healthy|<custom>",
      "timeout": "duration string",
      "blockingReason": "string"
    }
  ]
}
```

Stability notes:

- The top-level keys and required fields of `nodes` and `edges` are stable.
  Optional fields such as `message`, `restarts`, `replicas`, `readyReplicas`,
  `timeout`, and `blockingReason` may be omitted when empty.
- Additional optional fields may appear in the future to expose richer graph
  metadata (for example, rollout hints). Consumers should ignore unknown keys.

### Example

```bash
orco graph --output json
```

Sample output:

```json
{
  "stack": "demo",
  "version": "0.1",
  "nodes": [
    {"name":"db","state":"ready","ready":true},
    {"name":"api","state":"ready","ready":true,"restarts":1,"replicas":2,"readyReplicas":2},
    {"name":"web","state":"pending","ready":false,"message":"waiting for api"}
  ],
  "edges": [
    {"from":"api","to":"db","require":"ready"},
    {"from":"web","to":"api","require":"ready","timeout":"1m"}
  ]
}
```

---

For additional command details, run `orco <command> --help`.
