package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// Two readings as a cgroup v2 daemon streams them, a second apart: each with the previous one's
// CPU time, the first with none.
const stepStatsStream = `{"read":"2026-11-06T12:00:01Z","preread":"0001-01-01T00:00:00Z",` +
	`"cpu_stats":{"cpu_usage":{"total_usage":2000000000}},"precpu_stats":{"cpu_usage":{"total_usage":0}},` +
	`"memory_stats":{"usage":900000000,"stats":{"inactive_file":100000000}}}
{"read":"2026-11-06T12:00:02Z","preread":"2026-11-06T12:00:01Z",` +
	`"cpu_stats":{"cpu_usage":{"total_usage":3500000000}},"precpu_stats":{"cpu_usage":{"total_usage":2000000000}},` +
	`"memory_stats":{"usage":700000000,"stats":{"inactive_file":50000000}}}
{"read":"2026-11-06T12:00:03Z","preread":"2026-11-06T12:00:02Z",` +
	`"cpu_stats":{"cpu_usage":{"total_usage":0}},"precpu_stats":{"cpu_usage":{"total_usage":3500000000}},` +
	`"memory_stats":{}}
`

func TestStepUsage(t *testing.T) {
	docker := newMockDocker()
	docker.containerStats = stepStatsStream

	got := map[string]any{}
	attrs := watchStepUsage(context.Background(), docker, "step").logAttrs()
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i].(string)] = attrs[i+1]
	}

	for key, want := range map[string]any{
		// 900 MB less 100 MB of page cache; the stopped container's zeros change nothing
		"memoryPeakBytes": uint64(800000000),
		"cpuSeconds":      3.5,
		// 1.5 s of CPU over the second reading's one second
		"cpuPeakCores": 1.5,
	} {
		if got[key] != want {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got[key], got[key], want, want)
		}
	}
}

func TestStepUsageWithoutReadings(t *testing.T) {
	// A daemon that sends no stats: the step's record goes without usage
	if attrs := watchStepUsage(context.Background(), newMockDocker(), "step").logAttrs(); attrs != nil {
		t.Errorf("usage without readings: %v", attrs)
	}
}

func TestRunContainerLogsStepUsage(t *testing.T) {
	docker := newMockDocker()
	docker.containerStats = stepStatsStream
	task := testTask([]string{"step-image:latest"})
	var logs bytes.Buffer
	task.Log = slog.New(slog.NewJSONHandler(&logs, nil))

	docker.containerWaitCh <- container.WaitResponse{StatusCode: 0}
	if _, err := RunContainer(context.Background(), docker, task, newMockFS(), noopRunConfig(), nil); err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "Step finished" {
			continue
		}
		for _, key := range []string{"step", "exitCode", "durationSeconds", "memoryPeakBytes", "cpuSeconds", "cpuPeakCores"} {
			if _, ok := record[key]; !ok {
				t.Errorf("Step finished has no %s: %s", key, line)
			}
		}
		return
	}
	t.Fatalf("no Step finished record in:\n%s", logs.String())
}

func TestStepUsageOnCgroupV1(t *testing.T) {
	// cgroup v1 has both: Docker subtracts total_inactive_file, which counts descendant cgroups
	docker := newMockDocker()
	docker.containerStats = `{"read":"2026-11-06T12:00:01Z","memory_stats":{"usage":900,"stats":{"inactive_file":100,"total_inactive_file":300}}}`
	attrs := watchStepUsage(context.Background(), docker, "step").logAttrs()
	if len(attrs) < 2 || attrs[0] != "memoryPeakBytes" || attrs[1] != uint64(600) {
		t.Errorf("usage %v, want memoryPeakBytes 600", attrs)
	}
}
