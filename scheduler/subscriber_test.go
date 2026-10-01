package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A burst far larger than any fixed buffer, while the subscriber reads nothing, must still
// reach it whole and in order (cancelling a 2000-task job aborts its queued tasks at once).
func TestSubscribe_SlowSubscriberLosesNoEvents(t *testing.T) {
	s := New(newMockProvisioner(), newTestConfig())
	events, unsub := s.Subscribe()
	defer unsub()

	const burst = 5000
	for i := 0; i < burst; i++ {
		s.broadcast(EventTaskAborted{Job: "job", Task: fmt.Sprint(i)})
	}
	s.broadcast(EventJobCompleted{Job: "job"})

	for i := 0; i < burst; i++ {
		select {
		case event := <-events:
			require.Equal(t, EventTaskAborted{Job: "job", Task: fmt.Sprint(i)}, event)
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}
	select {
	case event := <-events:
		assert.Equal(t, EventJobCompleted{Job: "job"}, event)
	case <-time.After(5 * time.Second):
		t.Fatal("EventJobCompleted never arrived")
	}
}

// A subscriber that stopped reading must not hold up the scheduler or other subscribers.
func TestSubscribe_UnsubscribedListenerDoesNotBlockOthers(t *testing.T) {
	s := New(newMockProvisioner(), newTestConfig())
	_, unsubAbandoned := s.Subscribe()
	events, unsub := s.Subscribe()
	defer unsub()

	s.broadcast(EventJobCompleted{Job: "before"})
	unsubAbandoned()
	unsubAbandoned() // idempotent
	s.broadcast(EventJobCompleted{Job: "after"})

	for _, job := range []string{"before", "after"} {
		select {
		case event := <-events:
			assert.Equal(t, EventJobCompleted{Job: job}, event)
		case <-time.After(5 * time.Second):
			t.Fatalf("event for %q never arrived", job)
		}
	}
}
