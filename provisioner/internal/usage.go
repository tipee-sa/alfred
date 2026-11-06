package internal

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
)

// stepUsage follows a step container's resource usage through Docker's stats stream, one reading
// a second, for the step's "Step finished" record. The node's own state (provisioner/openstack)
// shows the whole VM; this tells which step used it.
// stepUsageStopTimeout bounds the wait for the stats stream to end: over a node's SSH
// connection that stopped draining, cancelling it can block, and the step's end (artifacts,
// cleanup, its slots) must not wait for telemetry.
var stepUsageStopTimeout = 5 * time.Second

type stepUsage struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	// Whether the stream's goroutine ended within stepUsageStopTimeout: only then are the
	// readings below safe to read. Set inside once.
	ended bool

	// Written by the stream's goroutine until done is closed, read after
	readings     int
	peakMemory   uint64  // bytes, without the page cache, as `docker stats` shows it
	cpu          uint64  // nanoseconds of CPU time, all cores
	peakCPUCores float64 // the most cores used over one reading's interval
	// Whether any reading had a previous one to measure an interval against: a step shorter
	// than two readings has no peak, rather than a peak of zero
	peakMeasured bool
}

// watchStepUsage starts reading the container's stats until stop. A daemon that refuses them
// costs the step nothing: its record goes without usage.
func watchStepUsage(ctx context.Context, docker DockerClient, containerID string) *stepUsage {
	ctx, cancel := context.WithCancel(ctx)
	u := &stepUsage{cancel: cancel, done: make(chan struct{})}

	go func() {
		defer close(u.done)

		stats, err := docker.ContainerStats(ctx, containerID, true)
		if err != nil {
			return
		}
		defer stats.Body.Close()

		decoder := json.NewDecoder(stats.Body)
		for {
			var reading container.StatsResponse
			if err := decoder.Decode(&reading); err != nil {
				return // the stream ended (the container exited) or stop cancelled it
			}
			u.add(reading)
		}
	}()

	return u
}

func (u *stepUsage) add(r container.StatsResponse) {
	// A stopped container's readings are zeros: they change no maximum
	u.readings++

	memory := r.MemoryStats.Usage
	// What `docker stats` subtracts: total_inactive_file on cgroup v1, which has inactive_file
	// too but without the descendant cgroups, and inactive_file on v2
	for _, key := range []string{"total_inactive_file", "inactive_file"} {
		if cache, ok := r.MemoryStats.Stats[key]; ok {
			memory -= min(cache, memory)
			break
		}
	}
	u.peakMemory = max(u.peakMemory, memory)

	cpu := r.CPUStats.CPUUsage.TotalUsage
	u.cpu = max(u.cpu, cpu)

	// The daemon sends each reading with the previous one; the first has none
	interval := r.Read.Sub(r.PreRead)
	precpu := r.PreCPUStats.CPUUsage.TotalUsage
	if !r.PreRead.IsZero() && interval > 0 && cpu >= precpu && precpu > 0 {
		u.peakCPUCores = max(u.peakCPUCores, float64(cpu-precpu)/float64(interval.Nanoseconds()))
		u.peakMeasured = true
	}
}

// stop ends the stream and waits for its goroutine, at most stepUsageStopTimeout, and says
// whether it ended. Safe to call more than once.
func (u *stepUsage) stop() bool {
	u.once.Do(func() {
		u.cancel()
		timer := time.NewTimer(stepUsageStopTimeout)
		defer timer.Stop()
		select {
		case <-u.done:
			u.ended = true
		case <-timer.C:
			// Left to end on its own, when the connection does
		}
	})
	return u.ended
}

// logAttrs stops the stream and gives the usage, or nothing without a reading or when the
// stream did not end in time.
func (u *stepUsage) logAttrs() []any {
	if !u.stop() || u.readings == 0 {
		return nil
	}
	attrs := []any{
		"memoryPeakBytes", u.peakMemory,
		"cpuSeconds", float64(u.cpu/1e6) / 1e3, // to the millisecond
	}
	if u.peakMeasured {
		attrs = append(attrs, "cpuPeakCores", float64(int(u.peakCPUCores*100))/100)
	}
	return attrs
}
