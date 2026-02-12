package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gammadia/alfred/proto"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- visualLineCount tests ---

func TestVisualLineCount_ShortLine(t *testing.T) {
	assert.Equal(t, 1, visualLineCount("hello", 80))
}

func TestVisualLineCount_ExactWidth(t *testing.T) {
	s := strings.Repeat("a", 80)
	assert.Equal(t, 1, visualLineCount(s, 80))
}

func TestVisualLineCount_OneCharOver(t *testing.T) {
	s := strings.Repeat("a", 81)
	assert.Equal(t, 2, visualLineCount(s, 80))
}

func TestVisualLineCount_WrapsToThreeLines(t *testing.T) {
	s := strings.Repeat("a", 200)
	assert.Equal(t, 3, visualLineCount(s, 80))
}

func TestVisualLineCount_MultiLineWithWrapping(t *testing.T) {
	// First line: 100 chars (wraps to 2 visual lines at width 80)
	// Second line: 10 chars (1 visual line)
	s := strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 10)
	assert.Equal(t, 3, visualLineCount(s, 80))
}

func TestVisualLineCount_EmptyString(t *testing.T) {
	// Empty string splits into one empty line
	assert.Equal(t, 1, visualLineCount("", 80))
}

func TestVisualLineCount_ZeroWidthFallback(t *testing.T) {
	// termWidth <= 0 should fall back to 80
	s := strings.Repeat("a", 81)
	assert.Equal(t, 2, visualLineCount(s, 0))
	assert.Equal(t, 2, visualLineCount(s, -1))
}

func TestVisualLineCount_EmojisAtBoundary(t *testing.T) {
	// Each emoji is 2 display columns wide. 40 emojis = 80 columns = exactly fits.
	s := strings.Repeat("🔥", 40)
	assert.Equal(t, 1, visualLineCount(s, 80))

	// 41 emojis = 82 columns → wraps to 2 lines
	s = strings.Repeat("🔥", 41)
	assert.Equal(t, 2, visualLineCount(s, 80))
}

func TestVisualLineCount_MultiCodepointEmoji(t *testing.T) {
	// Family emoji (multiple codepoints, 1 grapheme cluster, 2 display columns)
	family := "👨‍👩‍👧‍👦"
	s := strings.Repeat(family, 40)
	assert.Equal(t, 1, visualLineCount(s, 80))

	s = strings.Repeat(family, 41)
	assert.Equal(t, 2, visualLineCount(s, 80))
}

// --- formatMinutes tests ---

func TestFormatMinutes(t *testing.T) {
	assert.Equal(t, "30m", formatMinutes(30*time.Minute))
	assert.Equal(t, "1h0m", formatMinutes(1*time.Hour))
	assert.Equal(t, "1h30m", formatMinutes(90*time.Minute))
	assert.Equal(t, "2h0m", formatMinutes(2*time.Hour))
}

// --- emojiLabel tests ---

func TestEmojiLabel_SingleCodepoint(t *testing.T) {
	// "✅" is 1 rune → 1 space after
	result := emojiLabel("✅")
	assert.Equal(t, "✅ ", result)
}

func TestEmojiLabel_MultiRuneEmoji(t *testing.T) {
	// "⚙️" is 2 runes (⚙ + VS16) → 2 spaces after
	result := emojiLabel("⚙️")
	assert.Equal(t, "⚙️  ", result)
}

func TestEmojiLabel_FlagEmoji(t *testing.T) {
	// "🏁" is 1 rune → 1 space
	result := emojiLabel("🏁")
	assert.Equal(t, "🏁 ", result)
}

// --- taskProgressCount tests ---

func TestTaskProgressCount(t *testing.T) {
	tasks := []*proto.TaskStatus{
		{Status: proto.TaskStatus_QUEUED},
		{Status: proto.TaskStatus_RUNNING},
		{Status: proto.TaskStatus_COMPLETED},
		{Status: proto.TaskStatus_FAILED},
		{Status: proto.TaskStatus_ABORTED},
	}
	processed, total := taskProgressCount(tasks)
	assert.Equal(t, 3, processed)
	assert.Equal(t, 5, total)
}

func TestTaskProgressCount_Empty(t *testing.T) {
	processed, total := taskProgressCount(nil)
	assert.Equal(t, 0, processed)
	assert.Equal(t, 0, total)
}

// --- formatItems tests ---

func TestFormatItems_Empty(t *testing.T) {
	assert.Equal(t, "", formatItems([]string{}, false, false, 180))
	assert.Equal(t, "", formatItems(nil, false, false, 180))
}

func TestFormatItems_FewItems_First(t *testing.T) {
	items := []string{"task-a", "task-b", "task-c"}
	result := formatItems(items, false, false, 180)
	assert.Contains(t, result, "task-a task-b task-c")
	assert.NotContains(t, result, "…")
	assert.Contains(t, result, "3")
}

func TestFormatItems_FewItems_Last(t *testing.T) {
	items := []string{"task-a", "task-b", "task-c"}
	result := formatItems(items, true, false, 180)
	assert.Contains(t, result, "task-a task-b task-c")
	assert.NotContains(t, result, "…")
	assert.Contains(t, result, "3")
}

func TestFormatItems_ManyItems_First(t *testing.T) {
	items := make([]string, 25)
	for i := range items {
		items[i] = "task"
	}
	result := formatItems(items, false, false, 180)
	assert.Contains(t, result, " …")
	assert.Contains(t, result, "25")
}

func TestFormatItems_ManyItems_Last(t *testing.T) {
	items := make([]string, 25)
	for i := range items {
		items[i] = "task"
	}
	result := formatItems(items, true, false, 180)
	assert.Contains(t, result, "… ")
	assert.Contains(t, result, "25")
}

func TestFormatItems_Verbose_ShowsAll(t *testing.T) {
	items := make([]string, 25)
	for i := range items {
		items[i] = "task"
	}
	result := formatItems(items, false, true, 180)
	assert.NotContains(t, result, "…")
	assert.Equal(t, 25, strings.Count(result, "task"))
}

func TestFormatItems_LongItemsTruncated(t *testing.T) {
	// Items whose combined width exceeds 180 chars should be truncated further
	items := make([]string, 15)
	for i := range items {
		items[i] = strings.Repeat("x", 15) // 15*15 = 225 chars + spaces > 180
	}
	result := formatItems(items, false, false, 180)
	// Should have ellipsis since items are truncated
	assert.Contains(t, result, "…")
}

// --- renderTimestamp tests ---

func TestRenderTimestamp_Completed(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now: func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		CompletedAt: timestamppb.New(now),
	}
	result := r.renderTimestamp(msg)
	assert.Contains(t, result, "🏁")
	assert.Contains(t, result, "5m0s")
}

func TestRenderTimestamp_RunningShort(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now: func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-10 * time.Minute)),
	}
	result := r.renderTimestamp(msg)
	assert.Contains(t, result, "⏱️")
	assert.Contains(t, result, "10m0s")
}

func TestRenderTimestamp_RunningSlow(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now: func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-90 * time.Minute)),
	}
	result := r.renderTimestamp(msg)
	assert.Contains(t, result, "🐢")
}

func TestRenderTimestamp_RunningVerySlow(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now: func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-150 * time.Minute)),
	}
	result := r.renderTimestamp(msg)
	assert.Contains(t, result, "🧟")
}

// --- renderStats tests ---

func TestRenderStats_ExitCode42_IsFailure(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	exitCode42 := int32(42)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "test-task", Status: proto.TaskStatus_FAILED, ExitCode: &exitCode42, StartedAt: timestamppb.New(now.Add(-3 * time.Minute))},
		},
	}
	_, stats := r.renderStats(msg)
	assert.Contains(t, stats, "⚠️")
	assert.NotContains(t, stats, "💥")
}

func TestRenderStats_OtherExitCode_IsCrashed(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	exitCode1 := int32(1)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "test-task", Status: proto.TaskStatus_FAILED, ExitCode: &exitCode1, StartedAt: timestamppb.New(now.Add(-3 * time.Minute))},
		},
	}
	_, stats := r.renderStats(msg)
	assert.Contains(t, stats, "💥")
	assert.NotContains(t, stats, "⚠️")
}

func TestRenderStats_SectionOrdering(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	exitCode42 := int32(42)
	exitCode1 := int32(1)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "queued-task", Status: proto.TaskStatus_QUEUED},
			{Name: "running-task", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-1 * time.Minute))},
			{Name: "aborted-task", Status: proto.TaskStatus_ABORTED, StartedAt: timestamppb.New(now.Add(-2 * time.Minute))},
			{Name: "crashed-task", Status: proto.TaskStatus_FAILED, ExitCode: &exitCode1, StartedAt: timestamppb.New(now.Add(-2 * time.Minute))},
			{Name: "failed-task", Status: proto.TaskStatus_FAILED, ExitCode: &exitCode42, StartedAt: timestamppb.New(now.Add(-2 * time.Minute))},
			{Name: "done-task", Status: proto.TaskStatus_COMPLETED, StartedAt: timestamppb.New(now.Add(-3 * time.Minute))},
		},
	}
	_, stats := r.renderStats(msg)

	// Verify ordering: ⏳ ⚙️ ⛔ 💥 ⚠️ ✅
	lines := strings.Split(stats, "\n")
	assert.Len(t, lines, 6)
	assert.Contains(t, lines[0], "⏳")
	assert.Contains(t, lines[1], "⚙️")
	assert.Contains(t, lines[2], "⛔")
	assert.Contains(t, lines[3], "💥")
	assert.Contains(t, lines[4], "⚠️")
	assert.Contains(t, lines[5], "✅")
}

func TestRenderStats_EmptyTasks(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now),
		Tasks:       []*proto.TaskStatus{},
	}
	taskNames, stats := r.renderStats(msg)
	assert.Empty(t, taskNames)
	assert.Equal(t, "", stats)
}

func TestRenderStats_OnlyRunning(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "task-a", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-1 * time.Minute))},
			{Name: "task-b", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-2 * time.Minute))},
		},
	}
	taskNames, stats := r.renderStats(msg)
	assert.Equal(t, []string{"task-a", "task-b"}, taskNames)
	assert.Contains(t, stats, "⚙️")
	assert.NotContains(t, stats, "⏳")
	assert.NotContains(t, stats, "✅")
	// Should be a single line (only running section)
	assert.NotContains(t, stats, "\n")
}

func TestRenderStats_TaskNames(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now),
		Tasks: []*proto.TaskStatus{
			{Name: "alpha", Status: proto.TaskStatus_QUEUED},
			{Name: "beta", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now)},
			{Name: "gamma", Status: proto.TaskStatus_COMPLETED, StartedAt: timestamppb.New(now)},
		},
	}
	taskNames, _ := r.renderStats(msg)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, taskNames)
}

func TestRenderStats_SlowRunningTask(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-45 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "slow-task", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-45 * time.Minute))},
		},
	}
	_, stats := r.renderStats(msg)
	assert.Contains(t, stats, "🐢")
	assert.Contains(t, stats, "45m")
	assert.NotContains(t, stats, "45m0s")
}

func TestRenderStats_VerySlowRunningTask(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-90 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "zombie-task", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-90 * time.Minute))},
		},
	}
	_, stats := r.renderStats(msg)
	assert.Contains(t, stats, "🧟")
}

func TestRenderStats_LongQueuedTask(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		now:       func() time.Time { return now },
		verbose:   false,
		termWidth: func() int { return 300 },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-3 * time.Hour)),
		Tasks: []*proto.TaskStatus{
			{Name: "stuck-task", Status: proto.TaskStatus_QUEUED},
		},
	}
	_, stats := r.renderStats(msg)
	assert.Contains(t, stats, "😴")
}

// --- renderOutput tests ---

func TestRenderOutput_MixedStatuses(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	exitCode1 := int32(1)
	r := &watchRenderer{
		jobName:   "test-job",
		verbose:   false,
		termWidth: func() int { return 300 },
		now:       func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-5 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "queued", Status: proto.TaskStatus_QUEUED},
			{Name: "running", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-1 * time.Minute))},
			{Name: "done", Status: proto.TaskStatus_COMPLETED, StartedAt: timestamppb.New(now.Add(-3 * time.Minute))},
			{Name: "crashed", Status: proto.TaskStatus_FAILED, ExitCode: &exitCode1, StartedAt: timestamppb.New(now.Add(-2 * time.Minute))},
		},
	}
	output, displayLines := r.renderOutput(msg)
	assert.Contains(t, output, "Job 'test-job' running")
	assert.Contains(t, output, "⏳")
	assert.Contains(t, output, "⚙️")
	assert.Contains(t, output, "✅")
	assert.Contains(t, output, "💥")
	// 1 header + 4 stat lines - 1 = 4 displayLines (at wide terminal, no wrapping)
	assert.Equal(t, 4, displayLines)
}

func TestRenderOutput_OnlyRunning(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		jobName:   "my-job",
		verbose:   false,
		termWidth: func() int { return 300 },
		now:       func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-1 * time.Minute)),
		Tasks: []*proto.TaskStatus{
			{Name: "task-1", Status: proto.TaskStatus_RUNNING, StartedAt: timestamppb.New(now.Add(-30 * time.Second))},
		},
	}
	output, displayLines := r.renderOutput(msg)
	assert.Contains(t, output, "⚙️")
	// 1 header + 1 stat line - 1 = 1
	assert.Equal(t, 1, displayLines)
}

func TestRenderOutput_CompletedJob(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &watchRenderer{
		jobName:   "done-job",
		verbose:   false,
		termWidth: func() int { return 300 },
		now:       func() time.Time { return now },
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-10 * time.Minute)),
		CompletedAt: timestamppb.New(now),
		Tasks: []*proto.TaskStatus{
			{Name: "task-1", Status: proto.TaskStatus_COMPLETED, StartedAt: timestamppb.New(now.Add(-10 * time.Minute))},
		},
	}
	output, _ := r.renderOutput(msg)
	assert.Contains(t, output, "🏁")
	assert.Contains(t, output, "10m0s")
}

func TestRenderOutput_TruncationPreventsWrapping(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	// Create enough running tasks to produce a long ⚙️ line
	tasks := make([]*proto.TaskStatus, 15)
	for i := range tasks {
		tasks[i] = &proto.TaskStatus{
			Name:      strings.Repeat("x", 10),
			Status:    proto.TaskStatus_RUNNING,
			StartedAt: timestamppb.New(now.Add(-1 * time.Minute)),
		}
	}

	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-1 * time.Minute)),
		Tasks:       tasks,
	}

	rNarrow := &watchRenderer{
		jobName:   "wrap-test",
		verbose:   false,
		termWidth: func() int { return 80 },
		now:       func() time.Time { return now },
	}
	rWide := &watchRenderer{
		jobName:   "wrap-test",
		verbose:   false,
		termWidth: func() int { return 300 },
		now:       func() time.Time { return now },
	}

	_, linesNarrow := rNarrow.renderOutput(msg)
	_, linesWide := rWide.renderOutput(msg)
	assert.Equal(t, linesNarrow, linesWide, "width-aware truncation should prevent wrapping")
}

func TestSafeStringWidth(t *testing.T) {
	// Pure ASCII: same as uniseg
	assert.Equal(t, 5, safeStringWidth("hello"))
	// Single emoji (width 2 per uniseg) → 3 with safety margin
	assert.Equal(t, 3, safeStringWidth("📝"))
	// Emoji label: emoji(3) + space(1) = 4
	assert.Equal(t, 4, safeStringWidth(emojiLabel("📝")))
	// Mixed: "⏳ hello" = emoji(3) + space(1) + 5 = 9
	assert.Equal(t, 9, safeStringWidth("⏳ hello"))
	// Multiple emojis: each gets +1
	assert.Equal(t, 6, safeStringWidth("📝📝"))
	// Ellipsis is narrow (width 1), no extra margin
	assert.Equal(t, 1, safeStringWidth("…"))
}

func TestRenderStats_LineWidthWithEmojiMargin(t *testing.T) {
	// Reproduces the real-world scenario: many queued tasks where the stat line
	// is close to terminal width. safeStringWidth accounts for terminals rendering
	// emojis wider than uniseg.StringWidth reports.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	taskNames := []string{
		"activsante", "acver", "acvf", "adamautomation", "adc", "adcflowers",
		"admimmobilier", "adn", "adnv", "aigc", "aiglon", "aignep", "ailleurs",
		"aisge", "aismle", "aists", "aja-aigle", "akadoc", "aletsch", "alfa",
	}
	tasks := make([]*proto.TaskStatus, len(taskNames))
	for i, name := range taskNames {
		tasks[i] = &proto.TaskStatus{Name: name, Status: proto.TaskStatus_QUEUED}
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now),
		Tasks:       tasks,
	}

	for _, termWidth := range []int{80, 120, 140, 147, 148, 150, 160, 200} {
		t.Run(fmt.Sprintf("termWidth=%d", termWidth), func(t *testing.T) {
			r := &watchRenderer{
				jobName:   "test-job",
				verbose:   false,
				termWidth: func() int { return termWidth },
				now:       func() time.Time { return now },
			}
			_, stats := r.renderStats(msg)
			for i, line := range strings.Split(stats, "\n") {
				lineWidth := safeStringWidth(line)
				assert.LessOrEqual(t, lineWidth, termWidth-1,
					"line %d exceeds safe width at termWidth=%d: %q (safeWidth=%d)", i, termWidth, line, lineWidth)
			}
		})
	}
}

func TestRenderStats_LineWidthWithSlowTaskEmojis(t *testing.T) {
	// Worst case: many running tasks with slow-task emoji decorations.
	// Each task label gets a 🐢 emoji, so the line has many emojis.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	tasks := make([]*proto.TaskStatus, 20)
	for i := range tasks {
		tasks[i] = &proto.TaskStatus{
			Name:      fmt.Sprintf("task-%d", i),
			Status:    proto.TaskStatus_RUNNING,
			StartedAt: timestamppb.New(now.Add(-45 * time.Minute)), // triggers 🐢 label
		}
	}
	msg := &proto.JobStatus{
		ScheduledAt: timestamppb.New(now.Add(-45 * time.Minute)),
		Tasks:       tasks,
	}

	for _, termWidth := range []int{80, 120, 200, 300} {
		t.Run(fmt.Sprintf("termWidth=%d", termWidth), func(t *testing.T) {
			r := &watchRenderer{
				jobName:   "test-job",
				verbose:   false,
				termWidth: func() int { return termWidth },
				now:       func() time.Time { return now },
			}
			_, stats := r.renderStats(msg)
			for i, line := range strings.Split(stats, "\n") {
				lineWidth := safeStringWidth(line)
				assert.LessOrEqual(t, lineWidth, termWidth-1,
					"line %d exceeds safe width at termWidth=%d: %q (safeWidth=%d)", i, termWidth, line, lineWidth)
			}
		})
	}
}
