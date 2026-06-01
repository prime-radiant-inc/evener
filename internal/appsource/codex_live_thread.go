package appsource

import (
	"context"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
)

type codexLiveThread struct {
	client      *appwire.Client
	close       func() error
	closeOnce   sync.Once
	done        chan struct{}
	mu          sync.Mutex
	subscribers map[chan appwire.Notification]struct{}
	backlog     []appwire.Notification
	retiring    bool
	closed      bool
	hadSub      bool
}

const codexLiveSubscriberBuffer = 128
const codexLiveBacklogLimit = 4096

var codexLiveNoSubscriberTimeout = 30 * time.Second

func (live *codexLiveThread) publish(notification appwire.Notification) {
	live.mu.Lock()
	if live.closed || live.retiring {
		live.mu.Unlock()
		return
	}
	if len(live.subscribers) == 0 && !live.hadSub {
		live.backlog = append(live.backlog, notification)
		if len(live.backlog) > codexLiveBacklogLimit {
			live.backlog = live.backlog[len(live.backlog)-codexLiveBacklogLimit:]
		}
	}
	for subscriber := range live.subscribers {
		select {
		case subscriber <- notification:
		default:
			delete(live.subscribers, subscriber)
			close(subscriber)
		}
	}
	shouldRetire := live.hadSub && len(live.subscribers) == 0 && !live.closed && !live.retiring
	if shouldRetire {
		live.retiring = true
	}
	live.mu.Unlock()
	if shouldRetire {
		live.retire()
	}
}

func (live *codexLiveThread) subscribe(ctx context.Context) <-chan appwire.Notification {
	live.mu.Lock()
	capacity := codexLiveSubscriberBuffer + len(live.backlog)
	out := make(chan appwire.Notification, capacity)
	if live.closed || live.retiring {
		close(out)
		live.mu.Unlock()
		return out
	}
	for _, notification := range live.backlog {
		out <- notification
	}
	live.subscribers[out] = struct{}{}
	live.hadSub = true
	live.mu.Unlock()

	go func() {
		<-ctx.Done()
		live.unsubscribe(out)
	}()
	return out
}

func (live *codexLiveThread) unsubscribe(out chan appwire.Notification) {
	live.mu.Lock()
	if _, ok := live.subscribers[out]; ok {
		delete(live.subscribers, out)
		close(out)
	}
	shouldRetire := live.hadSub && len(live.subscribers) == 0 && !live.closed && !live.retiring
	if shouldRetire {
		live.retiring = true
	}
	live.mu.Unlock()
	if shouldRetire {
		live.retire()
	}
}

func (live *codexLiveThread) retireIfNoSubscriber(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-live.done:
		return
	}
	live.mu.Lock()
	shouldRetire := !live.hadSub && len(live.subscribers) == 0 && !live.closed && !live.retiring
	if shouldRetire {
		live.retiring = true
	}
	live.mu.Unlock()
	if shouldRetire {
		live.retire()
	}
}

func (live *codexLiveThread) finish() {
	live.mu.Lock()
	if !live.closed {
		live.closed = true
		for subscriber := range live.subscribers {
			close(subscriber)
		}
		live.subscribers = map[chan appwire.Notification]struct{}{}
		live.backlog = nil
	}
	live.mu.Unlock()
	close(live.done)
}

func (live *codexLiveThread) retire() {
	live.closeOnce.Do(func() {
		_ = live.close()
	})
}

func (live *codexLiveThread) isClosed() bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.closed || live.retiring
}
