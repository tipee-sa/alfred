package openstack

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The node's state is read this often, over its SSH connection, and logged as a "Node state"
// record: with the tasks' records, which name their node, it shows what a VM went through while
// a task ran there (CPU, memory, disk, network). Nothing runs on the VM but this command.
const nodeStateInterval = 15 * time.Second

// nodeStateTimeout bounds one reading: a VM too loaded to answer in time is skipped, not waited on.
const nodeStateTimeout = 10 * time.Second

// nodeStateCommand prints each source after a `#name` marker line, for parseNodeState.
const nodeStateCommand = "echo '#loadavg'; cat /proc/loadavg; " +
	"echo '#stat'; head -n1 /proc/stat; " +
	"echo '#meminfo'; cat /proc/meminfo; " +
	"echo '#netdev'; cat /proc/net/dev; " +
	"echo '#diskstats'; cat /proc/diskstats; " +
	"echo '#df'; df -P -B1 /"

// nodeSample is one reading of a node's state. Counters (CPU ticks, bytes) only make sense as the
// difference between two readings.
type nodeSample struct {
	at time.Time

	load1 float64

	// Ticks of /proc/stat's aggregate cpu line
	cpuTotal, cpuIdle, cpuIowait, cpuSteal uint64

	memoryTotal, memoryAvailable uint64 // bytes

	// Bytes received and sent by the VM's own interfaces: not loopback, not Docker's bridges and
	// veth pairs, which would count containers' traffic twice
	networkReceived, networkSent uint64

	// Bytes read and written on whole disks, not their partitions
	diskRead, diskWritten uint64

	diskTotal, diskUsed uint64 // bytes, of the filesystem holding /
}

// Whole disks of a VM, not their partitions (vda1, nvme0n1p1) or loop and device-mapper devices
var wholeDisk = regexp.MustCompile(`^((s|v|xv)d[a-z]+|nvme\d+n\d+)$`)

func parseNodeState(output string, at time.Time) (nodeSample, error) {
	s := nodeSample{at: at}
	seen := map[string]bool{}
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			section = strings.TrimPrefix(line, "#")
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		switch section {
		case "loadavg":
			load, err := strconv.ParseFloat(fields[0], 64)
			if err != nil {
				return s, fmt.Errorf("loadavg: %w", err)
			}
			s.load1 = load
			seen[section] = true

		case "stat":
			// cpu user nice system idle iowait irq softirq steal [guest guest_nice]: guest
			// time is already counted in user and nice
			if fields[0] != "cpu" || len(fields) < 9 {
				return s, fmt.Errorf("stat: unexpected line %q", line)
			}
			ticks := make([]uint64, 8)
			for i := range ticks {
				v, err := strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return s, fmt.Errorf("stat: %w", err)
				}
				ticks[i] = v
				s.cpuTotal += v
			}
			s.cpuIdle, s.cpuIowait, s.cpuSteal = ticks[3], ticks[4], ticks[7]
			seen[section] = true

		case "meminfo":
			if len(fields) < 2 {
				continue
			}
			kB, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				s.memoryTotal = kB * 1024
				seen[section] = true
			case "MemAvailable:":
				s.memoryAvailable = kB * 1024
			}

		case "netdev":
			// "  ens3: 1234 5 0 0 0 0 0 0 5678 6 ..." after two header lines
			name, counters, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
				continue
			}
			c := strings.Fields(counters)
			if len(c) < 9 {
				continue
			}
			rx, err1 := strconv.ParseUint(c[0], 10, 64)
			tx, err2 := strconv.ParseUint(c[8], 10, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			s.networkReceived += rx
			s.networkSent += tx
			seen[section] = true

		case "diskstats":
			// major minor name reads merged sectorsRead ms writes merged sectorsWritten ...
			if len(fields) < 10 || !wholeDisk.MatchString(fields[2]) {
				continue
			}
			read, err1 := strconv.ParseUint(fields[5], 10, 64)
			written, err2 := strconv.ParseUint(fields[9], 10, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			// Sectors of 512 bytes, whatever the device's own sector size
			s.diskRead += read * 512
			s.diskWritten += written * 512
			seen[section] = true

		case "df":
			// Filesystem 1-blocks Used Available Capacity Mounted-on, after a header line
			if fields[0] == "Filesystem" || len(fields) < 6 {
				continue
			}
			total, err1 := strconv.ParseUint(fields[1], 10, 64)
			used, err2 := strconv.ParseUint(fields[2], 10, 64)
			if err1 != nil || err2 != nil {
				return s, fmt.Errorf("df: unexpected line %q", line)
			}
			s.diskTotal, s.diskUsed = total, used
			seen[section] = true
		}
	}

	for _, name := range []string{"loadavg", "stat", "meminfo", "df"} {
		if !seen[name] {
			return s, fmt.Errorf("no %s in the output", name)
		}
	}
	return s, nil
}

// logAttrs gives the reading's values, and with the previous reading the rates since then. The
// first reading of a node has no rates.
func (s nodeSample) logAttrs(prev *nodeSample) []any {
	attrs := []any{
		"load1", s.load1,
		"memoryUsedBytes", s.memoryTotal - min(s.memoryAvailable, s.memoryTotal),
		"memoryTotalBytes", s.memoryTotal,
		"diskUsedBytes", s.diskUsed,
		"diskTotalBytes", s.diskTotal,
	}
	if prev == nil {
		return attrs
	}

	elapsed := s.at.Sub(prev.at).Seconds()
	// Counters only grow; a smaller one (a reset, an interface gone) gives no rate
	delta := func(cur, old uint64) (uint64, bool) { return cur - old, cur >= old }
	percent := func(part uint64, total uint64) float64 {
		return float64(int(float64(part)/float64(total)*1000)) / 10 // one decimal
	}
	perSecond := func(bytes uint64) int64 { return int64(float64(bytes) / elapsed) }

	// The kernel lets iowait go down (proc(5)): an interval where any of them does gives no
	// CPU rates rather than a wrapped difference
	total, totalOk := delta(s.cpuTotal, prev.cpuTotal)
	idle, idleOk := delta(s.cpuIdle, prev.cpuIdle)
	iowait, iowaitOk := delta(s.cpuIowait, prev.cpuIowait)
	steal, stealOk := delta(s.cpuSteal, prev.cpuSteal)
	if totalOk && idleOk && iowaitOk && stealOk && total > 0 {
		attrs = append(attrs,
			"cpuPercent", percent(total-min(idle+iowait, total), total),
			"cpuIowaitPercent", percent(iowait, total),
			// Time the hypervisor gave this VM's CPUs to others: a busy OpenStack host
			"cpuStealPercent", percent(steal, total),
		)
	}
	if elapsed > 0 {
		if rx, ok := delta(s.networkReceived, prev.networkReceived); ok {
			attrs = append(attrs, "networkReceivedBytesPerSecond", perSecond(rx))
		}
		if tx, ok := delta(s.networkSent, prev.networkSent); ok {
			attrs = append(attrs, "networkSentBytesPerSecond", perSecond(tx))
		}
		if read, ok := delta(s.diskRead, prev.diskRead); ok {
			attrs = append(attrs, "diskReadBytesPerSecond", perSecond(read))
		}
		if written, ok := delta(s.diskWritten, prev.diskWritten); ok {
			attrs = append(attrs, "diskWrittenBytesPerSecond", perSecond(written))
		}
	}
	return attrs
}

// readState runs nodeStateCommand on the node, within nodeStateTimeout.
func (n *Node) readState() (nodeSample, error) {
	output, at, err := n.stateReader.read(func() (outputSession, error) { return n.ssh.NewSession() }, nodeStateCommand, nodeStateTimeout)
	if err != nil {
		return nodeSample{}, err
	}
	return parseNodeState(string(output), at)
}

// outputSession is what boundedReader needs of an *ssh.Session.
type outputSession interface {
	Output(cmd string) ([]byte, error)
	Close() error
}

// boundedReader runs a command in a new SSH session within a timeout covering the session's
// opening too: both wait for the VM, which may stop answering on a connection that stays up.
// A reading that times out goes on in the background until the connection ends or its session
// closes, and the next ones fail at once meanwhile, so a VM that does not answer holds one
// goroutine, not one per interval.
type boundedReader struct {
	pending atomic.Bool
}

func (r *boundedReader) read(open func() (outputSession, error), cmd string, timeout time.Duration) ([]byte, time.Time, error) {
	if !r.pending.CompareAndSwap(false, true) {
		return nil, time.Time{}, fmt.Errorf("the previous reading has not ended")
	}

	type result struct {
		output []byte
		at     time.Time
		err    error
	}
	done := make(chan result, 1) // never blocks the goroutine, read or not
	var mu sync.Mutex
	var session outputSession
	var timedOut bool

	go func() {
		defer r.pending.Store(false)
		s, err := open()
		if err != nil {
			done <- result{err: fmt.Errorf("failed to open an SSH session: %w", err)}
			return
		}
		mu.Lock()
		if timedOut {
			// Opened after the deadline: nobody waits for it
			mu.Unlock()
			_ = s.Close()
			return
		}
		session = s
		mu.Unlock()
		defer s.Close()

		at := time.Now()
		output, err := s.Output(cmd)
		if err != nil {
			err = fmt.Errorf("failed to read the node's state: %w", err)
		}
		done <- result{output, at, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.output, res.at, res.err
	case <-timer.C:
		mu.Lock()
		timedOut = true
		s := session
		mu.Unlock()
		if s != nil {
			// Unblocks Output. In the background: closing writes to the VM, which a connection
			// that stopped draining blocks as well, and the caller must not wait past timeout
			go func() { _ = s.Close() }()
		}
		return nil, time.Time{}, fmt.Errorf("the node did not answer within %s", timeout)
	}
}

// watchState logs the node's state every nodeStateInterval until the node is terminated. A
// failed reading is skipped: the SSH keepalive reports a connection that is gone.
func (n *Node) watchState() {
	ticker := time.NewTicker(nodeStateInterval)
	defer ticker.Stop()

	var prev *nodeSample
	for {
		if n.terminated.Load() {
			return
		}
		sample, err := n.readState()
		if n.terminated.Load() {
			return
		}
		if err != nil {
			n.log.Debug("Failed to read the node's state", "error", err)
			prev = nil
		} else {
			n.log.Info("Node state", sample.logAttrs(prev)...)
			prev = &sample
		}
		<-ticker.C
	}
}
