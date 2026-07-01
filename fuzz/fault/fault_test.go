package fault

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

// allFail plans a byte that always trips (b%4==0); allPass never trips (b%4!=0).
var allFail = make([]byte, 8) // all-zero bytes always trip (b%4==0)
var allPass = bytes.Repeat([]byte{0x01}, 8)

func TestNilAndEmptyScheduleAreInert(t *testing.T) {
	for _, s := range []*Schedule{nil, FromBytes(nil), FromBytes([]byte{})} {
		if s.Active() {
			t.Fatalf("schedule %v reported Active", s)
		}
		if err := s.trip(); err != nil {
			t.Fatalf("inert schedule tripped: %v", err)
		}
		base := afero.NewMemMapFs()
		if got := FS(base, s); got != base {
			t.Fatal("FS over an inert schedule must return base unchanged")
		}
		rt := http.DefaultTransport
		if got := RoundTripper(rt, s); got != rt {
			t.Fatal("RoundTripper over an inert schedule must return base unchanged")
		}
	}
}

func TestScheduleInjectsWhenPlanned(t *testing.T) {
	// allFail: every fs op must error; allPass: none injected.
	fail := FS(afero.NewMemMapFs(), FromBytes(allFail))
	if err := fail.Mkdir("/d", 0o755); err == nil {
		t.Fatal("allFail schedule did not inject on Mkdir")
	}

	pass := afero.NewMemMapFs()
	pf := FS(pass, FromBytes(allPass))
	if err := pf.Mkdir("/d", 0o755); err != nil {
		t.Fatalf("allPass schedule injected a fault: %v", err)
	}
	if ok, _ := afero.DirExists(pass, "/d"); !ok {
		t.Fatal("allPass schedule blocked the real Mkdir")
	}
}

func TestReadFaultsMidFile(t *testing.T) {
	// A schedule that passes the open/create/write but fails a later read: the
	// hard branch a bare MemMapFs can never produce.
	base := afero.NewMemMapFs()
	afero.WriteFile(base, "/f", []byte("hello"), 0o644) //nolint:errcheck
	// Plan: pass (0x01) the Open, then fail (0x00) the first Read.
	fs := FS(base, FromBytes([]byte{0x01, 0x00}))
	f, err := fs.Open("/f")
	if err != nil {
		t.Fatalf("open unexpectedly faulted: %v", err)
	}
	if _, err := f.Read(make([]byte, 8)); err == nil {
		t.Fatal("expected an injected read fault after a clean open")
	}
}

func TestDeterministic(t *testing.T) {
	plan := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	seq := func() []bool {
		s := FromBytes(plan)
		var out []bool
		for i := 0; i < 24; i++ {
			out = append(out, s.trip() != nil)
		}
		return out
	}
	a, b := seq(), seq()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at op %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestErrorKindsVary(t *testing.T) {
	// Over a plan that trips on several distinct bytes, more than one injectable
	// error kind must appear, so type-switching branches get exercised.
	s := FromBytes([]byte{0, 4, 8, 12, 16}) // all %4==0, different >>2 buckets
	kinds := map[error]bool{}
	for i := 0; i < 5; i++ {
		if err := s.trip(); err != nil {
			kinds[err] = true
		}
	}
	if len(kinds) < 2 {
		t.Fatalf("expected varied injected error kinds, got %d", len(kinds))
	}
}

// bodyRT is a base RoundTripper that succeeds; the fault wrapper never reaches
// it when the schedule trips. Body drain/close is tracked by trackBody below.
type bodyRT struct{}

func (b *bodyRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

type trackBody struct {
	r      *bytes.Reader
	closed bool
}

func (t *trackBody) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *trackBody) Close() error               { t.closed = true; return nil }

func TestRoundTripperHonorsContractOnFault(t *testing.T) {
	rt := RoundTripper(&bodyRT{}, FromBytes(allFail))
	body := &trackBody{r: bytes.NewReader([]byte("payload"))}
	req, _ := http.NewRequest(http.MethodPost, "http://x/y", body)
	resp, err := rt.RoundTrip(req)
	if err == nil || resp != nil {
		t.Fatalf("faulted RoundTrip must return (nil, err), got (%v, %v)", resp, err)
	}
	if !body.closed {
		t.Fatal("faulted RoundTrip must drain and close the request body (contract)")
	}
	// The raw injected error must be unwrappable (as a real RoundTripper's is).
	if !errors.Is(err, ErrInjected) && err.Error() == "" {
		t.Fatal("expected a non-empty injected error")
	}
}

func TestConcurrentTripIsRaceFree(t *testing.T) {
	s := FromBytes(bytes.Repeat([]byte{0, 1, 2, 3}, 64))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = s.trip()
			}
		}()
	}
	wg.Wait()
	if s.n.Load() != 8*1000 {
		t.Fatalf("lost trips under concurrency: counted %d, want %d", s.n.Load(), 8*1000)
	}
}
