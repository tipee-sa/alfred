package main

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gammadia/alfred/proto"
	"github.com/rivo/uniseg"
	"github.com/samber/lo"
)

// Threshold constants for duration-based emoji labels in the watch display.
var (
	jobSlowThreshold      = 1 * time.Hour
	jobVerySlowThreshold  = 2 * time.Hour
	taskSlowThreshold     = 30 * time.Minute
	taskVerySlowThreshold = 1 * time.Hour
	taskQueuedThreshold   = 2 * time.Hour
)

// watchRenderer holds the rendering state and injectable dependencies for watch display.
type watchRenderer struct {
	jobName   string
	verbose   bool
	termWidth func() int       // injected; tests pass a constant
	now       func() time.Time // injected; tests pass a fixed time
}

// emojiLabel returns the emoji followed by spacing equal to its rune count,
// ensuring consistent alignment regardless of emoji rendering width.
func emojiLabel(emoji string) string {
	return emoji + strings.Repeat(" ", utf8.RuneCountInString(emoji))
}

// formatItems formats a list of task names for display, truncating if needed.
// When last is true, the last N items are shown (with "… " prefix); otherwise the first N.
// When verbose is true, all items are shown without truncation.
func formatItems(items []string, last bool, verbose bool) string {
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
		return fmt.Sprintf("%s%s (%s%d)", lo.Ternary(partial, "… ", ""), strings.Join(nItems, " "), emojiLabel("📝"), nbItems)
	}
	return fmt.Sprintf("%s%s (%s%d)", strings.Join(nItems, " "), lo.Ternary(partial, " …", ""), emojiLabel("📝"), nbItems)
}

// visualLineCount returns how many visual lines a string occupies in the terminal,
// accounting for line wrapping when a logical line exceeds terminal width.
func visualLineCount(s string, termWidth int) int {
	if termWidth <= 0 {
		termWidth = 80
	}
	count := 0
	for _, line := range strings.Split(s, "\n") {
		w := uniseg.GraphemeClusterCount(line)
		if w <= termWidth {
			count++
		} else {
			count += (w + termWidth - 1) / termWidth
		}
	}
	return count
}

// renderTimestamp returns the job duration display string with appropriate emoji.
func (r *watchRenderer) renderTimestamp(msg *proto.JobStatus) string {
	if msg.CompletedAt != nil {
		return emojiLabel("🏁") + fmt.Sprintf("%s", msg.CompletedAt.AsTime().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Second))
	}
	jobRunningFor := r.now().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Second)
	jobRunningForEmoji := lo.Ternary(jobRunningFor >= jobSlowThreshold, lo.Ternary(jobRunningFor >= jobVerySlowThreshold, "🧟", "🐢"), "⏱️")
	return emojiLabel(jobRunningForEmoji) + fmt.Sprintf("%s", jobRunningFor)
}

// renderStats returns the task names list and the formatted stats string with emoji sections.
func (r *watchRenderer) renderStats(msg *proto.JobStatus) (taskNames []string, stats string) {
	queued := []string{}
	running := []string{}
	aborted := []string{}
	crashed := []string{}
	failures := []string{}
	completed := []string{}

	taskNames = lo.Map(msg.Tasks, func(t *proto.TaskStatus, _ int) string { return t.Name })
	for _, t := range msg.Tasks {
		label := t.Name

		if t.StartedAt != nil {
			var taskRunningFor time.Duration
			if t.EndedAt != nil {
				taskRunningFor = t.EndedAt.AsTime().Sub(t.StartedAt.AsTime()).Truncate(time.Minute)
			} else {
				taskRunningFor = r.now().Sub(t.StartedAt.AsTime()).Truncate(time.Minute)
			}
			if taskRunningFor >= taskSlowThreshold {
				label += fmt.Sprintf(" (%s%s)", emojiLabel(lo.Ternary(taskRunningFor >= taskVerySlowThreshold, "🧟", "🐢")), taskRunningFor)
			}
		} else {
			taskQueuedFor := r.now().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Minute)
			if taskQueuedFor >= taskQueuedThreshold {
				label += fmt.Sprintf(" (%s%s)", emojiLabel("😴"), taskQueuedFor)
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
		statItems = append(statItems, emojiLabel("⏳")+formatItems(queued, false, r.verbose))
	}
	if len(running) > 0 {
		statItems = append(statItems, emojiLabel("⚙️")+formatItems(running, false, r.verbose))
	}
	if len(aborted) > 0 {
		statItems = append(statItems, emojiLabel("🛑")+formatItems(aborted, true, r.verbose))
	}
	if len(crashed) > 0 {
		statItems = append(statItems, emojiLabel("💥")+formatItems(crashed, true, r.verbose))
	}
	if len(failures) > 0 {
		statItems = append(statItems, emojiLabel("⚠️")+formatItems(failures, true, r.verbose))
	}
	if len(completed) > 0 {
		statItems = append(statItems, emojiLabel("✅")+formatItems(completed, true, r.verbose))
	}

	stats = strings.Join(statItems, "\n")
	return
}

// renderOutput composes the full watch display and returns the output string
// and the number of display lines (for cursor repositioning).
func (r *watchRenderer) renderOutput(msg *proto.JobStatus) (output string, displayLines int) {
	timestamp := r.renderTimestamp(msg)
	taskNames, stats := r.renderStats(msg)
	header := fmt.Sprintf("Job '%s' running (%s%d, %s)", r.jobName, emojiLabel("📝"), len(taskNames), timestamp)
	output = header
	if stats != "" {
		output += "\n" + stats
	}
	displayLines = visualLineCount(output, r.termWidth()) - 1 // -1: cursor is already on the last line
	return
}
