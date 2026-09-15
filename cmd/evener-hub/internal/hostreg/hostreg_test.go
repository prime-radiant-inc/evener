package hostreg

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

func host(name string) Host {
	return Host{Name: name, SSH: name + ".local"}
}

func TestNewRejectsDuplicate(t *testing.T) {
	_, err := New([]Host{host("m4"), host("m4")})
	if !errors.Is(err, ErrDuplicateHost) {
		t.Fatalf("New with duplicate = %v, want ErrDuplicateHost", err)
	}
}

func TestNewBuildsAndGets(t *testing.T) {
	r, err := New([]Host{host("m4"), host("studio")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := r.Get("m4"); !ok {
		t.Error("Get(m4) missing")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(nope) unexpectedly present")
	}
}

func TestAddRejectsNamedErrors(t *testing.T) {
	tests := []struct {
		name  string
		entry Host
		want  error
	}{
		{"empty name", Host{Name: "", SSH: "h"}, ErrInvalidName},
		{"bad charset slash", Host{Name: "a/b", SSH: "h"}, ErrInvalidName},
		{"bad charset space", Host{Name: "a b", SSH: "h"}, ErrInvalidName},
		{"bad charset colon", Host{Name: "a:b", SSH: "h"}, ErrInvalidName},
		{"dotdot alone", Host{Name: "..", SSH: "h"}, ErrInvalidName},
		{"dotdot inside", Host{Name: "a..b", SSH: "h"}, ErrInvalidName},
		{"reserved local", Host{Name: ReservedName, SSH: "h"}, ErrReservedName},
		{"empty ssh", Host{Name: "m4", SSH: "  "}, ErrMissingSSH},
		{"ambiguous user", Host{Name: "m4", SSH: "u@h", User: "jesse"}, ErrAmbiguousSSHUser},
		{"empty root", Host{Name: "m4", SSH: "h", Roots: []string{"ok", " "}}, ErrEmptyRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New(nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = r.Add(tt.entry)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Add(%+v) = %v, want %v", tt.entry, err, tt.want)
			}
			if len(r.All()) != 0 {
				t.Errorf("registry mutated after refusal: %+v", r.All())
			}
		})
	}
}

func TestAddRejectsDuplicate(t *testing.T) {
	r, err := New([]Host{host("m4")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Add(host("m4")); !errors.Is(err, ErrDuplicateHost) {
		t.Fatalf("Add duplicate = %v, want ErrDuplicateHost", err)
	}
}

func TestAddAcceptsValidEntry(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	entry := Host{
		Name:       "m4",
		SSH:        "u@m4.local",
		User:       "jesse",
		EvenerPath: "/usr/local/bin/evener",
		Roots:      []string{"/Users/jesse/src"},
	}
	// user is empty here, so u@m4.local is fine.
	entry.User = ""
	if err := r.Add(entry); err != nil {
		t.Fatalf("Add valid: %v", err)
	}
	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("Get(m4) missing after Add")
	}
	if got.SSH != entry.SSH || got.EvenerPath != entry.EvenerPath || len(got.Roots) != 1 {
		t.Fatalf("Get(m4) = %+v, want %+v", got, entry)
	}
}

func TestAddRejectsSelfEdge(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = r.AddWithUpstreams(host("m4"), []string{"m4"})
	if !errors.Is(err, ErrHostCycle) {
		t.Fatalf("self-edge = %v, want ErrHostCycle", err)
	}
	if _, ok := r.Get("m4"); ok {
		t.Error("self-edge candidate was inserted despite refusal")
	}
}

func TestAddRejectsTwoNodeCycle(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// b is upstream of a: a's upstream is b.
	if err := r.AddWithUpstreams(host("a"), []string{"b"}); err != nil {
		t.Fatalf("add a: %v", err)
	}
	// Adding b with upstream a closes a -> b -> a.
	err = r.AddWithUpstreams(host("b"), []string{"a"})
	if !errors.Is(err, ErrHostCycle) {
		t.Fatalf("two-node cycle = %v, want ErrHostCycle", err)
	}
	if _, ok := r.Get("b"); ok {
		t.Error("cycle candidate b was inserted despite refusal")
	}
	if got := r.All(); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("registry changed after refusal: %+v", got)
	}
}

func TestAddRejectsLongerCycle(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.AddWithUpstreams(host("a"), []string{"b"}); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := r.AddWithUpstreams(host("b"), []string{"c"}); err != nil {
		t.Fatalf("add b: %v", err)
	}
	err = r.AddWithUpstreams(host("c"), []string{"a"})
	if !errors.Is(err, ErrHostCycle) {
		t.Fatalf("three-node cycle = %v, want ErrHostCycle", err)
	}
}

func TestAllIsNameSorted(t *testing.T) {
	r, err := New([]Host{host("zeta"), host("alpha"), host("m4"), host("Beta")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := r.All()
	want := []string{"Beta", "alpha", "m4", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("All() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("All()[%d].Name = %q, want %q (full: %+v)", i, got[i].Name, want[i], got)
		}
	}
}

func TestRegistryConcurrentGetAll(t *testing.T) {
	r, err := New([]Host{host("m4"), host("studio"), host("lab")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				r.Get("m4")
				r.All()
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = r.Add(host("dynamic"))
			}
		}()
	}
	wg.Wait()
}

// TestNameAcceptanceMatchesRefGrammar pins hostreg's name acceptance to the
// exported appwire ref grammar: a name is accepted exactly when
// appwire.ValidRefPart accepts it, minus the stricter "." / ".." and "local" rules
// this package adds on purpose. It also cross-checks ParseRef so the grammar
// cannot drift behind the exported helper.
func TestNameAcceptanceMatchesRefGrammar(t *testing.T) {
	corpus := []string{
		"m4", "m4.local", "studio", "a-b", "a_b", "a~b", "a.b", "AB12", "0",
		"local", "..", "a..b", "a..", "..a", ".", "-", "_", "~", "a/b", "a b",
		"a:b", "", "a\nb", "café",
	}
	for _, name := range corpus {
		_, parseErr := appwire.ParseRef(name + ":x")
		want := appwire.ValidRefPart(name) &&
			!strings.Contains(name, "..") &&
			name != "." &&
			name != ReservedName
		// ParseRef must agree with the exported helper on this corpus.
		if got := parseErr == nil; got != appwire.ValidRefPart(name) {
			t.Errorf("ParseRef(%q:x) ok=%v disagrees with ValidRefPart=%v", name, got, appwire.ValidRefPart(name))
		}
		got := ValidateName(name) == nil
		if got != want {
			t.Errorf("ValidateName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A config-loaded host is registered by New, so its upstream edges have to come
// from SetUpstreams: AddWithUpstreams inserts, and would refuse the duplicate.
func TestSetUpstreamsAttachesEdgesToConfigLoadedHost(t *testing.T) {
	r, err := New([]Host{host("a"), host("b")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.AddWithUpstreams(host("b"), []string{"a"}); !errors.Is(err, ErrDuplicateHost) {
		t.Fatalf("AddWithUpstreams on a registered host = %v, want ErrDuplicateHost", err)
	}
	if err := r.SetUpstreams("b", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams: %v", err)
	}
	// The edge is live, so closing a cycle through it is refused.
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("SetUpstreams closing a cycle = %v, want ErrHostCycle", err)
	}
}

func TestSetUpstreamsUnknownHost(t *testing.T) {
	r, err := New([]Host{host("a")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetUpstreams("nope", []string{"a"}); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("SetUpstreams(unknown) = %v, want ErrUnknownHost", err)
	}
}

// A refused SetUpstreams must leave the recorded edges alone. Had the refusal
// left a->b behind, re-attaching b->a below would now look like a cycle.
func TestSetUpstreamsRefusalLeavesEdgesUnchanged(t *testing.T) {
	r, err := New([]Host{host("a"), host("b")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetUpstreams("b", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams(b, a): %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("SetUpstreams(a, b) = %v, want ErrHostCycle", err)
	}
	if err := r.SetUpstreams("b", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams(b, a) after the refusal = %v, want success", err)
	}
}

// Upstream names are normalized like everything else: a padded spelling stored
// verbatim would be a key distinct from its trimmed form, so the edge would look
// like a leaf and a cycle through it would go undetected.
func TestUpstreamNamesAreNormalized(t *testing.T) {
	r, err := New([]Host{host("a"), host("b")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetUpstreams("b", []string{"  a  "}); err != nil {
		t.Fatalf("SetUpstreams: %v", err)
	}
	// The edge is live under the trimmed name, so closing the cycle is refused.
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("cycle through a padded upstream = %v, want ErrHostCycle", err)
	}
}

func TestUpstreamNamesRejectBlank(t *testing.T) {
	r, err := New([]Host{host("a")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetUpstreams("a", []string{"   "}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("SetUpstreams with a blank upstream = %v, want ErrInvalidName", err)
	}
}

func TestUpstreamNamesRejectBadGrammar(t *testing.T) {
	r, err := New([]Host{host("a")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// An edge that could never name a registered host would sit forever as an
	// unresolvable leaf, so the grammar is checked up front.
	for _, upstream := range []string{"a/b", "a:b", "..", "."} {
		if err := r.SetUpstreams("a", []string{upstream}); !errors.Is(err, ErrInvalidName) {
			t.Errorf("SetUpstreams(%q) = %v, want ErrInvalidName", upstream, err)
		}
	}
}

// Lookup names are normalized like every other name: a padded spelling must find
// its host rather than failing as unknown and skipping the cycle check.
func TestPaddedLookupNamesFindTheirHost(t *testing.T) {
	r, err := New([]Host{host("a"), host("b")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := r.Get("  a  "); !ok {
		t.Error("Get with a padded name missed the host")
	}
	if err := r.SetUpstreams("  b  ", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams with a padded name: %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("cycle through the padded target = %v, want ErrHostCycle", err)
	}
}

// An upstream the registry has no entry for is a leaf: nothing beyond it can be
// traversed, which is the documented v1 limit on cross-hub cycle detection.
func TestUnknownUpstreamIsALeaf(t *testing.T) {
	r, err := New([]Host{host("a")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetUpstreams("a", []string{"lives-on-another-hub"}); err != nil {
		t.Fatalf("SetUpstreams through an unknown upstream = %v, want success", err)
	}
}

// The registry must not alias a caller's Roots array: mutating it after Add
// would reach registry state outside the lock, and a returned Host must not
// alias it either.
func TestRootsAreNotAliased(t *testing.T) {
	roots := []string{"/srv/a"}
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Add(Host{Name: "m4", SSH: "m4.local", Roots: roots}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	roots[0] = "/srv/HIJACKED"

	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("Get(m4) missing")
	}
	if got.Roots[0] != "/srv/a" {
		t.Fatalf("Get roots = %v, want the registry's own copy", got.Roots)
	}
	got.Roots[0] = "/srv/HIJACKED-AGAIN"
	if again, _ := r.Get("m4"); again.Roots[0] != "/srv/a" {
		t.Fatalf("roots after mutating a returned copy = %v, want the registry's own copy", again.Roots)
	}
	if all := r.All(); all[0].Roots[0] != "/srv/a" {
		t.Fatalf("All roots = %v, want the registry's own copy", all[0].Roots)
	}
}

// Validation trims, so storage must store the trimmed value: otherwise the
// registry accepts a host and then hands consumers a value they cannot resolve.
func TestValuesAreStoredTrimmed(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Add(Host{Name: "m4", SSH: "  m4.local  ", User: "  jesse  ", EvenerPath: "  /usr/local/bin/evener  ", Roots: []string{"  /srv/a  "}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("Get(m4) missing")
	}
	if got.SSH != "m4.local" || got.User != "jesse" || got.EvenerPath != "/usr/local/bin/evener" || got.Roots[0] != "/srv/a" {
		t.Fatalf("stored host = %+v, want trimmed values", got)
	}
}
