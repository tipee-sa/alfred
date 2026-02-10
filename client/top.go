package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gammadia/alfred/proto"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
)

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Show the status of the server",
	Args:  cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		// Fetch server version info
		ping, err := client.Ping(cmd.Context(), &proto.PingRequest{})
		if err != nil {
			return fmt.Errorf("failed to ping server: %w", err)
		}

		// Open status stream
		stream, err := client.WatchStatus(cmd.Context(), &proto.WatchStatusRequest{})
		if err != nil {
			return fmt.Errorf("failed to watch status: %w", err)
		}

		app := tview.NewApplication()

		// Header
		header := tview.NewTextView().
			SetDynamicColors(true).
			SetWordWrap(true).
			SetTextAlign(tview.AlignLeft)
		header.SetBorder(true).SetTitle(" Alfred ")

		// Nodes table
		nodesTable := tview.NewTable().
			SetFixed(1, 0).
			SetSelectable(true, false)
		nodesTable.SetBorder(true).SetTitle(" Nodes ")

		// Jobs table
		jobsTable := tview.NewTable().
			SetFixed(1, 0).
			SetSelectable(true, false)
		jobsTable.SetBorder(true).SetTitle(" Jobs ")

		// Layout
		layout := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(header, 5, 0, false).
			AddItem(nodesTable, 0, 1, false).
			AddItem(jobsTable, 0, 1, false)

		// Focus cycling: Tab switches between nodes and jobs tables
		focusables := []tview.Primitive{nodesTable, jobsTable}
		focusIndex := 0
		app.SetFocus(nodesTable)

		// Input handling
		app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Rune() == 'q' {
				app.Stop()
				return nil
			}
			if event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab {
				if event.Key() == tcell.KeyBacktab {
					focusIndex = (focusIndex + len(focusables) - 1) % len(focusables)
				} else {
					focusIndex = (focusIndex + 1) % len(focusables)
				}
				app.SetFocus(focusables[focusIndex])
				return nil
			}
			return event
		})

		// State for rendering — only accessed from tview's event loop (via QueueUpdateDraw)
		var lastStatus *proto.Status

		updateHeader := func() {
			if lastStatus == nil {
				return
			}
			header.Clear()

			uptime := ""
			if lastStatus.Server != nil && lastStatus.Server.StartedAt != nil {
				d := time.Since(lastStatus.Server.StartedAt.AsTime())
				hours := int(d.Hours())
				minutes := int(d.Minutes()) % 60
				seconds := int(d.Seconds()) % 60
				uptime = fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
			}

			provisioner := ""
			maxNodes := uint32(0)
			logLevel := ""
			provisioningDelay := ""
			if lastStatus.Scheduler != nil {
				provisioner = lastStatus.Scheduler.Provisioner
				maxNodes = lastStatus.Scheduler.MaxNodes
				logLevel = lastStatus.Scheduler.LogLevel
				if lastStatus.Scheduler.ProvisioningDelay != nil {
					provisioningDelay = lastStatus.Scheduler.ProvisioningDelay.AsDuration().String()
				}
			}

			fmt.Fprintf(header, " [yellow]Alfred[white] %s (%s)  |  Uptime: [green]%s[white]\n",
				ping.Version, ping.Commit, uptime)
			fmt.Fprintf(header, " Provisioner: [yellow]%s[white]  |  Max Nodes: [yellow]%d[white]  |  Log Level: [yellow]%s[white]  |  Provisioning Delay: [yellow]%s[white]",
				provisioner, maxNodes, logLevel, provisioningDelay)
		}

		updateNodes := func() {
			nodesTable.Clear()
			nodesTable.ScrollToBeginning()

			maxNodes := uint32(0)
			if lastStatus != nil && lastStatus.Scheduler != nil {
				maxNodes = lastStatus.Scheduler.MaxNodes
			}
			nodeCount := 0
			if lastStatus != nil {
				nodeCount = len(lastStatus.Nodes)
			}
			nodesTable.SetTitle(fmt.Sprintf(" Nodes: %d/%d ", nodeCount, maxNodes))

			// Header row
			for col, title := range []string{"NAME", "STATUS", "SLOTS", "RUNNING"} {
				nodesTable.SetCell(0, col, tview.NewTableCell(title).
					SetTextColor(tcell.ColorYellow).
					SetSelectable(false).
					SetExpansion(1))
			}

			if lastStatus == nil {
				return
			}

			nodes := make([]*proto.NodeStatus, len(lastStatus.Nodes))
			copy(nodes, lastStatus.Nodes)
			sort.Slice(nodes, func(i, j int) bool {
				oi, oj := nodeStatusOrder(nodes[i].Status), nodeStatusOrder(nodes[j].Status)
				if oi != oj {
					return oi < oj
				}
				return nodes[i].Name < nodes[j].Name
			})

			for row, node := range nodes {
				// Name
				nodesTable.SetCell(row+1, 0, tview.NewTableCell(node.Name).
					SetTextColor(tcell.ColorWhite).
					SetExpansion(1))

				// Status with color
				statusColor := nodeStatusColor(node.Status)
				nodesTable.SetCell(row+1, 1, tview.NewTableCell(node.Status.String()).
					SetTextColor(statusColor).
					SetExpansion(1))

				// Slots occupancy
				busy := 0
				running := ""
				for _, slot := range node.Slots {
					if slot != nil && slot.Task != nil {
						busy++
						if running != "" {
							running += " "
						}
						running += slot.Task.Name
					}
				}
				nodesTable.SetCell(row+1, 2, tview.NewTableCell(fmt.Sprintf("%d/%d", busy, len(node.Slots))).
					SetTextColor(tcell.ColorWhite).
					SetExpansion(0))

				// Running task names
				nodesTable.SetCell(row+1, 3, tview.NewTableCell(running).
					SetTextColor(tcell.ColorYellow).
					SetExpansion(3))
			}
		}

		updateJobs := func() {
			jobsTable.Clear()
			jobsTable.ScrollToBeginning()

			jobCount := 0
			if lastStatus != nil {
				jobCount = len(lastStatus.Jobs)
			}
			jobsTable.SetTitle(fmt.Sprintf(" Jobs (%d) ", jobCount))

			// Header row
			for col, title := range []string{"NAME", "STARTED BY", "SCHEDULED", "ELAPSED", "TASKS", "PROGRESS"} {
				jobsTable.SetCell(0, col, tview.NewTableCell(title).
					SetTextColor(tcell.ColorYellow).
					SetSelectable(false).
					SetExpansion(1))
			}

			if lastStatus == nil {
				return
			}

			// Sort jobs: incomplete first, then by scheduled time (newest first)
			jobs := make([]*proto.JobStatus, len(lastStatus.Jobs))
			copy(jobs, lastStatus.Jobs)
			sort.Slice(jobs, func(i, j int) bool {
				iDone := jobs[i].CompletedAt != nil
				jDone := jobs[j].CompletedAt != nil
				if iDone != jDone {
					return !iDone // incomplete jobs first
				}
				if jobs[i].ScheduledAt != nil && jobs[j].ScheduledAt != nil {
					return jobs[i].ScheduledAt.AsTime().After(jobs[j].ScheduledAt.AsTime())
				}
				return false
			})

			// Limit to 50 jobs
			if len(jobs) > 50 {
				jobs = jobs[:50]
			}

			now := time.Now()
			for row, job := range jobs {
				// Name
				nameColor := tcell.ColorAqua
				if job.CompletedAt != nil {
					nameColor = tcell.ColorGray
				}
				jobsTable.SetCell(row+1, 0, tview.NewTableCell(job.Name).
					SetTextColor(nameColor).
					SetExpansion(1))

				// Started by
				jobsTable.SetCell(row+1, 1, tview.NewTableCell(job.StartedBy).
					SetTextColor(tcell.ColorWhite).
					SetExpansion(1))

				// Scheduled time: show time only for today, date+time otherwise
				scheduled := ""
				if job.ScheduledAt != nil {
					t := job.ScheduledAt.AsTime().Local()
					if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
						scheduled = t.Format("15:04:05")
					} else {
						scheduled = t.Format("02 Jan 15:04")
					}
				}
				jobsTable.SetCell(row+1, 2, tview.NewTableCell(scheduled).
					SetTextColor(tcell.ColorWhite).
					SetExpansion(1))

				// Elapsed
				elapsed := ""
				if job.ScheduledAt != nil {
					var d time.Duration
					if job.CompletedAt != nil {
						d = job.CompletedAt.AsTime().Sub(job.ScheduledAt.AsTime())
					} else {
						d = now.Sub(job.ScheduledAt.AsTime())
					}
					elapsed = formatDuration(d)
				}
				elapsedColor := tcell.ColorWhite
				if job.CompletedAt != nil {
					elapsedColor = tcell.ColorGray
				}
				jobsTable.SetCell(row+1, 3, tview.NewTableCell(elapsed).
					SetTextColor(elapsedColor).
					SetExpansion(1))

				// Task count
				jobsTable.SetCell(row+1, 4, tview.NewTableCell(fmt.Sprintf("%d", len(job.Tasks))).
					SetTextColor(tcell.ColorWhite).
					SetExpansion(1))

				// Progress
				progress := taskProgress(job.Tasks)
				jobsTable.SetCell(row+1, 5, tview.NewTableCell(progress).
					SetExpansion(2))
			}
		}

		updateAll := func() {
			updateHeader()
			updateNodes()
			updateJobs()
		}

		// Recv goroutine: decouples blocking Recv() from tview event loop
		type statusResult struct {
			status *proto.Status
			err    error
		}
		statusCh := make(chan statusResult, 1)
		go func() {
			for {
				msg, err := stream.Recv()
				statusCh <- statusResult{msg, err}
				if err != nil {
					return
				}
			}
		}()

		// done is closed when the app stops, to signal goroutines to exit.
		done := make(chan struct{})

		// Goroutine to feed status updates into tview's event loop
		go func() {
			for result := range statusCh {
				if result.err != nil {
					app.Stop()
					return
				}
				status := result.status
				app.QueueUpdateDraw(func() {
					lastStatus = status
					updateAll()
				})
			}
		}()

		// 1-second ticker to refresh uptime and elapsed times
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					app.QueueUpdateDraw(func() {
						if lastStatus != nil {
							updateHeader()
							updateJobs()
						}
					})
				}
			}
		}()

		err = app.SetRoot(layout, true).Run()
		close(done)
		return err
	},
}

func nodeStatusOrder(status proto.NodeStatus_Status) int {
	switch status {
	case proto.NodeStatus_ONLINE:
		return 0
	case proto.NodeStatus_TERMINATING, proto.NodeStatus_FAILED_TERMINATING:
		return 1
	case proto.NodeStatus_PROVISIONING:
		return 2
	case proto.NodeStatus_QUEUED:
		return 3
	case proto.NodeStatus_TERMINATED, proto.NodeStatus_DISCARDED, proto.NodeStatus_FAILED_PROVISIONING:
		return 4
	default:
		return 5
	}
}

func nodeStatusColor(status proto.NodeStatus_Status) tcell.Color {
	switch status {
	case proto.NodeStatus_ONLINE:
		return tcell.ColorGreen
	case proto.NodeStatus_QUEUED, proto.NodeStatus_PROVISIONING:
		return tcell.ColorYellow
	case proto.NodeStatus_TERMINATING, proto.NodeStatus_TERMINATED:
		return tcell.ColorGray
	case proto.NodeStatus_FAILED_PROVISIONING, proto.NodeStatus_FAILED_TERMINATING, proto.NodeStatus_DISCARDED:
		return tcell.ColorRed
	default:
		return tcell.ColorWhite
	}
}

func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

func taskProgress(tasks []*proto.TaskStatus) string {
	var queued, running, completed, failed, aborted int
	for _, t := range tasks {
		switch t.Status {
		case proto.TaskStatus_QUEUED:
			queued++
		case proto.TaskStatus_RUNNING:
			running++
		case proto.TaskStatus_COMPLETED:
			completed++
		case proto.TaskStatus_FAILED:
			failed++
		case proto.TaskStatus_ABORTED:
			aborted++
		}
	}

	parts := []string{}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("[yellow]%d run[-]", running))
	}
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("[green]%d ok[-]", completed))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("[red]%d fail[-]", failed))
	}
	if aborted > 0 {
		parts = append(parts, fmt.Sprintf("[gray]%d abort[-]", aborted))
	}
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("[white]%d queue[-]", queued))
	}

	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}
