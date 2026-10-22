package main

import "github.com/spf13/cobra"

// scriptingHelp is a help topic (`alfred help scripting`): a command without
// Run, which cobra lists under "Additional help topics".
var scriptingHelp = &cobra.Command{
	Use:   "scripting",
	Short: "Using alfred from scripts, CI and AI agents",
	Long: `Using alfred from scripts, CI and AI agents.

No terminal is needed, except for 'alfred top'. Without one, colors and
spinners are off and 'run' and 'watch' print one plain line per status change.

Start a job, get its name
  alfred run --async --json job.yaml | jq -r .job
  ('--async' alone prints "Scheduled job 'NAME'"; without '--async', 'run'
  watches the job like 'alfred watch' until it completes.)

Follow it
  alfred watch --json NAME          one JSON line per status change, until
                                    the job completes
  alfred watch --json --once NAME   one line with the current status

JSON lines (a stable contract: fields and statuses are only ever added)
  job, state, scheduled_at, completed_at, counts, and either tasks or changed.
  state      "running", then "completed" once every task has ended, however
             it ended (a cancelled job completes too): the outcome is in the
             task statuses.
  counts     tasks per status, every status included (even at 0), and "total".
  tasks      every task: on the first line, the completed line and with --once.
  changed    only the tasks that changed since the previous line, on the
             lines in between.
  Each task: name, status, exit_code, started_at, ended_at. Statuses: queued,
  running, completed, warning (exit 42), failed (any other non-zero exit),
  timed_out, skipped (exit 43: a prerequisite was missing), aborted (cancelled).

Exit codes
  run, watch   1 if anything fails before the job's tasks run (jobfile, build,
               upload, scheduling, connection), or if the job's final status
               was not seen (interrupted, connection lost, server shutdown).
               Otherwise 0, whatever the tasks' outcome: read their statuses.
  artifact     1 if a requested task does not exist or a download failed.

Logs and artifacts
  alfred logs NAME TASK -n all      whole step logs, running or finished task
  alfred artifact NAME -o DIR       DIR/TASK.tar.zst per task; the paths
                                    written are printed on stdout, one per line

Connecting
  --remote (env ALFRED_REMOTE) picks the server. Except to localhost, alfred
  connects through an ssh tunnel, so it needs the SSH key the server accepts.
  A locked SSH agent does not refuse, it waits: alfred gives up after
  --connect-timeout (default 1m, env ALFRED_CONNECT_TIMEOUT) and says so.`,
}
