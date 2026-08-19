//go:build evenerfuzz

package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func FuzzWatchStoreLoadFaultSeams(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		want := errors.New("load events")

		jm := newTestJM(t)
		s := &Session{jobManager: jm}
		s.renderUnreachableChildPendingsWithLoaders(nil,
			func() (map[string]*jobstore.JobRecord, error) { return map[string]*jobstore.JobRecord{}, nil },
			func() (jobstore.WatchSendRecord, error) { return jobstore.WatchSendRecord{}, want },
		)
		if got := s.peekNotifications(); got != 0 {
			t.Fatalf("load failure enqueued %d notifications", got)
		}

		child := &Session{}
		parent := &Session{subagents: &subagentManager{subs: map[string]*subagent{
			"child": {id: "child", sess: child, closed: true},
		}}}
		if _, err := parent.drainPendingWatchSendsReport(context.Background()); err != nil {
			t.Fatalf("drain child without manager: %v", err)
		}
	})
}
