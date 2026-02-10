### Known bugs
- [ ] Loading multiple jobs at the same time seems to conflict (nodes are reused even though the images are not the same ?)

### Improvements
- [ ] **Provisioner cancellation**: `Provisioner.Provision()` does not accept a
  `context.Context`, so provisioning cannot be cancelled during server shutdown.
  For the local provisioner this is a no-op (instant), but for OpenStack the SSH
  retry loop in `node.connect()` blocks up to ~1 minute with no way to interrupt
  it. Fix: add `ctx context.Context` to the `Provision()` interface, pass it
  through to `node.connect()`, and derive it from the scheduler's stop channel in
  `watchNodeProvisioning()`. Related TODOs: `scheduler/provisioner.go:4`,
  `provisioner/openstack/node.go:84`, `scheduler/scheduler.go:835`.
