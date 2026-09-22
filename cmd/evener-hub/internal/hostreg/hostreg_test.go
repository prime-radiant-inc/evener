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

// Equal is the identity the attach rechecks compare, so it must be content
// equality: same fields and same roots, regardless of each slice's backing.
func TestHostEqualIsContentIdentity(t *testing.T) {
	base := Host{Name: "m4", SSH: "m4.local", Roots: []string{"/a", "/b"}}
	same := Host{Name: "m4", SSH: "m4.local", Roots: []string{"/a", "/b"}}
	if !base.Equal(same) {
		t.Fatal("Equal refused an identical entry")
	}
	if base.Equal(host("m4")) {
		t.Fatal("Equal accepted an entry with roots the other lacks")
	}
	if base.Equal(host("studio")) {
		t.Fatal("Equal accepted a different name")
	}
	changed := base
	changed.KeyPath = "/keys/other"
	if base.Equal(changed) {
		t.Fatal("Equal accepted a different key path")
	}
	changed = base
	changed.Roots = []string{"/b", "/a"}
	if base.Equal(changed) {
		t.Fatal("Equal accepted reordered roots")
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

func TestRemoveUnknownHost(t *testing.T) {
	r, err := New([]Host{host("m4")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Remove("nope"); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("Remove(unknown) = %v, want ErrUnknownHost", err)
	}
	// The refusal must not disturb the registered host.
	if _, ok := r.Get("m4"); !ok {
		t.Error("Get(m4) missing after a refused Remove")
	}
}

func TestRemoveRejectsInvalidNames(t *testing.T) {
	tests := []struct {
		name   string
		remove string
		want   error
	}{
		{"blank", "   ", ErrInvalidName},
		{"empty", "", ErrInvalidName},
		{"bad charset slash", "a/b", ErrInvalidName},
		{"dotdot inside", "a..b", ErrInvalidName},
		{"dot alone", ".", ErrInvalidName},
		{"reserved local", ReservedName, ErrReservedName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New([]Host{host("m4")})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := r.Remove(tt.remove); !errors.Is(err, tt.want) {
				t.Fatalf("Remove(%q) = %v, want %v", tt.remove, err, tt.want)
			}
			if _, ok := r.Get("m4"); !ok {
				t.Error("Get(m4) missing after a refused Remove")
			}
		})
	}
}

// Remove deletes the host: Get misses and All no longer lists it. The padded
// spelling must remove the host too, matching the trimmed lookups elsewhere.
func TestRemoveDeletesHost(t *testing.T) {
	r, err := New([]Host{host("m4"), host("studio")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Remove("  m4  "); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := r.Get("m4"); ok {
		t.Error("Get(m4) present after Remove")
	}
	if _, ok := r.Get("  m4  "); ok {
		t.Error("Get with a padded name present after Remove")
	}
	got := r.All()
	if len(got) != 1 || got[0].Name != "studio" {
		t.Errorf("All() after Remove = %+v, want only studio", got)
	}
	// Removing twice reports unknown: the host stays removed, it is not
	// resurrected and the second call is not a silent success.
	if err := r.Remove("m4"); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("second Remove = %v, want ErrUnknownHost", err)
	}
}

// A later Add of the same name starts clean: it succeeds with a different SSH
// value, proving no stale host state survived the removal.
func TestRemoveThenReAddStartsClean(t *testing.T) {
	r, err := New([]Host{host("m4")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Remove("m4"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	readded := Host{Name: "m4", SSH: "m4-new.local"}
	if err := r.Add(readded); err != nil {
		t.Fatalf("Add after Remove: %v", err)
	}
	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("Get(m4) missing after re-Add")
	}
	if got.SSH != "m4-new.local" {
		t.Fatalf("Get(m4).SSH = %q, want %q", got.SSH, "m4-new.local")
	}
}

// A remove and byte-identical re-add of a name is a new registry entry: the
// registry-wide generation advances across the cycle even though every configured
// field — the content Equal compares — is unchanged. That Equal passes between
// the two is exactly why an identity recheck must compare the generation
// alongside it: content equality alone cannot tell them apart (the round-3
// identity race behind sshconn's attach rechecks).
func TestRemoveReaddIdenticalAdvancesGeneration(t *testing.T) {
	r, err := New([]Host{host("m4")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, ok := r.Get("m4")
	if !ok || first.Generation != 1 {
		t.Fatalf("Get(m4) = %+v, %v; want the first insert's generation 1", first, ok)
	}
	if err := r.Remove("m4"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// The re-add is the byte-identical literal the registry stored.
	if err := r.Add(host("m4")); err != nil {
		t.Fatalf("Add after Remove: %v", err)
	}
	second, ok := r.Get("m4")
	if !ok {
		t.Fatal("Get(m4) missing after the re-add")
	}
	if !first.Equal(second) {
		t.Fatalf("Equal refused byte-identical entries: %+v vs %+v", first, second)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("re-added generation = %d, want greater than the removed entry's %d", second.Generation, first.Generation)
	}
}

// TestGenerationIsRegistryWideMonotonic pins the round-4 L1 design: the
// registry assigns generations from ONE registry-wide monotonic counter, not
// a per-name count that survives removals. A per-name counter map grows one
// tombstone per name ever added — unbounded growth under churn — and pruning
// it cannot stay safe: dropping a name's count lets a re-add reuse the
// generation a byte-identical re-add must not match. Registry-wide assignment
// costs nothing semantically, because every Host.Generation consumer compares
// a captured entry with the live entry of the SAME name (sshconn's attach
// rechecks): a parked Ensure reads only its own host's entry, so an unrelated
// host's add/remove never changes what it compares against, and a remove/
// re-add of any name still never reuses a generation — the counter only
// advances.
func TestGenerationIsRegistryWideMonotonic(t *testing.T) {
	r, err := New([]Host{host("a"), host("b")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a1, ok := r.Get("a")
	if !ok {
		t.Fatal("Get(a) missing after New")
	}
	b1, ok := r.Get("b")
	if !ok {
		t.Fatal("Get(b) missing after New")
	}
	// Registry-wide: every insert advances the one counter, so b's insert —
	// not b's name — carries the next generation. A per-name counter would
	// restart at 1 here.
	if b1.Generation <= a1.Generation {
		t.Fatalf("b's generation = %d, want greater than a's %d: the counter is registry-wide", b1.Generation, a1.Generation)
	}
	// Churn on unrelated names interleaved with a remove/re-add must not let
	// the re-added name reuse a generation: the byte-identical re-add is
	// still a different entry from the one Remove deleted.
	if err := r.Remove("a"); err != nil {
		t.Fatalf("Remove(a): %v", err)
	}
	if err := r.Add(host("c")); err != nil {
		t.Fatalf("Add(c): %v", err)
	}
	if err := r.Add(host("a")); err != nil {
		t.Fatalf("re-Add(a): %v", err)
	}
	a2, ok := r.Get("a")
	if !ok {
		t.Fatal("Get(a) missing after the re-add")
	}
	if !a1.Equal(a2) {
		t.Fatalf("Equal refused byte-identical entries: %+v vs %+v", a1, a2)
	}
	if a2.Generation <= a1.Generation {
		t.Fatalf("re-added generation = %d, want greater than the removed entry's %d", a2.Generation, a1.Generation)
	}
}

// Removing a dependent clears its edges, so re-adding it without upstreams
// succeeds instead of tripping the cycle check on a stale edge entry.
func TestRemoveClearsDependentEdges(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Add(host("a")); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := r.AddWithUpstreams(host("b"), []string{"a"}); err != nil {
		t.Fatalf("add b with upstream a: %v", err)
	}
	if err := r.Remove("b"); err != nil {
		t.Fatalf("Remove(b): %v", err)
	}
	if err := r.Add(host("b")); err != nil {
		t.Fatalf("re-Add b without upstreams: %v", err)
	}
	// The re-added host carries no edges, so it is a leaf again.
	if err := r.SetUpstreams("a", []string{"b"}); err != nil {
		t.Fatalf("SetUpstreams(a, b) after re-adding b edgeless: %v", err)
	}
}

// Removing an upstream does not stop it from being re-added, and the
// dependent's dangling edge still resolves once the upstream is back.
func TestRemoveUpstreamThenReAdd(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Add(host("a")); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := r.AddWithUpstreams(host("b"), []string{"a"}); err != nil {
		t.Fatalf("add b with upstream a: %v", err)
	}
	if err := r.Remove("a"); err != nil {
		t.Fatalf("Remove(a): %v", err)
	}
	if _, ok := r.Get("a"); ok {
		t.Error("Get(a) present after Remove")
	}
	if err := r.Add(host("a")); err != nil {
		t.Fatalf("re-Add a: %v", err)
	}
	got, ok := r.Get("a")
	if !ok || got.SSH != "a.local" {
		t.Fatalf("Get(a) after re-Add = %+v, %v, want the re-added host", got, ok)
	}
	// The dependent still points at a, so closing the loop back is refused.
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("SetUpstreams closing a cycle through the re-added upstream = %v, want ErrHostCycle", err)
	}
}
