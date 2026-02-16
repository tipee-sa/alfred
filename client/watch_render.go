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

// formatMinutes formats a minute-truncated duration as "36m" instead of "36m0s".
func formatMinutes(d time.Duration) string {
	return strings.TrimSuffix(d.String(), "0s")
}

// emojiLabel returns the emoji followed by spacing equal to its rune count,
// ensuring consistent alignment regardless of emoji rendering width.
func emojiLabel(emoji string) string {
	return emoji + strings.Repeat(" ", utf8.RuneCountInString(emoji))
}

// safeStringWidth returns the display width of a string with a safety margin for emoji
// characters. Terminals may render wide characters (width >= 2) up to 1 column wider than
// the Unicode standard specifies, so we add 1 extra column per wide grapheme cluster.
func safeStringWidth(s string) int {
	width := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		w := uniseg.StringWidth(g.Str())
		if w >= 2 {
			w++ // terminal may render this 1 column wider than uniseg reports
		}
		width += w
	}
	return width
}

// taskProgressCount returns the number of processed (completed/failed/aborted) tasks
// and the total number of tasks.
func taskProgressCount(tasks []*proto.TaskStatus) (processed, total int) {
	total = len(tasks)
	for _, t := range tasks {
		switch t.Status {
		case proto.TaskStatus_COMPLETED, proto.TaskStatus_FAILED, proto.TaskStatus_ABORTED, proto.TaskStatus_SKIPPED:
			processed++
		}
	}
	return
}

// formatItems formats a list of task names for display, truncating if needed.
// When last is true, the last N items are shown (with "… " prefix); otherwise the first N.
// When verbose is true, all items are shown without truncation.
func formatItems(items []string, last bool, verbose bool, lineLength int) string {
	nbItems := len(items)
	if nbItems < 1 {
		return ""
	}

	// Try to display the first or last 20 items, as long as displaying them doesn't exceed lineLength characters...
	// ... except in verbose mode, where we display everything (it might be ugly while it's running, but it's
	// very useful once it's finished).
	displayItems := 20
	if lineLength <= 0 {
		lineLength = 180
	}
	if verbose {
		displayItems = math.MaxInt32
		lineLength = math.MaxInt32
	}

	// Reserve space for the suffix: ellipsis "… " / " …" (3) + " (" (2) + emoji label + digits + ")" (1)
	suffix := fmt.Sprintf(" (%s%d)", emojiLabel("📝"), nbItems)
	suffixWidth := safeStringWidth(suffix) + 2 // +2 for ellipsis when partial
	availWidth := max(lineLength-suffixWidth, 20)

	partial := nbItems > displayItems
	var nItems []string
	for displayItems > 0 {
		if last {
			nItems = items[max(0, nbItems-displayItems):]
		} else {
			nItems = items[:min(nbItems, displayItems)]
		}
		if safeStringWidth(strings.Join(nItems, " ")) <= availWidth {
			break
		}
		displayItems -= 1
		partial = true
	}

	if last {
		return fmt.Sprintf("%s%s%s", lo.Ternary(partial, "… ", ""), strings.Join(nItems, " "), suffix)
	}
	return fmt.Sprintf("%s%s%s", strings.Join(nItems, " "), lo.Ternary(partial, " …", ""), suffix)
}

// visualLineCount returns how many visual lines a string occupies in the terminal,
// accounting for line wrapping when a logical line exceeds terminal width.
func visualLineCount(s string, termWidth int) int {
	if termWidth <= 0 {
		termWidth = 80
	}
	count := 0
	for _, line := range strings.Split(s, "\n") {
		w := uniseg.StringWidth(line)
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
	skipped := []string{}
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
				label += fmt.Sprintf(" (%s%s)", emojiLabel(lo.Ternary(taskRunningFor >= taskVerySlowThreshold, "🧟", "🐢")), formatMinutes(taskRunningFor))
			}
		} else {
			taskQueuedFor := r.now().Sub(msg.ScheduledAt.AsTime()).Truncate(time.Minute)
			if taskQueuedFor >= taskQueuedThreshold {
				label += fmt.Sprintf(" (%s%s)", emojiLabel("😴"), formatMinutes(taskQueuedFor))
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
		case proto.TaskStatus_SKIPPED:
			skipped = append(skipped, label)
		case proto.TaskStatus_COMPLETED:
			completed = append(completed, label)
		}
	}

	maxLineWidth := r.termWidth() - 1 // -1 to avoid terminal wrapping on exact-width lines
	statItems := []string{}
	addStat := func(emoji string, items []string, last bool) {
		prefix := emojiLabel(emoji)
		availWidth := max(maxLineWidth-safeStringWidth(prefix), 40)
		statItems = append(statItems, prefix+formatItems(items, last, r.verbose, availWidth))
	}
	if len(queued) > 0 {
		addStat("⏳", queued, false)
	}
	if len(running) > 0 {
		addStat("⚙️", running, false)
	}
	if len(aborted) > 0 {
		addStat("⛔", aborted, true)
	}
	if len(skipped) > 0 {
		addStat("⏭️", skipped, true)
	}
	if len(crashed) > 0 {
		addStat("💥", crashed, true)
	}
	if len(failures) > 0 {
		addStat("⚠️", failures, true)
	}
	if len(completed) > 0 {
		addStat("✅", completed, true)
	}

	stats = strings.Join(statItems, "\n")
	return
}

// renderOutput composes the full watch display and returns the output string
// and the number of display lines (for cursor repositioning).
func (r *watchRenderer) renderOutput(msg *proto.JobStatus) (output string, displayLines int) {
	timestamp := r.renderTimestamp(msg)
	_, stats := r.renderStats(msg)
	processed, total := taskProgressCount(msg.Tasks)
	pct := 0
	if total > 0 {
		pct = processed * 100 / total
	}
	header := fmt.Sprintf("Job '%s' running (%s%d/%d %d%%, %s)", r.jobName, emojiLabel("📝"), processed, total, pct, timestamp)
	output = header
	if stats != "" {
		output += "\n" + stats
	}
	displayLines = visualLineCount(output, r.termWidth()) - 1 // -1: cursor is already on the last line
	return
}
