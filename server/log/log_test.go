package log

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestDurationsInSeconds(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{ReplaceAttr: durationsInSeconds}))
	logger.With(slog.Group("node", "wait", 2*time.Second)).Info("Scheduling task on node", "startDelay", 68999800002*time.Nanosecond, "slots", 2)

	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if got := record["startDelaySeconds"]; got != 69.0 {
		t.Errorf("startDelaySeconds %v, want 69: %s", got, out.String())
	}
	if _, ok := record["startDelay"]; ok {
		t.Errorf("startDelay kept: %s", out.String())
	}
	if got := record["node"].(map[string]any)["waitSeconds"]; got != 2.0 {
		t.Errorf("node.waitSeconds %v, want 2: %s", got, out.String())
	}
	if got := record["slots"]; got != 2.0 {
		t.Errorf("slots %v, want 2: %s", got, out.String())
	}
}
