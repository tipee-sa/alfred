package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
	watchCmd.Flags().Bool("abort-on-failure", false, "cancel remaining tasks when a task exits with code 42")
	watchCmd.Flags().Bool("abort-on-error", false, "cancel remaining tasks when a task fails (excludes exit 42)")
}

var watchCmd = &cobra.Command{
	Use:   "watch [JOB]",
	Short: "Watch a job execution",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		abortOnFailure := lo.Must(cmd.Flags().GetBool("abort-on-failure"))
		abortOnError := lo.Must(cmd.Flags().GetBool("abort-on-error"))

		c, err := client.WatchJob(cmd.Context(), &proto.WatchJobRequest{Name: args[0]})
		if err != nil {
			return err
		}

		spinner := ui.NewSpinner("Waiting for job data")

		renderer := &watchRenderer{
			jobName: args[0],
			verbose: verbose,
			termWidth: func() int {
				w, _, _ := term.GetSize(int(os.Stderr.Fd()))
				if w <= 0 {
					return 80
				}
				return w
			},
			now: time.Now,
		}

		// Verbose mode: one-shot dump of current state, no refresh loop.
		// Useful when the output is too large for in-place terminal updates.
		if verbose {
			spinner.FinalMSG = ""
			spinner.Stop()

			msg, err := c.Recv()
			if err != nil {
				return fmt.Errorf("failed to receive job status: %w", err)
			}

			taskNames, stats := renderer.renderStats(msg)
			timestamp := renderer.renderTimestamp(msg)
			status := "running"
			statusColor := color.HiYellowString("⚙")
			if msg.CompletedAt != nil {
				status = "completed"
				statusColor = color.HiGreenString("✓")
			}
			fmt.Fprintf(os.Stderr, "%s Job '%s' %s (%s%d, %s)\n%s\n", statusColor, args[0], status, emojiLabel("📝"), len(taskNames), timestamp, stats)
			return nil
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

		return runWatchLoop(
			cmd.Context(),
			msgCh,
			renderer,
			args[0],
			os.Stderr,
			func() { spinner.FinalMSG = ""; spinner.Stop() },
			func() { spinner.FinalMSG = ""; spinner.Stop() },
			func() { spinner.Fail() },
			func(msg *proto.JobStatus) {
				if !abortOnFailure && !abortOnError {
					return
				}
				for _, t := range msg.Tasks {
					if t.Status == proto.TaskStatus_FAILED {
						shouldAbort := (abortOnFailure && *t.ExitCode == 42) || (abortOnError && *t.ExitCode != 42)
						if shouldAbort {
							if _, err := client.CancelJob(cmd.Context(), &proto.CancelJobRequest{Name: args[0]}); err != nil {
								fmt.Fprintf(os.Stderr, "%s Failed to cancel job '%s': %v\n", color.HiRedString("✗"), args[0], err)
							} else {
								abortOnFailure, abortOnError = false, false
							}
							break
						}
					}
				}
			},
		)
	},
}

// runWatchLoop runs the watch event loop, processing messages from msgCh,
// rendering output and managing cursor visibility. Returns nil on clean
// exits (EOF, context cancellation) and the stream error for real failures.
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

	render := func() {
		if displayLines > 0 {
			fmt.Fprintf(w, "\033[%dA", displayLines)
		}
		fmt.Fprint(w, "\r\033[J")
		output, lines := renderer.renderOutput(lastMsg)
		fmt.Fprint(w, output)
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
							fmt.Fprintf(w, "\033[%dA\r\033[J", displayLines)
						}
						fmt.Fprint(w, "\033[?25h")
					} else if onCleanStop != nil {
						onCleanStop()
					}
					taskNames, stats := renderer.renderStats(lastMsg)
					timestamp := renderer.renderTimestamp(lastMsg)
					fmt.Fprintf(w, "%s Job '%s' completed (%s%d, %s)\n%s\n", color.HiGreenString("✓"), jobName, emojiLabel("📝"), len(taskNames), timestamp, stats)
					return nil
				}
				if ctx.Err() == context.Canceled {
					if !started && onCleanStop != nil {
						onCleanStop()
					}
					// Keep current output visible, just move past it and restore cursor
					if started {
						fmt.Fprint(w, "\n")
					}
					return nil
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
