package scheduler

import "sync"

// subscriber delivers events to one Subscribe() caller, in order and without loss.
// forwardEvents pushes onto an unbounded queue, which a dedicated goroutine drains into the
// subscriber's channel: a slow subscriber costs memory instead of events, and never slows
// down the scheduler.
//
// Dropping events is not an option: the server rebuilds its state from them. Cancelling a
// job with ~1400 queued tasks broadcasts their aborts in one burst, and when that overflowed
// a fixed buffer the lost EventTaskAborted left tasks "queued" forever on a completed job.
// A lost EventJobCompleted would leave the job running, and every `alfred watch` on it
// waiting, forever.
type subscriber struct {
	out chan Event

	mu    sync.Mutex
	queue []Event

	wake chan struct{} // capacity 1: one pending wake-up covers any number of pushes
	done chan struct{} // closed by unsubscribe, stops the drain goroutine
}

func newSubscriber() *subscriber {
	sub := &subscriber{
		out:  make(chan Event),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	go sub.drain()
	return sub
}

// push queues an event without blocking.
func (sub *subscriber) push(event Event) {
	sub.mu.Lock()
	sub.queue = append(sub.queue, event)
	sub.mu.Unlock()

	select {
	case sub.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// drain hands queued events to the subscriber one by one, blocking on it as long as needed.
func (sub *subscriber) drain() {
	for {
		sub.mu.Lock()
		if len(sub.queue) == 0 {
			sub.mu.Unlock()
			select {
			case <-sub.wake:
				continue
			case <-sub.done:
				return
			}
		}
		event := sub.queue[0]
		sub.queue[0] = nil // let the event be collected once delivered
		sub.queue = sub.queue[1:]
		sub.mu.Unlock()

		select {
		case sub.out <- event:
		case <-sub.done:
			return
		}
	}
}
