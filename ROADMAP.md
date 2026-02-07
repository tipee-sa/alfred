### Features
- [ ] Add new command "alfred cancel <job-id>" to cancel a running job
  - Add `EventNode(Aborted|Killed|Shutdown|...?)`
  - Implement `Event(Node|Task)Aborted`
  - Re-test the scheduler's shutdown's logic, which seems to be only partially implemented at the moment
  - Allow to pass options `--abort-on-error` and/or `--abort-on-failure` when running a job to auto-abort early
- [ ] Add new command "alfred top" to have an overview of the current state of Alfred's server and the running nodes 

### Known bugs
- [ ] Loading multiple jobs at the same time seems to conflict (nodes are reused even though the images are not the same ?)
- [ ] The terminal cursor/state is broken after running "alfred watch", forces to run "reset" or "stty sane" (we tried to remove option `spinner.WithHiddenCursor(true)` but it had no effect. The issue probably comes from the "multiline" spinner). 
