package openstack

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
)

const (
	// A node that comes online does so in 60 s at the median, 137 s at most, SSH and Docker
	// included (issue #60): its server is ACTIVE well before that
	serverActiveTimeout = 2 * time.Minute
	// Each check is a call to Nova's API
	serverPollInterval = 2 * time.Second
)

// The fields of a Nova server that say where its creation stands
type serverStatus struct {
	// BUILD while it is created, then ACTIVE, or ERROR
	Status string `json:"status"`
	// The step of the creation, such as scheduling, networking, block_device_mapping or spawning
	TaskState string `json:"OS-EXT-STS:task_state"`
	VmState   string `json:"OS-EXT-STS:vm_state"`
	// Set when the server is in ERROR: why its creation failed
	Fault struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"fault"`
}

func (s serverStatus) String() string {
	parts := []string{fmt.Sprintf("status %s", orNone(s.Status))}
	if s.TaskState != "" {
		parts = append(parts, fmt.Sprintf("task state %s", s.TaskState))
	}
	if s.VmState != "" {
		parts = append(parts, fmt.Sprintf("vm state %s", s.VmState))
	}
	if s.Fault.Message != "" {
		parts = append(parts, fmt.Sprintf("fault %d %q", s.Fault.Code, s.Fault.Message))
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func getServerStatus(client *gophercloud.ServiceClient, id string) (serverStatus, error) {
	var s serverStatus
	err := servers.Get(client, id).ExtractInto(&s)
	return s, err
}

// waitForActive waits for a new server to become ACTIVE. Unlike gophercloud's WaitForStatus, it
// gives up at once on a server in ERROR, says where the server stood when it gives up (issue #60:
// a timeout alone does not tell a slow server from a stuck one), and stops with ctx.
//
// A failed check is tried again until the timeout: one API error should not cost a node.
func waitForActive(
	ctx context.Context,
	get func() (serverStatus, error),
	timeout, interval time.Duration,
) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)

	var last serverStatus
	var lastErr error
	for {
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return time.Since(start), ctx.Err()
		}

		status, err := get()
		if err != nil {
			lastErr = err
		} else {
			last, lastErr = status, nil
			switch status.Status {
			case "ACTIVE":
				return time.Since(start), nil
			case "ERROR":
				return time.Since(start), fmt.Errorf("server is in ERROR after %s: %s",
					time.Since(start).Round(time.Second), status)
			}
		}

		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return time.Since(start), fmt.Errorf("server is not ACTIVE after %s (%s), last check failed: %w",
					timeout, last, lastErr)
			}
			return time.Since(start), fmt.Errorf("server is not ACTIVE after %s: %s", timeout, last)
		}
	}
}
