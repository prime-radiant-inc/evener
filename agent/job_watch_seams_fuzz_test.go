//go:build serffuzz

package agent

import (
	"errors"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func FuzzWatchStoreLoadFaultSeams(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		want := errors.New("load events")
		state := jobstore.WatchSendState{Key: jobstore.WatchSendKey{ResolvedSendTo: "dlg_fault"}}
		_, _, err := staleDelegateWatchSendWithLoaders(state,
			func() (map[string]*jobstore.DelegateRecord, error) {
				return map[string]*jobstore.DelegateRecord{"dlg_fault": {DelegateID: "dlg_fault"}}, nil
			},
			func() ([]jobstore.Event, error) { return nil, want },
		)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}
