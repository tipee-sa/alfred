package main

import (
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

func init() {
	watchCmd.Flags().Bool("abort-on-failure", false, "cancel remaining tasks when any task fails (excludes exit 42)")
	watchCmd.Flags().Bool("abort-on-error", false, "cancel remaining tasks on any non-zero exit (including exit 42)")
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

		var lastMsg *proto.JobStatus
		var displayLines int
		var started bool

		render := func() {
			if displayLines > 0 {
				fmt.Fprintf(os.Stderr, "\033[%dA", displayLines)
			}
			fmt.Fprint(os.Stderr, "\r\033[J")
			output, lines := renderer.renderOutput(lastMsg)
			fmt.Fprint(os.Stderr, output)
			displayLines = lines
		}

		// Recv goroutine: decouples blocking gRPC Recv() from the main select loop.
		// Without this, we couldn't multiplex between incoming messages and the ticker.
		// Buffered channel (1) allows the goroutine to send one message ahead without blocking.
		// The goroutine exits when Recv returns an error (io.EOF on stream end, or real error).
		type recvResult struct {
			msg *proto.JobStatus
			err error
		}
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

		// Ticker re-renders the display every second to update elapsed time counters
		// (job duration, task duration) even when no new events arrive from the server.
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		defer fmt.Fprint(os.Stderr, "\033[?25h") // always restore cursor visibility on exit

		// Main event loop: blocks on select until either a message arrives or the ticker fires.
		for {
			select {
			case result := <-msgCh: // new message (or error/EOF) from gRPC stream
				if result.err != nil {
					if result.err == io.EOF {
						if started {
							if displayLines > 0 {
								fmt.Fprintf(os.Stderr, "\033[%dA\r\033[J", displayLines)
							}
							fmt.Fprint(os.Stderr, "\033[?25h")
						} else {
							spinner.FinalMSG = ""
							spinner.Stop()
						}
						taskNames, stats := renderer.renderStats(lastMsg)
						timestamp := renderer.renderTimestamp(lastMsg)
						fmt.Fprintf(os.Stderr, "%s Job '%s' completed (%s%d, %s)\n%s\n", color.HiGreenString("✓"), args[0], emojiLabel("📝"), len(taskNames), timestamp, stats)
						return nil
					}
					if started {
						if displayLines > 0 {
							fmt.Fprintf(os.Stderr, "\033[%dA\r\033[J", displayLines)
						}
						fmt.Fprint(os.Stderr, "\033[?25h")
						fmt.Fprintf(os.Stderr, "%s Waiting for job data\n", color.HiRedString("✗"))
					} else {
						spinner.Fail()
					}
					return result.err
				}
				lastMsg = result.msg
				if !started {
					spinner.FinalMSG = ""
					spinner.Stop()
					started = true
					fmt.Fprint(os.Stderr, "\033[?25l")
				}
				if abortOnFailure || abortOnError {
					for _, t := range result.msg.Tasks {
						if t.Status == proto.TaskStatus_FAILED {
							shouldAbort := abortOnError || (abortOnFailure && (t.ExitCode == nil || *t.ExitCode != 42))
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
				}
				render()

			case <-ticker.C: // 1-second tick: re-render to update elapsed time display
				if !started {
					continue // don't render before first message arrives
				}
				render()
			}
		}
	},
}
