package internal

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/docker/docker/api/types/container"
)

// stepUsage follows a step container's resource usage through Docker's stats stream, one reading
// a second, for the step's "Step finished" record. The node's own state (provisioner/openstack)
// shows the whole VM; this tells which step used it.
type stepUsage struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// Written by the stream's goroutine until done is closed, read after
	readings     int
	peakMemory   uint64  // bytes, without the page cache, as `docker stats` shows it
	cpu          uint64  // nanoseconds of CPU time, all cores
	peakCPUCores float64 // the most cores used over one reading's interval
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
	// What `docker stats` subtracts: inactive_file on cgroup v2, total_inactive_file on v1
	for _, key := range []string{"inactive_file", "total_inactive_file"} {
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
	}
}

// stop ends the stream and waits for its goroutine. Safe to call more than once.
func (u *stepUsage) stop() {
	u.once.Do(func() {
		u.cancel()
		<-u.done
	})
}

// logAttrs stops the stream and gives the usage, or nothing without a reading.
func (u *stepUsage) logAttrs() []any {
	u.stop()
	if u.readings == 0 {
		return nil
	}
	return []any{
		"memoryPeakBytes", u.peakMemory,
		"cpuSeconds", float64(u.cpu/1e6) / 1e3, // to the millisecond
		"cpuPeakCores", float64(int(u.peakCPUCores*100)) / 100,
	}
}
