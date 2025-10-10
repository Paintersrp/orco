# Blue/Green Updates

Orco supports atomic blue/green deploys by setting `update.strategy: blueGreen` on a service. The engine provisions a full green replica set, waits for every instance to report readiness, performs a single cutover, and only then decommissions the blue replicas. The following phases are observable through `UpdatePhase` events:

1. **ProvisionGreen** – all green replicas are created.
2. **Verify** – readiness gates the cutover.
3. **Cutover** – traffic switches according to `update.blueGreen.switch` (ports or proxyLabel).
4. **DecommissionBlue** – blue replicas are drained with the configured `drainTimeout`.

Optional settings under `update.blueGreen` provide additional control:

- `drainTimeout`: how long to wait for blue replicas to stop after the cutover.
- `rollbackWindow`: how long to watch green replicas for regressions and automatically roll back if a failure occurs.
- `switch`: the traffic switching mode (`ports` or `proxyLabel`).

If verification fails or a regression occurs during the rollback window, Orco aborts the update, tears down the green replica set, and leaves the blue replicas serving traffic. See `examples/bluegreen-stack.yaml` for a reference manifest.
