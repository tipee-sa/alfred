package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// recvResult holds the result of a gRPC Recv() call.
type recvResult struct {
	msg *proto.JobStatus
	err error
}

func init() {
	addWatchFlags(watchCmd)
}

// addWatchFlags registers the flags of watch, which run shares: run follows its job as watch does.
func addWatchFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("abort-on-failure", false, "cancel remaining tasks when a task exits with code 42 (excludes exit 43)")
	cmd.Flags().Bool("abort-on-error", false, "cancel remaining tasks when a task fails (excludes exit 42 and 43)")
	cmd.Flags().Bool("json", false, "write one JSON line to stdout per status change, for scripts")
	cmd.Flags().Bool("once", false, "print the current status once to stdout and exit, even if the job is still running (with -v, untruncated)")
}

// watchOptions holds the flags registered by addWatchFlags.
type watchOptions struct {
	abortOnFailure, abortOnError, json, once bool
}

func watchOptionsFromFlags(cmd *cobra.Command) watchOptions {
	return watchOptions{
		abortOnFailure: lo.Must(cmd.Flags().GetBool("abort-on-failure")),
		abortOnError:   lo.Must(cmd.Flags().GetBool("abort-on-error")),
		json:           lo.Must(cmd.Flags().GetBool("json")),
		once:           lo.Must(cmd.Flags().GetBool("once")),
	}
}

var watchCmd = &cobra.Command{
	Use:   "watch [JOB]",
	Short: "Watch a job execution",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		return watchJob(cmd, args[0], watchOptionsFromFlags(cmd))
	},
}

// watchJob follows the job until it completes, or with once prints its current status, as
// text or (opts.json) as JSON lines. It returns nil only if it showed the job's final status
// (or, with once, its current one), so that exit code 0 always leaves a status to check.
func watchJob(cmd *cobra.Command, jobName string, opts watchOptions) error {
	abortOnFailure, abortOnError := opts.abortOnFailure, opts.abortOnError
	jsonOutput, once := opts.json, opts.once

	c, err := client.WatchJob(cmd.Context(), &proto.WatchJobRequest{Name: jobName})
	if err != nil {
		return err
	}

	abortIfNeeded := func(msg *proto.JobStatus) {
		if !abortOnFailure && !abortOnError {
			return
		}
		for _, t := range msg.Tasks {
			if t.Status == proto.TaskStatus_TIMED_OUT && abortOnError {
				if _, err := client.CancelJob(cmd.Context(), &proto.CancelJobRequest{Name: jobName}); err != nil {
					fmt.Fprintf(os.Stderr, "%s Failed to cancel job '%s': %v\n", color.HiRedString("✗"), jobName, err)
				} else {
					abortOnFailure, abortOnError = false, false
				}
				break
			}
			if t.Status == proto.TaskStatus_FAILED {
				shouldAbort := (abortOnFailure && *t.ExitCode == 42) || (abortOnError && *t.ExitCode != 42)
				if shouldAbort {
					if _, err := client.CancelJob(cmd.Context(), &proto.CancelJobRequest{Name: jobName}); err != nil {
						fmt.Fprintf(os.Stderr, "%s Failed to cancel job '%s': %v\n", color.HiRedString("✗"), jobName, err)
					} else {
						abortOnFailure, abortOnError = false, false
					}
					break
				}
			}
		}
	}

	// Recv goroutine: decouples blocking gRPC Recv() from the main select loop.
	// Without this, we couldn't multiplex between incoming messages and the ticker.
	// Buffered channel (1) allows the goroutine to send one message ahead without blocking.
	// The goroutine exits when Recv returns an error (io.EOF on stream end, or real error).
	msgCh := make(chan recvResult, 1)
	go func() {
		for {
			msg, err := c.Recv() // blocks until server sends or stream ends
			msgCh <- recvResult{msg, err}
			if err != nil {
				return // EOF or error: stop reading
			}
		}
	}()

	// JSON mode: no spinner, colors or cursor control, so it works headless and piped.
	if jsonOutput {
		return runWatchJSON(cmd.Context(), msgCh, jobName, cmd.OutOrStdout(), once, abortIfNeeded)
	}

	// --once: the current status is the command's output, so it goes to stdout.
	if once {
		out := cmd.OutOrStdout()
		return printWatchOnce(cmd.Context(), msgCh, newWatchRenderer(jobName, verbose, out), out, !isTerminal(out))
	}

	// Off a terminal (an agent's shell, CI, a pipe), redrawing in place would write a copy
	// of the display every second: write one plain line per status change instead.
	if !isTerminal(os.Stderr) {
		return runWatchPlain(cmd.Context(), msgCh, newWatchRenderer(jobName, verbose, os.Stderr), os.Stderr, abortIfNeeded)
	}

	spinner := ui.NewSpinner("Waiting for job data")
	renderer := newWatchRenderer(jobName, verbose, os.Stderr)

	return runWatchLoop(
		cmd.Context(),
		msgCh,
		renderer,
		jobName,
		os.Stderr,
		func() { spinner.FinalMSG = ""; spinner.Stop() },
		func() { spinner.FinalMSG = ""; spinner.Stop() },
		func() { spinner.Fail() },
		abortIfNeeded,
	)
}

// runWatchLoop runs the watch event loop on a terminal, processing messages from msgCh,
// redrawing the display in place and managing cursor visibility. Once the stream ends, it
// replaces the display with the job's summary. Returns nil only if it saw the job complete;
// an interruption, a stream that ends before the job completed, or a stream error is an error.
//
// Callbacks handle spinner lifecycle (nil-safe):
//   - onFirstMessage: called when first message arrives (e.g. stop spinner)
//   - onCleanStop: called on clean exit before first message (e.g. stop spinner cleanly)
//   - onFailStop: called on error before first message (e.g. fail spinner)
//   - onMessage: called after each received message (e.g. abort-on-failure logic)
func runWatchLoop(
	ctx context.Context,
	msgCh <-chan recvResult,
	renderer *watchRenderer,
	jobName string,
	w io.Writer,
	onFirstMessage func(),
	onCleanStop func(),
	onFailStop func(),
	onMessage func(*proto.JobStatus),
) error {
	var lastMsg *proto.JobStatus
	var displayLines int
	var started bool

	// render redraws the display in place, as one write per frame. It overwrites each line and
	// clears only what is left of it, instead of clearing the whole display first: a terminal
	// that paints between the clear and the new text shows the display blank, which flickers.
	// The frame is also wrapped in synchronized-output markers (mode 2026), so terminals that
	// support them paint it at once; the others ignore an unknown mode.
	render := func() {
		output, lines := renderer.renderOutput(lastMsg)
		var frame strings.Builder
		frame.WriteString("\033[?2026h")
		if displayLines > 0 {
			fmt.Fprintf(&frame, "\033[%dA", displayLines)
		}
		frame.WriteString("\r")
		frame.WriteString(strings.ReplaceAll(output, "\n", "\033[K\n"))
		frame.WriteString("\033[K\033[J") // rest of the last line, then lines left by a taller frame
		frame.WriteString("\033[?2026l")
		fmt.Fprint(w, frame.String())
		displayLines = lines
	}

	// Ticker re-renders the display every second to update elapsed time counters
	// (job duration, task duration) even when no new events arrive from the server.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	defer fmt.Fprint(w, "\033[?25h") // always restore cursor visibility on exit

	// Main event loop: blocks on select until either a message arrives or the ticker fires.
	for {
		select {
		case result := <-msgCh: // new message (or error/EOF) from gRPC stream
			if result.err != nil {
				if result.err == io.EOF {
					if started {
						if displayLines > 0 {
							fmt.Fprintf(w, "\033[%dA", displayLines)
						}
						fmt.Fprint(w, "\r\033[J\033[?25h")
					} else if onCleanStop != nil {
						onCleanStop()
					}
					return finishWatch(w, lastMsg, jobName, renderer.renderSummary)
				}
				if ctx.Err() == context.Canceled {
					if !started && onCleanStop != nil {
						onCleanStop()
					}
					// Keep current output visible, just move past it and restore cursor
					if started {
						fmt.Fprint(w, "\n")
					}
					return errWatchInterrupted(jobName)
				}
				if started {
					if displayLines > 0 {
						fmt.Fprintf(w, "\033[%dA\r\033[J", displayLines)
					}
					fmt.Fprint(w, "\033[?25h")
					fmt.Fprintf(w, "%s Waiting for job data\n", color.HiRedString("✗"))
				} else if onFailStop != nil {
					onFailStop()
				}
				return result.err
			}
			lastMsg = result.msg
			if !started {
				if onFirstMessage != nil {
					onFirstMessage()
				}
				started = true
				fmt.Fprint(w, "\033[?25l")
			}
			if onMessage != nil {
				onMessage(result.msg)
			}
			render()

		case <-ticker.C: // 1-second tick: re-render to update elapsed time display
			if !started {
				continue // don't render before first message arrives
			}
			render()
		}
	}
}

// runWatchPlain follows the job when stderr is not a terminal (an agent's shell, CI, a pipe):
// one plain line, without escape codes, each time a task changes status, then the job's summary
// once the stream ends. Nothing is rewritten on a timer, so a log holds each change once.
// It returns like runWatchLoop.
func runWatchPlain(
	ctx context.Context,
	msgCh <-chan recvResult,
	renderer *watchRenderer,
	w io.Writer,
	onMessage func(*proto.JobStatus),
) error {
	var lastMsg *proto.JobStatus
	var previous map[string]string // task name → its status on the previous message; nil before the first
	for {
		var result recvResult
		select {
		case result = <-msgCh:
		case <-ctx.Done():
			return errWatchInterrupted(renderer.jobName)
		}
		if result.err == io.EOF {
			return finishWatch(w, lastMsg, renderer.jobName, renderer.renderPlainSummary)
		}
		if result.err != nil {
			if ctx.Err() != nil {
				return errWatchInterrupted(renderer.jobName)
			}
			return result.err
		}

		lastMsg = result.msg
		current := taskStatusNames(result.msg)
		// The completed state is left to the summary, which follows it.
		if result.msg.CompletedAt == nil && (previous == nil || !maps.Equal(previous, current)) {
			if _, err := fmt.Fprintln(w, renderer.renderPlainLine(result.msg, previous)); err != nil {
				return err
			}
		}
		previous = current
		if onMessage != nil {
			onMessage(result.msg)
		}
	}
}

// printWatchOnce writes the job's current status, as received first from msgCh, and returns.
// With plain, it is written without colours or emoji, as off a terminal.
func printWatchOnce(ctx context.Context, msgCh <-chan recvResult, renderer *watchRenderer, w io.Writer, plain bool) error {
	select {
	case result := <-msgCh:
		if result.err == io.EOF {
			return errStreamEnded(renderer.jobName)
		}
		if result.err != nil {
			return fmt.Errorf("failed to receive job status: %w", result.err)
		}
		summary := renderer.renderSummary
		if plain {
			summary = renderer.renderPlainSummary
		}
		_, err := fmt.Fprint(w, summary(result.msg))
		return err // the snapshot is the command's output: failing to write it is a failure
	case <-ctx.Done():
		return errWatchInterrupted(renderer.jobName)
	}
}

// finishWatch writes the job's last known status once its stream has ended. The server ends
// the stream right after sending the completed state, but also when it shuts down: only the
// former leaves a final status to check, so the latter is an error. So is failing to write it.
func finishWatch(w io.Writer, lastMsg *proto.JobStatus, jobName string, summary func(*proto.JobStatus) string) error {
	if lastMsg != nil {
		if _, err := fmt.Fprint(w, summary(lastMsg)); err != nil {
			return err
		}
	}
	if lastMsg == nil || lastMsg.CompletedAt == nil {
		return errStreamEnded(jobName)
	}
	return nil
}

func errWatchInterrupted(jobName string) error {
	return fmt.Errorf("interrupted before job '%s' completed", jobName)
}

func errStreamEnded(jobName string) error {
	return fmt.Errorf("stream ended before job '%s' completed", jobName)
}

func newWatchRenderer(jobName string, verbose bool, w io.Writer) *watchRenderer {
	return &watchRenderer{
		jobName:   jobName,
		verbose:   verbose,
		termWidth: func() int { return terminalWidth(w) },
		now:       time.Now,
	}
}

// isTerminal tells whether w writes to a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// terminalWidth returns the width of the terminal w writes to, or 80 if it is not one.
func terminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}
