package openstack

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// testdata/node-state.txt is nodeStateCommand's output on a Debian 12 VM with Docker: two disks
// with partitions, loopback and docker0 next to the VM's own interface.
func readNodeStateFixture(t *testing.T) string {
	t.Helper()
	output, err := os.ReadFile("testdata/node-state.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestParseNodeState(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s, err := parseNodeState(readNodeStateFixture(t), at)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"load1", s.load1, 0.32},
		// user nice system idle iowait irq softirq steal, without guest time
		{"cpuTotal", s.cpuTotal, uint64(660965866)},
		{"cpuIdle", s.cpuIdle, uint64(655476542)},
		{"cpuIowait", s.cpuIowait, uint64(986758)},
		{"cpuSteal", s.cpuSteal, uint64(14620)},
		{"memoryTotal", s.memoryTotal, uint64(16388428 * 1024)},
		{"memoryAvailable", s.memoryAvailable, uint64(15018208 * 1024)},
		// ens3 only: not lo, not docker0
		{"networkReceived", s.networkReceived, uint64(58978419874)},
		{"networkSent", s.networkSent, uint64(534990939824)},
		// sda and sdb, not their partitions
		{"diskRead", s.diskRead, uint64(132311986176)},
		{"diskWritten", s.diskWritten, uint64(1364546475008)},
		{"diskTotal", s.diskTotal, uint64(52666433536)},
		{"diskUsed", s.diskUsed, uint64(40959102976)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseNodeStateRefusesAnIncompleteOutput(t *testing.T) {
	// A VM that answered only part of the command: no df
	output := readNodeStateFixture(t)
	output = output[:strings.Index(output, "#df")]
	if _, err := parseNodeState(output, time.Now()); err == nil {
		t.Fatal("an output without df parsed")
	}
}

func attrsMap(t *testing.T, attrs []any) map[string]any {
	t.Helper()
	m := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		m[attrs[i].(string)] = attrs[i+1]
	}
	return m
}

func TestNodeSampleLogAttrs(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	prev := nodeSample{
		at:       at,
		cpuTotal: 1000, cpuIdle: 600, cpuIowait: 100, cpuSteal: 10,
		networkReceived: 1000, networkSent: 5000,
		diskRead: 0, diskWritten: 100,
	}
	cur := nodeSample{
		at:    at.Add(10 * time.Second),
		load1: 1.5,
		// 1000 ticks: 250 idle, 150 iowait, 50 steal
		cpuTotal: 2000, cpuIdle: 850, cpuIowait: 250, cpuSteal: 60,
		memoryTotal: 8 << 30, memoryAvailable: 6 << 30,
		networkReceived: 21000, networkSent: 4000, // a counter that went down: no rate
		diskRead: 5120, diskWritten: 100,
		diskTotal: 100 << 30, diskUsed: 40 << 30,
	}

	first := attrsMap(t, cur.logAttrs(nil))
	if _, ok := first["cpuPercent"]; ok {
		t.Error("a first reading has a CPU rate")
	}
	if first["memoryUsedBytes"] != uint64(2<<30) {
		t.Errorf("memoryUsedBytes = %v, want %v", first["memoryUsedBytes"], uint64(2<<30))
	}

	got := attrsMap(t, cur.logAttrs(&prev))
	for key, want := range map[string]any{
		"load1":                         1.5,
		"cpuPercent":                    60.0, // all but idle and iowait
		"cpuIowaitPercent":              15.0,
		"cpuStealPercent":               5.0,
		"networkReceivedBytesPerSecond": int64(2000),
		"diskReadBytesPerSecond":        int64(512),
		"diskWrittenBytesPerSecond":     int64(0),
		"diskUsedBytes":                 uint64(40 << 30),
	} {
		if got[key] != want {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got[key], got[key], want, want)
		}
	}
	if _, ok := got["networkSentBytesPerSecond"]; ok {
		t.Error("a counter that went down has a rate")
	}
}

func TestNodeSampleLogAttrsSkipsCPUWhenIowaitGoesDown(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	prev := nodeSample{at: at, cpuTotal: 1000, cpuIdle: 600, cpuIowait: 100}
	// proc(5): the iowait counter may decrease
	cur := nodeSample{at: at.Add(10 * time.Second), cpuTotal: 2000, cpuIdle: 1500, cpuIowait: 90}

	got := attrsMap(t, cur.logAttrs(&prev))
	for _, key := range []string{"cpuPercent", "cpuIowaitPercent", "cpuStealPercent"} {
		if v, ok := got[key]; ok {
			t.Errorf("%s = %v over an interval where iowait went down", key, v)
		}
	}
}

// fakeSession is an SSH session whose Output blocks until it is closed when hang is set.
type fakeSession struct {
	hang   bool
	closed chan struct{}
	once   sync.Once
}

func newFakeSession(hang bool) *fakeSession {
	return &fakeSession{hang: hang, closed: make(chan struct{})}
}

func (s *fakeSession) Output(string) ([]byte, error) {
	if s.hang {
		<-s.closed
		return nil, errors.New("session closed")
	}
	return []byte("state"), nil
}

func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestBoundedReaderReads(t *testing.T) {
	var r boundedReader
	session := newFakeSession(false)
	output, _, err := r.read(func() (outputSession, error) { return session, nil }, "cmd", time.Second)
	if err != nil || string(output) != "state" {
		t.Fatalf("read %q, %v", output, err)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Error("the session was not closed after the reading")
	}
}

func TestBoundedReaderBoundsTheSessionsOpening(t *testing.T) {
	// A VM that stops answering on a live connection: opening a session waits
	var r boundedReader
	release := make(chan struct{})
	late := newFakeSession(false)
	open := func() (outputSession, error) {
		<-release
		return late, nil
	}

	start := time.Now()
	if _, _, err := r.read(open, "cmd", 50*time.Millisecond); err == nil {
		t.Fatal("a session that never opened gave a reading")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gave up after %s, want the 50ms timeout", elapsed)
	}

	// While it is stuck, the next readings fail at once instead of piling up goroutines
	start = time.Now()
	if _, _, err := r.read(func() (outputSession, error) { return newFakeSession(false), nil }, "cmd", time.Second); err == nil {
		t.Error("a reading started while the previous one was stuck")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("the next reading waited %s", elapsed)
	}

	// The session opening after the deadline is closed, and readings work again
	close(release)
	select {
	case <-late.closed:
	case <-time.After(time.Second):
		t.Fatal("a session opened after the deadline stayed open")
	}
	deadline := time.Now().Add(time.Second)
	for r.pending.Load() && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if _, _, err := r.read(func() (outputSession, error) { return newFakeSession(false), nil }, "cmd", time.Second); err != nil {
		t.Errorf("reading after the stuck one ended: %v", err)
	}
}

func TestBoundedReaderBoundsTheCommand(t *testing.T) {
	var r boundedReader
	session := newFakeSession(true)
	start := time.Now()
	if _, _, err := r.read(func() (outputSession, error) { return session, nil }, "cmd", 50*time.Millisecond); err == nil {
		t.Fatal("a command that never ended gave a reading")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gave up after %s, want the 50ms timeout", elapsed)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Error("the session of a timed-out command stayed open")
	}
}
