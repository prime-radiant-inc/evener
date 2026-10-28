package hub

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// The logical-fingerprint walk reads every resource a build is about to
// publish; a context cancelled at any of its checks must fail the walk rather
// than publish a partial fingerprint set. As with the projection build sweep,
// the check counts are measured first so no flip point is hand-calibrated.
func TestNavigationLogicalFingerprintsSurfacesCancellationAtEveryCheck(t *testing.T) {
	t.Parallel()
	projection, err := buildNavigationProjection(navigationCancellationInputs())
	if err != nil {
		t.Fatal(err)
	}
	counting := &cancelAfterChecksContext{Context: context.Background(), limit: math.MaxInt32}
	if _, _, err := navigationLogicalFingerprintsContext(counting, projection); err != nil {
		t.Fatalf("uncancelled fingerprint walk: %v", err)
	}
	total := counting.checks.Load()
	if total < 1 {
		t.Fatal("fixture made no context checks; the sweep would prove nothing")
	}
	for limit := int32(1); limit <= total; limit++ {
		_, _, err := navigationLogicalFingerprintsContext(&cancelAfterChecksContext{Context: context.Background(), limit: limit}, projection)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fingerprint walk cancelled at check %d of %d returned %v, want the cancellation surfaced", limit, total, err)
		}
	}
}

// The fingerprint encoder is a structural equality hash over arbitrary
// values: every Go shape a resource can carry must encode deterministically,
// and the shapes it cannot carry must be refused, not hashed ambiguously.
func TestNavigationFingerprintEncodesEveryValueShape(t *testing.T) {
	t.Parallel()
	type shapes struct {
		When      time.Time
		Nothing   any
		Something any
		Ptr       *int
		Names     []string
		Counts    map[string]int
		Flag      bool
		Small     int8
		Unsigned  uint32
		Half      float32
		Double    float64
	}
	answer := 42
	first := shapes{
		When: time.Unix(1_700_000_000, 0).UTC(), Nothing: nil, Something: "shared",
		Ptr: &answer, Names: []string{"a", "b"}, Counts: map[string]int{"a": 1, "b": 2},
		Flag: true, Small: -3, Unsigned: 7, Half: 0.5, Double: 1.25,
	}
	same := first
	same.Counts = map[string]int{"b": 2, "a": 1}
	firstFingerprint, err := navigationLogicalFingerprintContext(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	sameFingerprint, err := navigationLogicalFingerprintContext(context.Background(), same)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != sameFingerprint {
		t.Fatal("equal values with map keys in a different insertion order hashed differently")
	}
	same.Names = []string{"a", "c"}
	other, err := navigationLogicalFingerprintContext(context.Background(), same)
	if err != nil {
		t.Fatal(err)
	}
	if other == firstFingerprint {
		t.Fatal("different values hashed equal")
	}
	// A nil value encodes as its own shape instead of failing the walk.
	if _, err := navigationLogicalFingerprintContext(context.Background(), nil); err != nil {
		t.Fatalf("nil value: %v", err)
	}
	if _, err := navigationLogicalFingerprintContext(context.Background(), map[int]int{1: 2}); err == nil {
		t.Fatal("non-string map keys were hashed instead of refused")
	}
	if _, err := navigationLogicalFingerprintContext(context.Background(), make(chan int)); err == nil {
		t.Fatal("an unsupported kind was hashed instead of refused")
	}
}

// The next-state fold is where a build's fingerprints become revisions. A
// cancelled fold must fail, and a resource already at the revision ceiling
// must refuse to advance instead of wrapping past the JavaScript safe
// integer.
func TestNavigationNextStatesSurfacesCancellationAndRevisionCeiling(t *testing.T) {
	t.Parallel()
	live := navigationResourceKey{Kind: navigationResourceLive}
	needs := navigationResourceKey{Kind: navigationResourceNeedsYou}
	previous := map[navigationResourceKey]navigationResourceState{
		live:  {Revision: 4, Present: true},
		needs: {Revision: 2, Present: true},
	}
	fingerprints := map[navigationResourceKey]navigationFingerprint{
		live: {1},
	}
	counting := &cancelAfterChecksContext{Context: context.Background(), limit: math.MaxInt32}
	if _, _, err := navigationNextStatesContext(counting, previous, fingerprints, nil, nil, nil, 0); err != nil {
		t.Fatalf("uncancelled fold: %v", err)
	}
	total := counting.checks.Load()
	for limit := int32(1); limit <= total; limit++ {
		if _, _, err := navigationNextStatesContext(&cancelAfterChecksContext{Context: context.Background(), limit: limit}, previous, fingerprints, nil, nil, nil, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("fold cancelled at check %d of %d returned %v, want the cancellation surfaced", limit, total, err)
		}
	}
	atCeiling := map[navigationResourceKey]navigationResourceState{
		live: {Revision: maxNavigationSafeInteger, Present: true, Fingerprint: navigationFingerprint{9}},
	}
	changed := map[navigationResourceKey]navigationFingerprint{live: {1}}
	if _, _, err := navigationNextStatesContext(context.Background(), atCeiling, changed, nil, nil, nil, 0); err == nil {
		t.Fatal("a resource at the revision ceiling advanced past the safe integer")
	}
}

// Committing invalidation targets folds a change set into the distinct set of
// targets its dependencies address: unchanged keys contribute nothing, a
// dependency the state lost falls back to the changing key's own revision,
// and a dependency that addresses no target kind (a location) is skipped. At
// the sequence ceiling the commit refuses rather than wrapping.
func TestNavigationCommitTargetsFoldsDependenciesAndRefusesPastTheSequenceCeiling(t *testing.T) {
	t.Parallel()
	changed := navigationResourceKey{Kind: navigationResourceLive}
	unchanged := navigationResourceKey{Kind: navigationResourceNeedsYou}
	manifestDependency := navigationResourceKey{Kind: navigationResourceManifest}
	missingDependency := navigationResourceKey{Kind: navigationResourceProjects}
	locationDependency := navigationResourceKey{Kind: navigationResourceLocation}
	service := &NavigationService{
		generation: "generation",
		resources: map[navigationResourceKey]navigationResourceState{
			changed:            {Revision: 1, Present: true, Dependencies: []navigationResourceKey{manifestDependency, missingDependency, locationDependency}},
			manifestDependency: {Revision: 3, Present: true},
			locationDependency: {Revision: 5, Present: true},
		},
	}
	service.mu.Lock()
	mutation, err := service.commitTargetsLocked(map[navigationResourceKey]bool{changed: true, unchanged: false}, navigationChangeHint{AllLoadedProjects: true})
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if mutation.GenerationID != "generation" {
		t.Fatalf("mutation generation = %q", mutation.GenerationID)
	}
	kinds := map[appwire.NavigationTargetKind]bool{}
	revisions := map[appwire.NavigationTargetKind]uint64{}
	for _, target := range mutation.Targets {
		kinds[target.Kind] = true
		revisions[target.Kind] = target.Revision
	}
	if !kinds[appwire.NavigationTargetManifest] {
		t.Errorf("targets = %+v, want the manifest dependency's target", mutation.Targets)
	}
	if !kinds[appwire.NavigationTargetCatalog] || revisions[appwire.NavigationTargetCatalog] != 1 {
		// The projects dependency carries no state of its own, so its target
		// must fall back to the changing key's revision rather than invent one.
		t.Errorf("targets = %+v, want the stateless projects dependency folded at the changing key's revision 1", mutation.Targets)
	}
	if !kinds[appwire.NavigationTargetAllLoadedProjects] {
		t.Errorf("targets = %+v, want the all-loaded-projects hint target", mutation.Targets)
	}
	if len(mutation.Targets) != 3 {
		t.Errorf("targets = %+v, want exactly the manifest, fallback catalog and hint targets: locations address no target", mutation.Targets)
	}
	if service.sequence != 1 {
		t.Errorf("sequence = %d, want one advance", service.sequence)
	}
	// The location kind addresses no invalidation target at all.
	if _, ok := navigationTargetForResource(locationDependency, 1); ok {
		t.Error("a location produced an invalidation target")
	}
	// At the ceiling the commit must refuse instead of wrapping.
	service.mu.Lock()
	service.sequence = maxNavigationSafeInteger
	_, err = service.commitTargetsLocked(map[navigationResourceKey]bool{changed: true}, navigationChangeHint{})
	service.mu.Unlock()
	if err == nil {
		t.Fatal("a commit at the sequence ceiling succeeded")
	}
}

// Refresh tickets are completed exactly once: a ticket another flight already
// answered is skipped, so joining callers never see a double close or a
// replayed outcome.
func TestCompleteRefreshTicketsSkipsAlreadyCompletedTickets(t *testing.T) {
	t.Parallel()
	flight := &navigationBuildFlight{}
	done := &navigationRefreshTicket{done: make(chan struct{})}
	answered := &navigationRefreshTicket{done: make(chan struct{}), completed: true, outcome: hubapi.NavigationMutation{GenerationID: "first"}}
	flight.tickets = []*navigationRefreshTicket{answered, done}
	service := &NavigationService{}
	outcome := hubapi.NavigationMutation{GenerationID: "second"}
	service.completeRefreshTicketsLocked(flight, outcome, nil)
	select {
	case <-done.done:
	default:
		t.Fatal("the fresh ticket was not completed")
	}
	if done.outcome.GenerationID != "second" {
		t.Fatalf("fresh ticket outcome = %+v, want the flight's", done.outcome)
	}
	if answered.outcome.GenerationID != "first" {
		t.Fatalf("completed ticket outcome = %+v, want its earlier answer preserved", answered.outcome)
	}
}

// The availability and not-found errors are the wire contract's status codes:
// 503 for a navigation surface that cannot be built right now, 404 for a
// resource the generation never knew.
func TestNavigationAvailabilityAndNotFoundErrorsCarryStatusCodes(t *testing.T) {
	t.Parallel()
	unavailable := navigationUnavailable(context.Canceled)
	if !errors.Is(unavailable, context.Canceled) {
		t.Fatal("availability error does not unwrap to its cause")
	}
	if coder, ok := unavailable.(interface{ StatusCode() int }); !ok || coder.StatusCode() != 503 {
		t.Fatalf("availability status = %v, want 503", unavailable)
	}
	notFound := navigationNotFoundError{known: true}
	if coder, ok := any(notFound).(interface{ StatusCode() int }); !ok || coder.StatusCode() != 404 {
		t.Fatalf("not-found status = %v, want 404", notFound)
	}
}

// A web navigation source with no server behind it is a stub that must report
// a still revision and refuse to capture, never panic: the hub runs one
// service per web server and the zero value is the failure shape.
func TestWebNavigationSourceWithoutServerRefusesToCapture(t *testing.T) {
	t.Parallel()
	source := webNavigationSource{}
	if got := source.Revision(); got != (navigationSourceRevision{}) {
		t.Fatalf("revision = %+v, want the zero revision", got)
	}
	if _, err := source.Capture(context.Background(), "generation", time.Unix(0, 0).UTC()); err == nil {
		t.Fatal("a source with no server captured a snapshot")
	}
}

// unavailableNavigationSource answers the source interface for tests that
// need a source which never succeeds.
type unavailableNavigationSource struct{}

func (unavailableNavigationSource) Revision() navigationSourceRevision {
	return navigationSourceRevision{Inputs: 1}
}

func (unavailableNavigationSource) Capture(context.Context, string, time.Time) (navigationSourceSnapshot, error) {
	return navigationSourceSnapshot{}, errors.New("capture unavailable")
}

// The service refuses reads before it ever reaches a build: no context, no
// source, and a stopped lifecycle each answer an error, and a lifecycle that
// has been stopped reports the cancellation that stopped it.
func TestNavigationServiceRefusesReadsWithoutSourceContextOrLifecycle(t *testing.T) {
	t.Parallel()
	service := newNavigationService(navigationServiceConfig{})
	if _, err := service.ensureSnapshot(nil, false, nil); err == nil {
		t.Error("a nil context was accepted")
	}
	if _, err := service.ensureSnapshot(context.Background(), false, nil); err == nil {
		t.Error("a missing source was accepted")
	}
	stopped := newNavigationService(navigationServiceConfig{Source: unavailableNavigationSource{}})
	stopped.mu.Lock()
	stopped.lifecycleStopped = true
	stopped.mu.Unlock()
	_, err := stopped.ensureSnapshot(context.Background(), false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped lifecycle error = %v, want the stopping cancellation", err)
	}
}

// The generation id is the ETag root: a generation function that hands back a
// value the ETag grammar refuses must disable the capability rather than
// serve an unstable navigation surface.
func TestNavigationServiceDisablesCapabilityForInvalidGeneration(t *testing.T) {
	t.Parallel()
	service := newNavigationService(navigationServiceConfig{Generation: func() (string, error) {
		return "not an ETag generation", nil
	}})
	if capability := service.Capability(); capability != nil {
		t.Fatalf("capability = %+v, want no navigation surface for an invalid generation", capability)
	}
	if _, err := service.ensureSnapshot(context.Background(), false, nil); err == nil {
		t.Error("a read was served under an invalid generation")
	}
}
