package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

type navigationPublisherRecorder struct {
	mu      sync.Mutex
	methods []string
	items   []appwire.NavigationInvalidatedPayload
	seen    chan struct{}
}

func (p *navigationPublisherRecorder) BroadcastAll(method string, params any) {
	payload := params.(appwire.NavigationInvalidatedPayload)
	p.mu.Lock()
	p.methods = append(p.methods, method)
	p.items = append(p.items, payload)
	p.mu.Unlock()
	p.seen <- struct{}{}
}

func (p *navigationPublisherRecorder) snapshot() ([]string, []appwire.NavigationInvalidatedPayload) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.methods...), append([]appwire.NavigationInvalidatedPayload(nil), p.items...)
}

func TestNavigationPublisherLifecycleCoalescesReadinessAndBroadcastsFIFOOnce(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	recorder := &navigationPublisherRecorder{seen: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		runNavigationPublisher(ctx, service, recorder)
		close(done)
	}()

	source.changeTitle("one")
	first, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	source.changeTitle("two")
	second, err := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-recorder.seen:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}

	methods, payloads := recorder.snapshot()
	if len(methods) != 2 || len(payloads) != 2 {
		t.Fatalf("broadcasts = %d/%d, want two", len(methods), len(payloads))
	}
	for _, method := range methods {
		if method != appwire.NotifyEvenerNavigationInvalidated {
			t.Fatalf("method = %q", method)
		}
	}
	if payloads[0].Sequence != 1 || payloads[1].Sequence != 2 {
		t.Fatalf("broadcast order = %d,%d; refresh outcomes = %+v,%+v", payloads[0].Sequence, payloads[1].Sequence, first, second)
	}
	if got := service.DrainPublications(); len(got) != 0 {
		t.Fatalf("publisher left publications queued: %+v", got)
	}
	select {
	case <-service.PublicationReady():
		t.Fatal("publisher left duplicate readiness token")
	default:
	}
	cancel()
	<-done
}

func TestNavigationPublisherDrainDoesNotRefresh(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	before := source.captureCount()
	if got := service.DrainPublications(); len(got) != 0 {
		t.Fatalf("empty drain = %+v", got)
	}
	if got := source.captureCount(); got != before {
		t.Fatalf("drain started capture: before=%d after=%d", before, got)
	}
}
