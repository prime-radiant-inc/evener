package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"reflect"
	"sort"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// JavaScript transports revisions as numbers. Never publish a value that cannot
// round-trip through Number.MAX_SAFE_INTEGER.
const maxNavigationSafeInteger uint64 = 9_007_199_254_740_991

// Named service defaults make the retained-cache and independent-build bounds
// observable rather than hiding protocol behavior in constructor literals.
const (
	defaultNavigationCacheEntries = 256
	defaultNavigationCacheBytes   = int64(64 << 20)
	defaultNavigationBuildTimeout = 15 * time.Second
	defaultNavigationRetryAfter   = 5 * time.Second
)

// buildNavigationServiceProjectionContext is a narrow test seam proving
// resource misses reuse the retained core projection rather than rebuilding it.
var buildNavigationServiceProjectionContext = buildNavigationProjectionContext

// These narrow seams make cancellation at the publication linearization point
// and during a large string fingerprint deterministic in tests. Production
// behavior is only the context check shown here.
var navigationBeforeSnapshotCommit = func(context.Context) {}

var navigationFingerprintStringChunkContext = func(ctx context.Context, _ string) error {
	return ctx.Err()
}

var navigationPublicationCommittedLocked = func(*NavigationService) {}

// navigationRefreshTicketAttached is a deterministic test seam for observing
// the causal cutoff while the service lock is held. Production leaves it nil.
var navigationRefreshTicketAttached = func(*NavigationService, *navigationBuildFlight) {}

var navigationBeforePublicationDrainLock = func() {}

var navigationPendingCleared = func() {}

type navigationSourceRevision struct {
	Inputs uint64
	Remote uint64
}

type navigationSourceSnapshot struct {
	Inputs       navigationBuildInputs
	NextBoundary time.Time
}

type navigationSource interface {
	Revision() navigationSourceRevision
	Capture(context.Context, string, time.Time) (navigationSourceSnapshot, error)
}

type navigationChangeHint struct {
	Projects          []string
	Sources           bool
	Time              bool
	AllLoadedProjects bool
}

type navigationServiceConfig struct {
	Source       navigationSource
	Generation   func() (string, error)
	Now          func() time.Time
	NewTimer     func(time.Duration) navigationTimer
	Cache        *navigationRepresentationCache
	BuildTimeout time.Duration
	RetryAfter   time.Duration
}

type navigationTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realNavigationTimer struct{ timer *time.Timer }

func (t realNavigationTimer) C() <-chan time.Time { return t.timer.C }
func (t realNavigationTimer) Stop() bool          { return t.timer.Stop() }

type navigationResourceState struct {
	Revision     uint64
	Fingerprint  navigationFingerprint
	Present      bool
	Dependencies []navigationResourceKey // semantic target-bearing keys
}

type navigationCoreSnapshot struct {
	projection   navigationProjection // immutable deep snapshot; never source aliases
	source       navigationSourceRevision
	epoch        uint64
	nextBoundary time.Time
}

type navigationBuildFlight struct {
	done      chan struct{}
	changes   map[navigationResourceKey]bool
	err       error
	id        uint64
	hint      navigationChangeHint
	mutated   bool
	mutation  hubapi.NavigationMutation
	finalized bool
	tickets   []*navigationRefreshTicket
}

type navigationRefreshTicket struct {
	done      chan struct{}
	outcome   hubapi.NavigationMutation
	err       error
	completed bool
}

type navigationServiceStats struct {
	CoreBuilds uint64
	Cache      navigationCacheStats
}

type NavigationServiceStats = navigationServiceStats
type NavigationResourceKey = navigationResourceKey
type NavigationRepresentation = navigationRepresentation

// NavigationService owns the coherent, revisioned navigation generation for a
// single hub. Its source capture and pure projection are separated so a changed
// source can never publish an incoherent projection under an older key.
type NavigationService struct {
	mu sync.Mutex

	source       navigationSource
	generation   string
	genErr       error
	now          func() time.Time
	newTimer     func(time.Duration) navigationTimer
	buildTimeout time.Duration
	retryAfter   time.Duration
	cache        *navigationRepresentationCache

	core                *navigationCoreSnapshot
	resources           map[navigationResourceKey]navigationResourceState // includes tombstones
	sequence            uint64
	epoch               uint64
	flight              *navigationBuildFlight
	buildID             uint64
	coreBuilds          uint64
	wake                chan struct{}
	lifecycleCtx        context.Context
	lifecycleCancel     context.CancelFunc
	lifecycleWG         sync.WaitGroup
	lifecycleStopped    bool
	publications        []appwire.NavigationInvalidatedPayload
	publicationReady    chan struct{}
	pendingHint         navigationChangeHint
	pendingInvalidation bool
	pendingEpoch        uint64
}

func newNavigationService(cfg navigationServiceConfig) *NavigationService {
	generation := cfg.Generation
	if generation == nil {
		generation = newNavigationGenerationID
	}
	id, err := generation()
	if err == nil && !validNavigationETagGeneration(id) {
		err = errors.New("navigation generation is invalid")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newTimer := cfg.NewTimer
	if newTimer == nil {
		newTimer = func(delay time.Duration) navigationTimer {
			return realNavigationTimer{timer: time.NewTimer(delay)}
		}
	}
	cache := cfg.Cache
	if cache == nil {
		cache = newNavigationRepresentationCache(defaultNavigationCacheEntries, defaultNavigationCacheBytes)
	}
	if cfg.BuildTimeout <= 0 {
		cfg.BuildTimeout = defaultNavigationBuildTimeout
	}
	if cfg.RetryAfter <= 0 {
		cfg.RetryAfter = defaultNavigationRetryAfter
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	return &NavigationService{
		source:           cfg.Source,
		generation:       id,
		genErr:           err,
		now:              now,
		newTimer:         newTimer,
		buildTimeout:     cfg.BuildTimeout,
		retryAfter:       cfg.RetryAfter,
		cache:            cache,
		resources:        make(map[navigationResourceKey]navigationResourceState),
		wake:             make(chan struct{}, 1),
		publicationReady: make(chan struct{}, 1),
		lifecycleCtx:     lifecycleCtx,
		lifecycleCancel:  lifecycleCancel,
	}
}

func newNavigationGenerationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("navigation generation: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Invalidate marks source-side state dirty and immediately resets a scheduler
// wait. Task 7 owns producer hooks; this method is intentionally idempotent.
func (s *NavigationService) Invalidate(hint navigationChangeHint) {
	s.mu.Lock()
	s.epoch++
	// Keep the hint until the scheduler consumes it.  In particular, a source
	// wake must result in a forced capture; a normal scheduler build is not a
	// publication operation.
	s.pendingHint = mergeNavigationChangeHints(s.pendingHint, hint)
	s.pendingInvalidation = true
	s.pendingEpoch++
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Capability is nil when generation construction failed. That omission is
// deliberate: clients must not receive a malformed/unstable navigation surface.
func (s *NavigationService) Capability() *appwire.NavigationCapability {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.genErr != nil {
		return nil
	}
	return &appwire.NavigationCapability{Version: 1, GenerationID: s.generation, Sequence: s.sequence}
}

// EmptyMutation returns the current navigation generation with no invalidation
// targets for an idempotent mutation that changed no durable state.
func (s *NavigationService) EmptyMutation() appwire.NavigationMutation {
	mutation := appwire.NavigationMutation{Targets: []appwire.NavigationInvalidationTarget{}}
	if capability := s.Capability(); capability != nil {
		mutation.GenerationID = capability.GenerationID
	}
	return mutation
}

func (s *NavigationService) Stats() NavigationServiceStats {
	s.mu.Lock()
	stats := navigationServiceStats{CoreBuilds: s.coreBuilds}
	s.mu.Unlock()
	stats.Cache = s.cache.Stats()
	return stats
}

// CurrentRevision is assertion-oriented. HTTP must pass a semantic, unversioned
// key directly to Representation, which captures its version and projection in
// one transaction; a VersionedKey then Representation sequence is racy.
func (s *NavigationService) CurrentRevision(key navigationResourceKey) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resources[key.Semantic()].Revision
}

// VersionedKey atomically obtains the current semantic resource version. It is
// paired internally with the immutable core projection selected by
// Representation, so a new projection's bytes cannot enter an old cache key.
func (s *NavigationService) VersionedKey(ctx context.Context, key navigationResourceKey) (NavigationResourceKey, error) {
	_, versioned, _, err := s.versionedCore(ctx, key)
	return versioned, err
}

func (s *NavigationService) versionedCore(ctx context.Context, key navigationResourceKey) (*navigationBuildFlight, navigationResourceKey, navigationProjection, error) {
	flight, err := s.ensureSnapshot(ctx, false, nil)
	if err != nil {
		return nil, navigationResourceKey{}, navigationProjection{}, err
	}
	semantic := key.Semantic()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.core == nil {
		return nil, navigationResourceKey{}, navigationProjection{}, errors.New("navigation core unavailable")
	}
	state, ok := s.resources[semantic]
	if !ok || !state.Present {
		return nil, navigationResourceKey{}, navigationProjection{}, navigationNotFoundError{kind: semantic.Kind}
	}
	versioned := key.canonical()
	versioned.Generation = s.generation
	versioned.Revision = state.Revision
	// navigationProjection retains only deep-cloned input and derived maps. A
	// value copy is enough to bind this request to the exact core selected above.
	return flight, versioned, s.core.projection, nil
}

// Representation captures a versioned key and its immutable core projection in
// one service transaction, then caches bytes only under that paired version.
func (s *NavigationService) Representation(ctx context.Context, key navigationResourceKey) (NavigationRepresentation, error) {
	_, versioned, projection, err := s.versionedCore(ctx, key)
	if err != nil {
		return navigationRepresentation{}, err
	}
	representation, err := s.cache.Get(ctx, versioned, func(context.Context) (navigationRepresentation, error) {
		object, _, err := projection.Resource(versioned)
		if err != nil {
			return navigationRepresentation{}, err
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return navigationRepresentation{}, fmt.Errorf("encode navigation representation: %w", err)
		}
		compressed, err := gzipNavigation(encoded)
		if err != nil {
			return navigationRepresentation{}, err
		}
		return navigationRepresentation{
			Object:       object,
			JSON:         encoded,
			Gzip:         compressed,
			Generation:   versioned.Generation,
			Revision:     versioned.Revision,
			SizeEstimate: int64(len(encoded) + len(compressed)),
		}, nil
	})
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return navigationRepresentation{}, navigationUnavailable(err)
	}
	return representation, err
}

func gzipNavigation(input []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(input); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Refresh always requests a new source capture, but all concurrent callers join
// its service-owned flight. Caller cancellation stops only that caller's wait.
func (s *NavigationService) Refresh(ctx context.Context, hint navigationChangeHint) (hubapi.NavigationMutation, error) {
	if s.sequenceAtSafeLimit() {
		return hubapi.NavigationMutation{}, errors.New("navigation sequence exceeds JavaScript safe integer")
	}
	ticket := &navigationRefreshTicket{done: make(chan struct{})}
	_, err := s.ensureSnapshot(ctx, true, []navigationChangeHint{hint}, ticket)
	if err != nil {
		return hubapi.NavigationMutation{}, err
	}
	select {
	case <-ctx.Done():
		return hubapi.NavigationMutation{}, navigationUnavailable(ctx.Err())
	case <-ticket.done:
		return ticket.outcome, ticket.err
	}
}

// PublicationReady is the sole notification that committed invalidations are
// available. The coalesced signal is level-like: DrainPublications clears it
// atomically with the FIFO, so a concurrent commit cannot be missed.
func (s *NavigationService) PublicationReady() <-chan struct{} {
	return s.publicationReady
}

// DrainPublications transfers publication authority to Task 7 without starting
// a build. Every committed payload is returned FIFO exactly once.
func (s *NavigationService) DrainPublications() []appwire.NavigationInvalidatedPayload {
	navigationBeforePublicationDrainLock()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]appwire.NavigationInvalidatedPayload(nil), s.publications...)
	s.publications = nil
	select {
	case <-s.publicationReady:
	default:
	}
	return out
}

func (s *NavigationService) sequenceAtSafeLimit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence >= maxNavigationSafeInteger
}

func (s *NavigationService) ensureSnapshot(ctx context.Context, force bool, hints []navigationChangeHint, tickets ...*navigationRefreshTicket) (*navigationBuildFlight, error) {
	if ctx == nil {
		return nil, errors.New("navigation: nil context")
	}
	if err := s.generationError(); err != nil {
		return nil, err
	}
	if s.source == nil {
		return nil, errors.New("navigation source is unavailable")
	}
	revision := s.source.Revision() // external call: never under s.mu
	s.mu.Lock()
	if s.lifecycleStopped {
		s.mu.Unlock()
		return nil, navigationUnavailable(context.Canceled)
	}
	if !force && s.core != nil && s.core.source == revision && s.core.epoch == s.epoch {
		flight := &navigationBuildFlight{changes: map[navigationResourceKey]bool{}, id: s.buildID, done: closedNavigationDone()}
		s.mu.Unlock()
		return flight, nil
	}
	if flight := s.flight; flight != nil && !flight.finalized {
		if len(tickets) != 0 {
			flight.tickets = append(flight.tickets, tickets[0])
			navigationRefreshTicketAttached(s, flight)
		}
		if force && len(hints) != 0 {
			flight.hint = mergeNavigationChangeHints(flight.hint, hints[0])
			flight.mutated = true
		}
		s.mu.Unlock()
		return s.waitFlight(ctx, flight)
	}
	flight := &navigationBuildFlight{done: make(chan struct{})}
	if len(tickets) != 0 {
		flight.tickets = append(flight.tickets, tickets[0])
		navigationRefreshTicketAttached(s, flight)
	}
	if force && len(hints) != 0 {
		flight.hint, flight.mutated = hints[0], true
	}
	s.flight = flight
	buildCtx, cancel := context.WithTimeout(s.lifecycleCtx, s.buildTimeout)
	s.lifecycleWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.lifecycleWG.Done()
		defer cancel()
		s.buildSnapshot(buildCtx, revision, flight)
	}()
	return s.waitFlight(ctx, flight)
}

func mergeNavigationChangeHints(a, b navigationChangeHint) navigationChangeHint {
	a.Projects = append(a.Projects, b.Projects...)
	a.Sources = a.Sources || b.Sources
	a.Time = a.Time || b.Time
	a.AllLoadedProjects = a.AllLoadedProjects || b.AllLoadedProjects
	return a
}

func closedNavigationDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (s *NavigationService) waitFlight(ctx context.Context, flight *navigationBuildFlight) (*navigationBuildFlight, error) {
	select {
	case <-ctx.Done():
		return nil, navigationUnavailable(ctx.Err())
	case <-flight.done:
		return flight, flight.err
	}
}

func (s *NavigationService) generationError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.genErr
}

func (s *NavigationService) stopLifecycle() {
	s.mu.Lock()
	if !s.lifecycleStopped {
		s.lifecycleStopped = true
		s.lifecycleCancel()
	}
	s.mu.Unlock()
	s.lifecycleWG.Wait()
}

func (s *NavigationService) buildSnapshot(ctx context.Context, expected navigationSourceRevision, flight *navigationBuildFlight) {
	defer func() {
		s.mu.Lock()
		if flight.err != nil {
			s.completeRefreshTicketsLocked(flight, hubapi.NavigationMutation{}, flight.err)
		}
		if s.flight == flight {
			s.flight = nil
		}
		close(flight.done)
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			flight.err = navigationUnavailable(err)
			return
		}
		s.mu.Lock()
		epoch := s.epoch
		generation := s.generation
		s.mu.Unlock()
		snapshot, err := s.source.Capture(ctx, generation, s.now())
		if err != nil {
			if ctx.Err() != nil {
				flight.err = navigationUnavailable(ctx.Err())
			} else {
				flight.err = err
			}
			return
		}
		inputs := snapshot.Inputs
		inputs.GenerationID = generation
		inputs.Revision = 0 // semantic fingerprints never include transport revision.
		projection, err := buildNavigationServiceProjectionContext(ctx, inputs)
		if err != nil {
			if ctx.Err() != nil {
				flight.err = navigationUnavailable(ctx.Err())
			} else {
				flight.err = err
			}
			return
		}
		fingerprints, dependencies, err := navigationLogicalFingerprintsContext(ctx, projection)
		if err != nil {
			if ctx.Err() != nil {
				flight.err = navigationUnavailable(ctx.Err())
			} else {
				flight.err = err
			}
			return
		}
		// Compare immediately before publication. Any source/Invalidate movement
		// rejects this full build and retries; publication never mixes a newer core
		// with an older resource version.
		after := s.source.Revision() // external call: never under s.mu
		s.mu.Lock()
		stale := ctx.Err() != nil || s.epoch != epoch || after != expected
		if stale {
			s.mu.Unlock()
			expected = after
			continue
		}
		changes, states, err := navigationNextStatesContext(ctx, s.resources, fingerprints, dependencies)
		if err != nil {
			s.mu.Unlock()
			if ctx.Err() != nil {
				flight.err = navigationUnavailable(ctx.Err())
			} else {
				flight.err = err
			}
			return
		}
		navigationBeforeSnapshotCommit(ctx)
		// This is the publication linearization point. State transition work
		// above is detached; after this check every resources/core/sequence/FIFO
		// mutation occurs atomically under s.mu.
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			flight.err = navigationUnavailable(err)
			return
		}
		s.resources = states
		s.buildID++
		flight.id = s.buildID
		flight.changes = changes
		s.core = &navigationCoreSnapshot{projection: projection, source: after, epoch: epoch, nextBoundary: snapshot.NextBoundary}
		s.coreBuilds++
		if flight.mutated {
			flight.mutation, err = s.commitTargetsLocked(changes, flight.hint)
			if err != nil {
				s.mu.Unlock()
				flight.err = err
				return
			}
			if len(flight.mutation.Targets) != 0 {
				s.publications = append(s.publications, appwire.NavigationInvalidatedPayload{
					GenerationID: flight.mutation.GenerationID,
					Sequence:     s.sequence,
					Targets:      append([]appwire.NavigationInvalidationTarget(nil), flight.mutation.Targets...),
				})
				// publicationReady is a nonblocking level token. Installing it in
				// the same critical section as the FIFO append prevents a drain
				// from observing the payload between append and token creation.
				select {
				case s.publicationReady <- struct{}{}:
				default:
				}
				navigationPublicationCommittedLocked(s)
			}
			s.completeRefreshTicketsLocked(flight, flight.mutation, nil)
		}
		flight.finalized = true
		woke := flight.mutated
		s.mu.Unlock()
		// Commit owns the scheduler wake, including canceled-waiter builds.
		if woke {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		return
	}
}

func (s *NavigationService) completeRefreshTicketsLocked(flight *navigationBuildFlight, outcome hubapi.NavigationMutation, err error) {
	for _, ticket := range flight.tickets {
		if ticket.completed {
			continue
		}
		ticket.outcome, ticket.err, ticket.completed = outcome, err, true
		close(ticket.done)
	}
}

func navigationNextStatesContext(ctx context.Context, previous map[navigationResourceKey]navigationResourceState, fingerprints map[navigationResourceKey]navigationFingerprint, dependencies map[navigationResourceKey][]navigationResourceKey) (map[navigationResourceKey]bool, map[navigationResourceKey]navigationResourceState, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	next := make(map[navigationResourceKey]navigationResourceState, len(previous)+len(fingerprints))
	changes := make(map[navigationResourceKey]bool)
	keys := make(map[navigationResourceKey]bool, len(previous)+len(fingerprints))
	for key := range previous {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		keys[key] = true
	}
	for key := range fingerprints {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		keys[key] = true
	}
	for key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		old, existed := previous[key]
		fingerprint, present := fingerprints[key]
		if !existed {
			next[key] = navigationResourceState{Revision: 1, Fingerprint: fingerprint, Present: present, Dependencies: cloneNavigationDependencies(dependencies[key])}
			changes[key] = true
			continue
		}
		changed := old.Present != present || (present && old.Fingerprint != fingerprint)
		if changed {
			if old.Revision >= maxNavigationSafeInteger {
				return nil, nil, errors.New("navigation resource revision exceeds JavaScript safe integer")
			}
			old.Revision++
			changes[key] = true
		}
		if present {
			old.Fingerprint = fingerprint
			old.Dependencies = cloneNavigationDependencies(dependencies[key])
		}
		old.Present = present
		next[key] = old
	}
	return changes, next, nil
}

func cloneNavigationDependencies(in []navigationResourceKey) []navigationResourceKey {
	return append([]navigationResourceKey(nil), in...)
}

func navigationLogicalFingerprintsWithContext(ctx context.Context, projection navigationProjection) (map[navigationResourceKey]navigationFingerprint, map[navigationResourceKey][]navigationResourceKey, error) {
	fingerprints := make(map[navigationResourceKey]navigationFingerprint)
	dependencies := make(map[navigationResourceKey][]navigationResourceKey)
	put := func(key navigationResourceKey, value any, deps ...navigationResourceKey) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fingerprint, err := navigationLogicalFingerprintContext(ctx, value)
		if err != nil {
			return err
		}
		key = key.Semantic()
		fingerprints[key] = fingerprint
		for _, dep := range deps {
			if err := ctx.Err(); err != nil {
				return err
			}
			dependencies[key] = append(dependencies[key], dep.Semantic())
		}
		return nil
	}
	manifest, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceManifest})
	if err != nil {
		return nil, nil, err
	}
	if err := put(navigationResourceKey{Kind: navigationResourceManifest}, manifest, navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		return nil, nil, err
	}
	live, err := navigationLogicalNodesContext(ctx, projection, projection.live)
	if err != nil {
		return nil, nil, err
	}
	if err := put(navigationResourceKey{Kind: navigationResourceLive}, live, navigationResourceKey{Kind: navigationResourceLive}); err != nil {
		return nil, nil, err
	}
	needsYou, err := navigationLogicalNodesContext(ctx, projection, projection.needsYou)
	if err != nil {
		return nil, nil, err
	}
	if err := put(navigationResourceKey{Kind: navigationResourceNeedsYou}, needsYou, navigationResourceKey{Kind: navigationResourceNeedsYou}); err != nil {
		return nil, nil, err
	}
	pinCatalog := make([]hubapi.NavigationPinSectionDescriptor, 0, len(projection.pinSections))
	for _, section := range projection.pinSections {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		pinCatalog = append(pinCatalog, hubapi.NavigationPinSectionDescriptor{ID: section.id, Name: section.name, Count: section.memberCount})
		key := navigationResourceKey{Kind: navigationResourcePinSection, SectionID: section.id}
		rows, err := navigationLogicalNodesContext(ctx, projection, section.rows)
		if err != nil {
			return nil, nil, err
		}
		if err := put(key, rows, key); err != nil {
			return nil, nil, err
		}
	}
	if err := put(navigationResourceKey{Kind: navigationResourcePinCatalog}, pinCatalog, navigationResourceKey{Kind: navigationResourcePinCatalog}); err != nil {
		return nil, nil, err
	}
	for _, kind := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns} {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		projects := projection.catalogs[kind]
		logical := make([]hubapi.NavigationProjectSummary, 0, len(projects))
		for _, project := range projects {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			logical = append(logical, projection.projectSummary(project))
		}
		key := navigationResourceKey{Kind: kind}
		if err := put(key, logical, key); err != nil {
			return nil, nil, err
		}
	}
	for projectKey, project := range projection.projects {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		logical := struct {
			Project  hubapi.NavigationProjectSummary
			Current  hubapi.NavigationArray[hubapi.NavigationSessionSummary]
			Recent   hubapi.NavigationArray[hubapi.NavigationSessionSummary]
			Archived hubapi.NavigationArray[hubapi.NavigationSessionSummary]
		}{Project: projection.projectSummary(project)}
		current, _ := project.TierRows("current")
		recent, _ := project.TierRows("recent")
		archived, _ := project.TierRows("archived")
		logical.Current, err = navigationLogicalNodesContext(ctx, projection, current)
		if err != nil {
			return nil, nil, err
		}
		logical.Recent, err = navigationLogicalNodesContext(ctx, projection, recent)
		if err != nil {
			return nil, nil, err
		}
		logical.Archived, err = navigationLogicalNodesContext(ctx, projection, archived)
		if err != nil {
			return nil, nil, err
		}
		key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: projectKey}
		if err := put(key, logical, key); err != nil {
			return nil, nil, err
		}
	}
	for ref, location := range projection.locations {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		key := navigationResourceKey{Kind: navigationResourceLocation, ID: ref}
		var dep navigationResourceKey
		if location.ProjectKey != "" {
			dep = navigationResourceKey{Kind: navigationResourceProject, ProjectKey: location.ProjectKey}
		} else if location.Tier == "live" || location.Tier == "needs_you" {
			dep = navigationResourceKey{Kind: navigationResourceKind(location.Tier)}
		}
		if err := put(key, location, dep); err != nil {
			return nil, nil, err
		}
	}
	return fingerprints, dependencies, nil
}

// navigationLogicalFingerprintsContext keeps a canceled build from publishing
// after expensive complete-resource traversal.
func navigationLogicalFingerprintsContext(ctx context.Context, projection navigationProjection) (map[navigationResourceKey]navigationFingerprint, map[navigationResourceKey][]navigationResourceKey, error) {
	return navigationLogicalFingerprintsWithContext(ctx, projection)
}

func navigationLogicalNodesContext(ctx context.Context, projection navigationProjection, rows []hubcore.TreeNode) (hubapi.NavigationArray[hubapi.NavigationSessionSummary], error) {
	out := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], len(rows))
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		out[index], err = navigationLogicalNodeContext(ctx, projection, row)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func navigationLogicalNodeContext(ctx context.Context, projection navigationProjection, row hubcore.TreeNode) (hubapi.NavigationSessionSummary, error) {
	if err := ctx.Err(); err != nil {
		return hubapi.NavigationSessionSummary{}, err
	}
	summary := navigationProjector{projection: projection}.projectShallow(row)
	children, err := navigationLogicalNodesContext(ctx, projection, row.Children)
	if err != nil {
		return hubapi.NavigationSessionSummary{}, err
	}
	summary.Children = children
	return summary, nil
}

const navigationFingerprintStringChunkSize = 4 << 10

var navigationTimeType = reflect.TypeFor[time.Time]()

func navigationLogicalFingerprintContext(ctx context.Context, value any) (navigationFingerprint, error) {
	digest := sha256.New()
	if err := writeNavigationFingerprintValue(ctx, digest, reflect.ValueOf(value)); err != nil {
		return navigationFingerprint{}, err
	}
	var fingerprint navigationFingerprint
	copy(fingerprint[:], digest.Sum(nil))
	return fingerprint, nil
}

// writeNavigationFingerprintValue is a deterministic structural encoding used
// only for logical equality. It checks cancellation at every scalar, collection
// element, struct field, pointer recursion, and bounded string chunk; no whole
// resource is ever marshaled into an intermediate byte slice.
func writeNavigationFingerprintValue(ctx context.Context, digest hash.Hash, value reflect.Value) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !value.IsValid() {
		writeNavigationFingerprintByte(digest, '0')
		return nil
	}
	if value.Type() == navigationTimeType {
		writeNavigationFingerprintByte(digest, 't')
		text, err := value.Interface().(time.Time).AppendText(nil)
		if err != nil {
			return fmt.Errorf("fingerprint navigation time: %w", err)
		}
		return writeNavigationFingerprintString(ctx, digest, string(text))
	}

	switch value.Kind() {
	case reflect.Interface:
		writeNavigationFingerprintByte(digest, 'i')
		if value.IsNil() {
			writeNavigationFingerprintByte(digest, '0')
			return nil
		}
		return writeNavigationFingerprintValue(ctx, digest, value.Elem())
	case reflect.Pointer:
		writeNavigationFingerprintByte(digest, 'p')
		if value.IsNil() {
			writeNavigationFingerprintByte(digest, '0')
			return nil
		}
		writeNavigationFingerprintByte(digest, '1')
		return writeNavigationFingerprintValue(ctx, digest, value.Elem())
	case reflect.Struct:
		writeNavigationFingerprintByte(digest, 's')
		writeNavigationFingerprintUint(digest, uint64(value.NumField()))
		for _, field := range value.Fields() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writeNavigationFingerprintValue(ctx, digest, field); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		writeNavigationFingerprintByte(digest, 'a')
		writeNavigationFingerprintUint(digest, uint64(value.Len()))
		for index := range value.Len() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writeNavigationFingerprintValue(ctx, digest, value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("fingerprint navigation value: unsupported map key %s", value.Type().Key())
		}
		writeNavigationFingerprintByte(digest, 'm')
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		writeNavigationFingerprintUint(digest, uint64(len(keys)))
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writeNavigationFingerprintString(ctx, digest, key.String()); err != nil {
				return err
			}
			if err := writeNavigationFingerprintValue(ctx, digest, value.MapIndex(key)); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		writeNavigationFingerprintByte(digest, 'q')
		return writeNavigationFingerprintString(ctx, digest, value.String())
	case reflect.Bool:
		writeNavigationFingerprintByte(digest, 'b')
		if value.Bool() {
			writeNavigationFingerprintByte(digest, '1')
		} else {
			writeNavigationFingerprintByte(digest, '0')
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		writeNavigationFingerprintByte(digest, 'n')
		writeNavigationFingerprintUint(digest, uint64(value.Int()))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		writeNavigationFingerprintByte(digest, 'u')
		writeNavigationFingerprintUint(digest, value.Uint())
		return nil
	case reflect.Float32:
		writeNavigationFingerprintByte(digest, 'f')
		writeNavigationFingerprintUint(digest, uint64(math.Float32bits(float32(value.Float()))))
		return nil
	case reflect.Float64:
		writeNavigationFingerprintByte(digest, 'd')
		writeNavigationFingerprintUint(digest, math.Float64bits(value.Float()))
		return nil
	default:
		return fmt.Errorf("fingerprint navigation value: unsupported %s", value.Kind())
	}
}

func writeNavigationFingerprintString(ctx context.Context, digest hash.Hash, value string) error {
	writeNavigationFingerprintUint(digest, uint64(len(value)))
	for offset := 0; offset < len(value); offset += navigationFingerprintStringChunkSize {
		end := min(offset+navigationFingerprintStringChunkSize, len(value))
		chunk := value[offset:end]
		if err := navigationFingerprintStringChunkContext(ctx, chunk); err != nil {
			return err
		}
		_, _ = digest.Write([]byte(chunk))
	}
	return ctx.Err()
}

func writeNavigationFingerprintByte(digest hash.Hash, value byte) {
	_, _ = digest.Write([]byte{value})
}

func writeNavigationFingerprintUint(digest hash.Hash, value uint64) {
	var encoded [10]byte
	length := binary.PutUvarint(encoded[:], value)
	_, _ = digest.Write(encoded[:length])
}

// commitFlightTargets makes a shared capture one logical mutation. Joined
// refresh calls must not replay its exact invalidations or advance sequence a
// second time after the first caller has committed them.
func (s *NavigationService) commitTargetsLocked(changes map[navigationResourceKey]bool, hint navigationChangeHint) (hubapi.NavigationMutation, error) {
	targetSet := make(map[string]appwire.NavigationInvalidationTarget)
	for key, changed := range changes {
		if !changed {
			continue
		}
		state := s.resources[key]
		for _, dependency := range state.Dependencies {
			dependencyState, exists := s.resources[dependency]
			if !exists {
				dependencyState = state
			}
			if target, ok := navigationTargetForResource(dependency, dependencyState.Revision); ok {
				targetSet[navigationTargetIdentity(target)] = target
			}
		}
	}
	if len(targetSet) == 0 {
		return hubapi.NavigationMutation{GenerationID: s.generation, Targets: hubapi.NavigationArray[appwire.NavigationInvalidationTarget]{}}, nil
	}
	targets := make(hubapi.NavigationArray[appwire.NavigationInvalidationTarget], 0, len(targetSet)+1)
	for _, target := range targetSet {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		return navigationTargetIdentity(targets[i]) < navigationTargetIdentity(targets[j])
	})
	if hint.AllLoadedProjects {
		targets = append(targets, appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetAllLoadedProjects})
	}
	if s.sequence >= maxNavigationSafeInteger {
		return hubapi.NavigationMutation{}, errors.New("navigation sequence exceeds JavaScript safe integer")
	}
	s.sequence++
	return hubapi.NavigationMutation{GenerationID: s.generation, Targets: targets}, nil
}

func navigationTargetIdentity(target appwire.NavigationInvalidationTarget) string {
	return string(target.Kind) + "\x00" + target.Section + "\x00" + target.SectionID + "\x00" + target.Catalog + "\x00" + target.ProjectKey
}

func navigationTargetForResource(key navigationResourceKey, revision uint64) (appwire.NavigationInvalidationTarget, bool) {
	switch key.Kind {
	case navigationResourceManifest:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetManifest, Revision: revision}, true
	case navigationResourceLive, navigationResourceNeedsYou:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetSection, Section: string(key.Kind), Revision: revision}, true
	case navigationResourcePinCatalog:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetPinCatalog, Revision: revision}, true
	case navigationResourcePinSection:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetPinSection, SectionID: key.SectionID, Revision: revision}, true
	case navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetCatalog, Catalog: string(key.Kind), Revision: revision}, true
	case navigationResourceProject:
		return appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetProject, ProjectKey: key.ProjectKey, Revision: revision}, true
	default:
		return appwire.NavigationInvalidationTarget{}, false
	}
}

// Start owns a resettable lifecycle scheduler. Empty snapshots and failures are
// retried; invalidation wakes an earlier-boundary wait immediately.
func (s *NavigationService) Start(ctx context.Context) {
	defer s.stopLifecycle()
	for {
		_, err := s.ensureSnapshot(ctx, false, nil)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !s.waitRetryOrWake(ctx) {
				return
			}
			s.refreshPending(ctx)
			continue
		}
		s.mu.Lock()
		boundary := time.Time{}
		if s.core != nil {
			boundary = s.core.nextBoundary
		}
		s.mu.Unlock()
		if boundary.IsZero() {
			if !s.waitRetryOrWake(ctx) {
				return
			}
			continue
		}
		elapsed, keepGoing := s.waitBoundaryOrWake(ctx, boundary)
		if !keepGoing {
			return
		}
		if elapsed {
			s.Invalidate(navigationChangeHint{Time: true})
			s.refreshPending(ctx)
		} else if s.hasPendingInvalidation() {
			s.refreshPending(ctx)
		}
	}
}

func (s *NavigationService) hasPendingInvalidation() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingInvalidation
}

func (s *NavigationService) refreshPending(ctx context.Context) {
	hint, epoch, ok := s.snapshotPendingHint()
	if !ok {
		return
	}
	if _, err := s.Refresh(ctx, hint); err != nil {
		retained := false
		s.mu.Lock()
		if s.pendingEpoch == epoch {
			s.pendingHint = mergeNavigationChangeHints(hint, s.pendingHint)
			s.pendingInvalidation = true
			retained = true
		}
		s.mu.Unlock()
		// A failed forced rebuild re-arms the pending invalidation but would
		// otherwise leave no wake pending: the Start loop's next non-forced
		// rebuild publishes nothing (the pending epoch survives it) and the
		// loop parks in waitBoundaryOrWake until the next snapshot boundary —
		// up to 24h. Re-send the wake token exactly as Invalidate does so the
		// retryAfter retry window governs the next attempt.
		if retained {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		return
	}
	s.mu.Lock()
	// Clear only the exact pending epoch consumed by this flight. A same-value
	// invalidation racing after commit still advances the epoch and remains
	// pending for a subsequent forced capture.
	if s.pendingEpoch == epoch {
		s.pendingHint = navigationChangeHint{}
		s.pendingInvalidation = false
		navigationPendingCleared()
	}
	s.mu.Unlock()
}

func (s *NavigationService) snapshotPendingHint() (navigationChangeHint, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingHint, s.pendingEpoch, s.pendingInvalidation
}

func (s *NavigationService) waitRetryOrWake(ctx context.Context) bool {
	timer := s.newTimer(s.retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C():
		return true
	}
}

func (s *NavigationService) waitBoundaryOrWake(ctx context.Context, boundary time.Time) (elapsed, keepGoing bool) {
	delay := boundary.Sub(s.now())
	delay = max(delay, 0)
	timer := s.newTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, false
	case <-s.wake:
		return false, true
	case <-timer.C():
		return true, true
	}
}

type navigationAvailabilityError struct{ err error }

func (e navigationAvailabilityError) Error() string   { return e.err.Error() }
func (e navigationAvailabilityError) Unwrap() error   { return e.err }
func (e navigationAvailabilityError) StatusCode() int { return 503 }
func navigationUnavailable(err error) error           { return navigationAvailabilityError{err: err} }

type navigationNotFoundError struct{ kind navigationResourceKind }

func (e navigationNotFoundError) Error() string   { return "navigation resource not found" }
func (e navigationNotFoundError) StatusCode() int { return 404 }

// Semantic removes representation-only pagination and version fields. One
// section/catalog/project revision governs every page of that logical resource.
func (key navigationResourceKey) Semantic() navigationResourceKey {
	key = key.canonical()
	key.Generation = ""
	key.Revision = 0
	switch key.Kind {
	case navigationResourceLive, navigationResourceNeedsYou, navigationResourcePinCatalog, navigationResourcePinSection,
		navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns:
		key.Offset, key.Limit = 0, 0
	case navigationResourceProjectPage:
		key.Kind, key.Tier, key.Offset, key.Limit = navigationResourceProject, "", 0, 0
	}
	return key
}

// webNavigationSource takes a fresh deep snapshot at the service clock. It
// deliberately bypasses the legacy 30-second TreeCache: tier classification and
// scheduler boundaries must agree exactly at 24h and 14d.
type webNavigationSource struct{ web *WebServer }

func (s webNavigationSource) Revision() navigationSourceRevision {
	if s.web == nil {
		return navigationSourceRevision{}
	}
	var revision navigationSourceRevision
	if s.web.cfg.Inputs != nil {
		revision.Inputs = s.web.cfg.Inputs.Load()
	}
	if s.web.cfg.RemoteThreadCache != nil {
		revision.Remote = s.web.cfg.RemoteThreadCache.Generation()
	}
	return revision
}

func (s webNavigationSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	if s.web == nil {
		return navigationSourceSnapshot{}, errors.New("navigation web source is unavailable")
	}
	snapshot := s.web.navigationSnapshot(ctx)
	decisions := s.web.archiveDecisions()
	// Keep the legacy adapter on the exact same seam as the established endpoint;
	// in particular, test and compatibility fixtures replace these functions.
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, decisions, snapshot.projects)
	_, attention := hubDeriveNavigationAttention(snapshot.metas, snapshot.live, decisions)
	authority := favoriteAuthorityForNavigation(snapshot, tree)
	favorites, err := s.web.favoriteDecisions()
	if err != nil {
		return navigationSourceSnapshot{}, err
	}
	assignments, err := s.web.pinSectionAssignments()
	if err != nil {
		return navigationSourceSnapshot{}, err
	}
	sections, err := s.web.pinSections()
	if err != nil {
		return navigationSourceSnapshot{}, err
	}
	favoriteView := hubcore.ClassifyFavoriteDecisions(favorites, authority).Presentation
	pinView := classifySessionPins(assignments, authority)
	assignments = canonicalPinAssignments(assignments, pinView)
	inputs := navigationBuildInputsFromTreeSnapshot(generation, 0, tree, s.web.apiTreeSources(), hubAttentionSummaryFromCore(attention), snapshot.live, favoriteView, projectFavoritePresentation(favoriteView), sections, assignments)
	return navigationSourceSnapshot{Inputs: inputs, NextBoundary: navigationSnapshotBoundary(tree, now)}, nil
}

func navigationSnapshotBoundary(tree hubcore.Tree, now time.Time) time.Time {
	var nearest time.Time
	consider := func(updated time.Time) {
		if updated.IsZero() {
			return
		}
		for _, age := range []time.Duration{24 * time.Hour, 14 * 24 * time.Hour} {
			boundary := updated.Add(age)
			if boundary.After(now) && (nearest.IsZero() || boundary.Before(nearest)) {
				nearest = boundary
			}
		}
	}
	var visit func([]hubcore.TreeNode)
	visit = func(rows []hubcore.TreeNode) {
		for _, row := range rows {
			consider(row.UpdatedAt)
			visit(row.Children)
		}
	}
	visit(tree.Live)
	visit(tree.NeedsYou)
	for _, project := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		for _, tier := range []string{"current", "recent", "archived"} {
			rows, _ := project.TierRows(tier)
			visit(rows)
		}
	}
	return nearest
}
