package openstack

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
)

// Answers each check with the next status, then repeats the last one
func statusSequence(statuses ...serverStatus) (func() (serverStatus, error), *int) {
	calls := 0
	return func() (serverStatus, error) {
		i := min(calls, len(statuses)-1)
		calls++
		return statuses[i], nil
	}, &calls
}

func TestWaitForActiveReturnsOnceActive(t *testing.T) {
	get, calls := statusSequence(
		serverStatus{Status: "BUILD", TaskState: "scheduling"},
		serverStatus{Status: "BUILD", TaskState: "spawning"},
		serverStatus{Status: "ACTIVE"},
	)
	if _, err := waitForActive(context.Background(), get, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Errorf("checked %d times, want 3", *calls)
	}
}

func TestWaitForActiveGivesUpAtOnceOnError(t *testing.T) {
	status := serverStatus{Status: "ERROR", VmState: "error"}
	status.Fault.Code = 500
	status.Fault.Message = "No valid host was found."
	get, calls := statusSequence(serverStatus{Status: "BUILD"}, status)

	_, err := waitForActive(context.Background(), get, time.Minute, time.Millisecond)
	if err == nil {
		t.Fatal("no error for a server in ERROR")
	}
	if *calls != 2 {
		t.Errorf("checked %d times, want 2", *calls)
	}
	for _, want := range []string{"ERROR", "vm state error", `fault 500 "No valid host was found."`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

func TestWaitForActiveSaysWhereTheServerStoodAtTheTimeout(t *testing.T) {
	get, _ := statusSequence(serverStatus{Status: "BUILD", TaskState: "block_device_mapping", VmState: "building"})

	_, err := waitForActive(context.Background(), get, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("no error at the timeout")
	}
	want := "status BUILD, task state block_device_mapping, vm state building"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not say %q", err, want)
	}
}

func TestWaitForActiveRetriesAFailedCheck(t *testing.T) {
	calls := 0
	get := func() (serverStatus, error) {
		calls++
		if calls == 1 {
			return serverStatus{}, errors.New("503 Service Unavailable")
		}
		return serverStatus{Status: "ACTIVE"}, nil
	}
	if _, err := waitForActive(context.Background(), get, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForActiveReportsAFailingCheckAtTheTimeout(t *testing.T) {
	cause := errors.New("503 Service Unavailable")
	get := func() (serverStatus, error) { return serverStatus{}, cause }

	_, err := waitForActive(context.Background(), get, 20*time.Millisecond, time.Millisecond)
	if !errors.Is(err, cause) {
		t.Errorf("error %q does not wrap the failed check's", err)
	}
}

func TestWaitForActiveStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	get, _ := statusSequence(serverStatus{Status: "BUILD"})

	_, err := waitForActive(ctx, get, time.Minute, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
}

// What Nova answers for a server that failed to build (GET /servers/{id}), trimmed
func TestServerStatusDecodesNovasFields(t *testing.T) {
	body := `{"server": {
		"status": "ERROR",
		"OS-EXT-STS:task_state": null,
		"OS-EXT-STS:vm_state": "error",
		"OS-EXT-STS:power_state": 0,
		"fault": {"code": 500, "created": "2026-11-11T13:04:12Z", "message": "No valid host was found. ", "details": "Traceback ..."}
	}}`
	var result servers.GetResult
	if err := json.Unmarshal([]byte(body), &result.Body); err != nil {
		t.Fatal(err)
	}
	var s serverStatus
	if err := result.ExtractInto(&s); err != nil {
		t.Fatal(err)
	}
	want := `status ERROR, vm state error, fault 500 "No valid host was found. "`
	if s.String() != want {
		t.Errorf("got %q, want %q", s.String(), want)
	}
}
