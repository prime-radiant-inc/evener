package hubcore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestManagerModeStoreLifetimeAndRevision(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	s := NewSessionPreferencesStore(path)
	id, err := s.HubID()
	if err != nil || id == "" {
		t.Fatalf("HubID = %q, %v", id, err)
	}
	reopened := NewSessionPreferencesStore(path)
	gotID, err := reopened.HubID()
	if err != nil || gotID != id {
		t.Fatalf("reopened ID = %q, %v", gotID, err)
	}
	off, err := s.Manager("local:same")
	if err != nil || off != (appwire.ManagerSetting{}) {
		t.Fatalf("default = %+v, %v", off, err)
	}
	on, err := s.SetManager("local:same", id, true)
	if err != nil || !on.Enabled || on.HubID != id || on.Revision != 1 {
		t.Fatalf("enable = %+v, %v", on, err)
	}
	persisted, err := reopened.Manager("local:same")
	if err != nil || persisted != on {
		t.Fatalf("persisted = %+v, %v", persisted, err)
	}
	other, err := s.Manager("host:same")
	if err != nil || other != off {
		t.Fatalf("source collision = %+v, %v", other, err)
	}
	noop, err := s.SetManager("local:same", id, true)
	if err != nil || noop != on {
		t.Fatalf("noop = %+v, %v", noop, err)
	}
	for _, enabled := range []bool{true, false} {
		refused, err := reopened.SetManager("local:same", "competitor", enabled)
		if !errors.Is(err, ErrManagerAuthority) || refused != on {
			t.Fatalf("competitor = %+v, %v", refused, err)
		}
	}
	disabled, err := s.SetManager("local:same", id, false)
	if err != nil || disabled.Enabled || disabled.HubID != id || disabled.Revision != 2 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	noop, err = s.SetManager("local:same", "competitor", false)
	if err != nil || noop != disabled {
		t.Fatalf("disabled noop = %+v, %v", noop, err)
	}
	selected, err := reopened.SetManager("local:same", "competitor", true)
	if err != nil || !selected.Enabled || selected.HubID != "competitor" || selected.Revision != 3 {
		t.Fatalf("reselect = %+v, %v", selected, err)
	}
}

func TestManagerModeAuthoritySelectionRace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	start := make(chan struct{})
	type result struct {
		setting appwire.ManagerSetting
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, hub := range []string{"hub-a", "hub-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			setting, err := NewSessionPreferencesStore(path).SetManager("local:root", hub, true)
			results <- result{setting, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	var selected appwire.ManagerSetting
	for result := range results {
		if result.err == nil {
			successes++
			selected = result.setting
		} else if errors.Is(result.err, ErrManagerAuthority) {
			conflicts++
		} else {
			t.Fatal(result.err)
		}
	}
	current, err := NewSessionPreferencesStore(path).Manager("local:root")
	if err != nil || successes != 1 || conflicts != 1 || current != selected || current.Revision != 1 {
		t.Fatalf("race successes=%d conflicts=%d selected=%+v current=%+v err=%v", successes, conflicts, selected, current, err)
	}
}

func TestManagerModeStorageFailurePreservesSetting(t *testing.T) {
	t.Parallel()
	s := NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	want, err := s.SetManager("local:root", "hub", true)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_manager_write BEFORE UPDATE ON session_manager BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetManager("local:root", "hub", false)
	if err == nil || got != want {
		t.Fatalf("failed write = %+v, %v, want unchanged %+v and error", got, err, want)
	}
	got, err = s.Manager("local:root")
	if err != nil || got != want {
		t.Fatalf("persisted after failure = %+v, %v", got, err)
	}
}

func TestManagerModeHubIDConcurrentFirstOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			id, err := NewSessionPreferencesStore(path).HubID()
			results <- result{id, err}
		}()
	}
	close(start)
	var want string
	for range 8 {
		got := <-results
		if got.err != nil || got.id == "" {
			t.Fatalf("HubID = %q, %v", got.id, got.err)
		}
		if want == "" {
			want = got.id
		}
		if got.id != want {
			t.Fatalf("concurrent HubID = %q, want %q", got.id, want)
		}
	}
}
