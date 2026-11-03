package openstack

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestMeterCountsBytesAndMarksEOF(t *testing.T) {
	m := &meter{r: iotest.OneByteReader(strings.NewReader("hello"))}
	data, err := io.ReadAll(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" || m.bytes != 5 {
		t.Errorf("read %q, counted %d bytes; want \"hello\", 5", data, m.bytes)
	}
	if m.eof.IsZero() {
		t.Error("eof not marked after the last byte")
	}
}

// slowReader takes 50ms to produce its one byte, inside Read so that the meter times it all
type slowReader struct{ done bool }

func (r *slowReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	time.Sleep(50 * time.Millisecond)
	r.done = true
	return copy(p, "x"), nil
}

func TestMeterTimesWaitsOnTheProducer(t *testing.T) {
	m := &meter{r: &slowReader{}}
	if _, err := io.ReadAll(m); err != nil {
		t.Fatal(err)
	}
	if m.wait < 50*time.Millisecond {
		t.Errorf("wait = %s, want at least the producer's 50ms", m.wait)
	}
}

func TestTransferStatsSplitTheTimeline(t *testing.T) {
	start := time.Now()
	s := transferStats{
		saved: meter{bytes: 1000, wait: 2 * time.Second},
		sent:  meter{bytes: 200, wait: 5 * time.Second, first: start.Add(time.Second), eof: start.Add(20 * time.Second)},
		start: start,
		end:   start.Add(26 * time.Second),
	}
	attrs := map[string]any{}
	kv := s.logAttrs()
	for i := 0; i < len(kv); i += 2 {
		attrs[kv[i].(string)] = kv[i+1]
	}
	want := map[string]any{
		"rawBytes": int64(1000), "sentBytes": int64(200), "totalSeconds": 26.0, "setupSeconds": 1.0,
		"saveWaitSeconds": 2.0, "compressWaitSeconds": 5.0, "sendSeconds": 14.0, "loadTailSeconds": 6.0,
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("%s = %v, want %v", k, attrs[k], v)
		}
	}
}
