package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/gammadia/alfred/client/ui"
	"github.com/gammadia/alfred/proto"
	"github.com/rivo/uniseg"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch [JOB]",
	Short: "Watch a job execution",
	Args:  cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client.WatchJob(cmd.Context(), &proto.WatchJobRequest{Name: args[0]})
		if err != nil {
			return err
		}

		spinner := ui.NewSpinner("Waiting for job data")

		emoji := func(emoji string) string {
			return emoji + strings.Repeat(" ", utf8.RuneCountInString(emoji))
		}
		itemsPrinter := func(items []string, last bool) string {
			nbItems := len(items)
			if nbItems < 1 {
				return ""
			}

			// Try to display the first or last 20 items, as long as displaying them doesn't exceed 180 characters...
			// ... except in verbose mode, where we display everything (it might be ugly while it's running, but it's
			// very useful once it's finished).
			displayItems := 20
			lineLength := 180
			if verbose {
				displayItems = math.MaxInt32
				lineLength = math.MaxInt32
			}
			partial := nbItems > displayItems
			var nItems []string
			for displayItems > 0 {
				if last {
					nItems = items[max(0, nbItems-displayItems):]
				} else {
					nItems = items[:min(nbItems, displayItems)]
				}
				if uniseg.GraphemeClusterCount(strings.Join(nItems, " ")) <= lineLength {
					break
				}
				displayItems -= 1
				partial = true
			}

			if last {
				return fmt.Sprintf("%s%s (%s%d)", lo.Ternary(partial, "… ", ""), strings.Join(nItems, " "), emoji("📝"), nbItems)
			}
			return fmt.Sprintf("%s%s (%s%d)", strings.Join(nItems, " "), lo.Ternary(partial, " …", ""), emoji("📝"), nbItems)
		}

		// Job header: <1h ⏱️ / 1-2h 🐢 / 2h+ 🧟
		jobSlowThreshold := lo.Must(time.ParseDuration("1h"))
		jobVerySlowThreshold := lo.Must(time.ParseDuration("2h"))
		// Task duration: 30-60m 🐢 / 1h+ 🧟
		taskSlowThreshold := lo.Must(time.ParseDuration("30m"))
		taskVerySlowThreshold := lo.Must(time.ParseDuration("1h"))
		// Queued task: 2h+ 😴
		taskQueuedThreshold := lo.Must(time.ParseDuration("2h"))

		var lastMsg *proto.JobStatus
		var lastTasks []string
		var displayLines int
		var started bool

		renderTimestamp := func() string {
			msg := lastMsg
			if msg.CompletedAt != nil {
				return emoji("🏁") + fmt.Sprintf("%s", msg.CompletedAt.AsTime().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Second))
			}
			jobRunningFor := time.Since(msg.ScheduledAt.AsTime()).Truncate(time.Second)
			jobRunningForEmoji := lo.Ternary(jobRunningFor >= jobSlowThreshold, lo.Ternary(jobRunningFor >= jobVerySlowThreshold, "🧟", "🐢"), "⏱️")
			return emoji(jobRunningForEmoji) + fmt.Sprintf("%s", jobRunningFor)
		}

		renderStats := func() string {
			msg := lastMsg

			queued := []string{}
			running := []string{}
			aborted := []string{}
			crashed := []string{}
			failures := []string{}
			completed := []string{}

			lastTasks = lo.Map(msg.Tasks, func(t *proto.TaskStatus, _ int) string { return t.Name })
			for _, t := range msg.Tasks {
				label := t.Name

				if t.StartedAt != nil {
					var taskRunningFor time.Duration
					if t.EndedAt != nil {
						taskRunningFor = t.EndedAt.AsTime().Sub(t.StartedAt.AsTime()).Truncate(time.Minute)
					} else {
						taskRunningFor = time.Since(t.StartedAt.AsTime()).Truncate(time.Minute)
					}
					if taskRunningFor >= taskSlowThreshold {
						label += fmt.Sprintf(" (%s%s)", emoji(lo.Ternary(taskRunningFor >= taskVerySlowThreshold, "🧟", "🐢")), taskRunningFor)
					}
				} else {
					taskQueuedFor := time.Since(msg.ScheduledAt.AsTime()).Truncate(time.Minute)
					if taskQueuedFor >= taskQueuedThreshold {
						label += fmt.Sprintf(" (%s%s)", emoji("😴"), taskQueuedFor)
					}
				}

				switch t.Status {
				case proto.TaskStatus_QUEUED:
					queued = append(queued, label)
				case proto.TaskStatus_RUNNING:
					running = append(running, label)
				case proto.TaskStatus_ABORTED:
					aborted = append(aborted, label)
				case proto.TaskStatus_FAILED:
					if *t.ExitCode == 42 {
						failures = append(failures, label)
					} else {
						crashed = append(crashed, label)
					}
				case proto.TaskStatus_COMPLETED:
					completed = append(completed, label)
				}
			}

			statItems := []string{}
			if len(queued) > 0 {
				statItems = append(statItems, emoji("⏳")+itemsPrinter(queued, false))
			}
			if len(running) > 0 {
				statItems = append(statItems, emoji("⚙️")+itemsPrinter(running, false))
			}
			if len(aborted) > 0 {
				statItems = append(statItems, emoji("🛑")+itemsPrinter(aborted, true))
			}
			if len(crashed) > 0 {
				statItems = append(statItems, emoji("💥")+itemsPrinter(crashed, true))
			}
			if len(failures) > 0 {
				statItems = append(statItems, emoji("⚠️")+itemsPrinter(failures, true))
			}
			if len(completed) > 0 {
				statItems = append(statItems, emoji("✅")+itemsPrinter(completed, true))
			}

			return strings.Join(statItems, "\n")
		}

		headerMsg := func(timestamp string) string {
			return fmt.Sprintf("Job '%s' running (%s%d, %s)", args[0], emoji("📝"), len(lastTasks), timestamp)
		}

		render := func() {
			if displayLines > 0 {
				fmt.Fprintf(os.Stderr, "\033[%dA\r\033[J", displayLines)
			}
			timestamp := renderTimestamp()
			stats := renderStats()
			header := headerMsg(timestamp)
			output := header
			if stats != "" {
				output += "\n" + stats
			}
			fmt.Fprint(os.Stderr, output)
			displayLines = strings.Count(output, "\n")
		}

		type recvResult struct {
			msg *proto.JobStatus
			err error
		}
		msgCh := make(chan recvResult, 1)
		go func() {
			for {
				msg, err := c.Recv()
				msgCh <- recvResult{msg, err}
				if err != nil {
					return
				}
			}
		}()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		defer fmt.Fprint(os.Stderr, "\033[?25h")

		for {
			select {
			case result := <-msgCh:
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
						stats := renderStats()
						timestamp := renderTimestamp()
						fmt.Fprintf(os.Stderr, "%s Job '%s' completed (%s%d, %s)\n%s\n", color.HiGreenString("✓"), args[0], emoji("📝"), len(lastTasks), timestamp, stats)
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
				render()

			case <-ticker.C:
				if !started {
					continue
				}
				render()
			}
		}
	},
}
