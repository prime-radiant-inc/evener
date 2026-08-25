package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// A tiny collection window lets requests released together join one service
	// refresh flight even when a capture itself is faster than goroutine dispatch.
	defaultNavigationRefreshCoalesceWindow = time.Millisecond
)

// buildNavigationServiceProjection is a narrow test seam proving resource
// misses reuse the retained core projection rather than rebuilding it.
var buildNavigationServiceProjection = buildNavigationProjection

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
	WaitUntil    func(context.Context, time.Time) error
	Cache        *navigationRepresentationCache
	BuildTimeout time.Duration
	RetryAfter   time.Duration
}

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
	committed bool
}

type navigationServiceStats struct {
	CoreBuilds uint64
	Cache      navigationCacheStats
}

// NavigationService owns the coherent, revisioned navigation generation for a
// single hub. Its source capture and pure projection are separated so a changed
// source can never publish an incoherent projection under an older key.
type NavigationService struct {
	mu sync.Mutex

	source       navigationSource
	generation   string
	genErr       error
	now          func() time.Time
	waitUntil    func(context.Context, time.Time) error
	buildTimeout time.Duration
	retryAfter   time.Duration
	coalesceWait time.Duration
	cache        *navigationRepresentationCache

	core       *navigationCoreSnapshot
	resources  map[navigationResourceKey]navigationResourceState // includes tombstones
	sequence   uint64
	epoch      uint64
	flight     *navigationBuildFlight
	buildID    uint64
	coreBuilds uint64
	wake       chan struct{}
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
	waitUntil := cfg.WaitUntil
	if waitUntil == nil {
		waitUntil = waitForNavigationBoundary
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
	return &NavigationService{
		source:       cfg.Source,
		generation:   id,
		genErr:       err,
		now:          now,
		waitUntil:    waitUntil,
		buildTimeout: cfg.BuildTimeout,
		retryAfter:   cfg.RetryAfter,
		coalesceWait: defaultNavigationRefreshCoalesceWindow,
		cache:        cache,
		resources:    make(map[navigationResourceKey]navigationResourceState),
		wake:         make(chan struct{}, 1),
	}
}

func newNavigationGenerationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("navigation generation: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func waitForNavigationBoundary(ctx context.Context, boundary time.Time) error {
	delay := time.Until(boundary)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Invalidate marks source-side state dirty and immediately resets a scheduler
// wait. Task 7 owns producer hooks; this method is intentionally idempotent.
func (s *NavigationService) Invalidate(navigationChangeHint) {
	s.mu.Lock()
	s.epoch++
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

func (s *NavigationService) Stats() navigationServiceStats {
	s.mu.Lock()
	stats := navigationServiceStats{CoreBuilds: s.coreBuilds}
	s.mu.Unlock()
	stats.Cache = s.cache.Stats()
	return stats
}

// CurrentRevision is assertion-oriented. HTTP must use VersionedKey, which
// reads generation and semantic revision under one lock after a coherent build.
func (s *NavigationService) CurrentRevision(key navigationResourceKey) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resources[key.Semantic()].Revision
}

// VersionedKey atomically obtains the current semantic resource version. It is
// paired internally with the immutable core projection selected by
// Representation, so a new projection's bytes cannot enter an old cache key.
func (s *NavigationService) VersionedKey(ctx context.Context, key navigationResourceKey) (navigationResourceKey, error) {
	_, versioned, _, err := s.versionedCore(ctx, key)
	return versioned, err
}

func (s *NavigationService) versionedCore(ctx context.Context, key navigationResourceKey) (*navigationBuildFlight, navigationResourceKey, navigationProjection, error) {
	flight, err := s.ensureSnapshot(ctx, false)
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
func (s *NavigationService) Representation(ctx context.Context, key navigationResourceKey) (navigationRepresentation, error) {
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
	if err != nil && errors.Is(err, context.Canceled) {
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
	flight, err := s.ensureSnapshot(ctx, true)
	if err != nil {
		return hubapi.NavigationMutation{}, err
	}
	return s.commitFlightTargets(flight, hint)
}

func (s *NavigationService) sequenceAtSafeLimit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence >= maxNavigationSafeInteger
}

func (s *NavigationService) ensureSnapshot(ctx context.Context, force bool) (*navigationBuildFlight, error) {
	if ctx == nil {
		return nil, errors.New("navigation: nil context")
	}
	if err := s.generationError(); err != nil {
		return nil, err
	}
	if s.source == nil {
		return nil, errors.New("navigation source is unavailable")
	}
	revision := s.source.Revision()
	s.mu.Lock()
	if !force && s.core != nil && s.core.source == revision && s.core.epoch == s.epoch {
		flight := &navigationBuildFlight{changes: map[navigationResourceKey]bool{}, id: s.buildID, done: closedNavigationDone()}
		s.mu.Unlock()
		return flight, nil
	}
	if flight := s.flight; flight != nil {
		s.mu.Unlock()
		return s.waitFlight(ctx, flight)
	}
	flight := &navigationBuildFlight{done: make(chan struct{})}
	s.flight = flight
	s.mu.Unlock()
	buildCtx, cancel := context.WithTimeout(context.Background(), s.buildTimeout)
	go func() {
		defer cancel()
		s.buildSnapshot(buildCtx, revision, flight)
	}()
	return s.waitFlight(ctx, flight)
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

func (s *NavigationService) buildSnapshot(ctx context.Context, expected navigationSourceRevision, flight *navigationBuildFlight) {
	defer func() {
		s.mu.Lock()
		if s.flight == flight {
			s.flight = nil
		}
		close(flight.done)
		s.mu.Unlock()
	}()
	// Keep the newly-created flight joinable long enough to collect callers that
	// were released concurrently. Without this bounded window, a fast capture can
	// finish between scheduler turns and turn one concurrent refresh burst into a
	// serial run of redundant captures.
	if s.coalesceWait > 0 {
		timer := time.NewTimer(s.coalesceWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			flight.err = navigationUnavailable(ctx.Err())
			return
		case <-timer.C:
		}
	}
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
		inputs := cloneNavigationInputs(snapshot.Inputs)
		inputs.Tree = snapshot.Inputs.Tree.Snapshot()
		inputs.GenerationID = generation
		inputs.Revision = 0 // semantic fingerprints never include transport revision.
		projection, err := buildNavigationServiceProjection(inputs)
		if err != nil {
			flight.err = err
			return
		}
		fingerprints, dependencies, err := navigationLogicalFingerprints(projection)
		if err != nil {
			flight.err = err
			return
		}
		// Compare immediately before publication. Any source/Invalidate movement
		// rejects this full build and retries; publication never mixes a newer core
		// with an older resource version.
		after := s.source.Revision()
		s.mu.Lock()
		stale := s.epoch != epoch || after != expected || s.source.Revision() != after
		if stale {
			s.mu.Unlock()
			expected = s.source.Revision()
			continue
		}
		changes, states, err := navigationNextStates(s.resources, fingerprints, dependencies)
		if err != nil {
			s.mu.Unlock()
			flight.err = err
			return
		}
		s.resources = states
		s.buildID++
		flight.id = s.buildID
		flight.changes = changes
		s.core = &navigationCoreSnapshot{projection: projection, source: after, epoch: epoch, nextBoundary: snapshot.NextBoundary}
		s.coreBuilds++
		s.mu.Unlock()
		return
	}
}

func navigationNextStates(previous map[navigationResourceKey]navigationResourceState, fingerprints map[navigationResourceKey]navigationFingerprint, dependencies map[navigationResourceKey][]navigationResourceKey) (map[navigationResourceKey]bool, map[navigationResourceKey]navigationResourceState, error) {
	next := make(map[navigationResourceKey]navigationResourceState, len(previous)+len(fingerprints))
	changes := make(map[navigationResourceKey]bool)
	keys := make(map[navigationResourceKey]bool, len(previous)+len(fingerprints))
	for key := range previous {
		keys[key] = true
	}
	for key := range fingerprints {
		keys[key] = true
	}
	for key := range keys {
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

// navigationLogicalFingerprints covers complete logical resources, not just a
// first page. It therefore changes a shared section/catalog/project revision on
// off-page edits, membership changes, descendant changes, and removals.
func navigationLogicalFingerprints(projection navigationProjection) (map[navigationResourceKey]navigationFingerprint, map[navigationResourceKey][]navigationResourceKey, error) {
	fingerprints := make(map[navigationResourceKey]navigationFingerprint)
	dependencies := make(map[navigationResourceKey][]navigationResourceKey)
	put := func(key navigationResourceKey, value any, deps ...navigationResourceKey) error {
		fingerprint, err := navigationLogicalFingerprint(value)
		if err != nil {
			return err
		}
		key = key.Semantic()
		fingerprints[key] = fingerprint
		for _, dep := range deps {
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
	if err := put(navigationResourceKey{Kind: navigationResourceLive}, navigationLogicalNodes(projection, projection.live), navigationResourceKey{Kind: navigationResourceLive}); err != nil {
		return nil, nil, err
	}
	if err := put(navigationResourceKey{Kind: navigationResourceNeedsYou}, navigationLogicalNodes(projection, projection.needsYou), navigationResourceKey{Kind: navigationResourceNeedsYou}); err != nil {
		return nil, nil, err
	}
	pinCatalog := make([]hubapi.NavigationPinSectionDescriptor, 0, len(projection.pinSections))
	for _, section := range projection.pinSections {
		pinCatalog = append(pinCatalog, hubapi.NavigationPinSectionDescriptor{ID: section.id, Name: section.name, Count: len(section.rows)})
		key := navigationResourceKey{Kind: navigationResourcePinSection, SectionID: section.id}
		if err := put(key, navigationLogicalNodes(projection, section.rows), key); err != nil {
			return nil, nil, err
		}
	}
	if err := put(navigationResourceKey{Kind: navigationResourcePinCatalog}, pinCatalog, navigationResourceKey{Kind: navigationResourcePinCatalog}); err != nil {
		return nil, nil, err
	}
	for _, kind := range []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects, navigationResourceTestRuns} {
		projects := projection.catalogs[kind]
		logical := make([]hubapi.NavigationProjectSummary, 0, len(projects))
		for _, project := range projects {
			logical = append(logical, projection.projectSummary(project))
		}
		key := navigationResourceKey{Kind: kind}
		if err := put(key, logical, key); err != nil {
			return nil, nil, err
		}
	}
	for projectKey, project := range projection.projects {
		logical := struct {
			Project  hubapi.NavigationProjectSummary
			Current  hubapi.NavigationArray[hubapi.NavigationSessionSummary]
			Recent   hubapi.NavigationArray[hubapi.NavigationSessionSummary]
			Archived hubapi.NavigationArray[hubapi.NavigationSessionSummary]
		}{Project: projection.projectSummary(project)}
		current, _ := project.TierRows("current")
		recent, _ := project.TierRows("recent")
		archived, _ := project.TierRows("archived")
		logical.Current = navigationLogicalNodes(projection, current)
		logical.Recent = navigationLogicalNodes(projection, recent)
		logical.Archived = navigationLogicalNodes(projection, archived)
		key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: projectKey}
		if err := put(key, logical, key); err != nil {
			return nil, nil, err
		}
	}
	for ref, location := range projection.locations {
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

func navigationLogicalNodes(projection navigationProjection, rows []hubcore.TreeNode) hubapi.NavigationArray[hubapi.NavigationSessionSummary] {
	out := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], len(rows))
	for index, row := range rows {
		out[index] = navigationLogicalNode(projection, row)
	}
	return out
}

func navigationLogicalNode(projection navigationProjection, row hubcore.TreeNode) hubapi.NavigationSessionSummary {
	summary := navigationProjector{projection: projection}.projectShallow(row)
	summary.Children = navigationLogicalNodes(projection, row.Children)
	return summary
}

func navigationLogicalFingerprint(value any) (navigationFingerprint, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return navigationFingerprint{}, err
	}
	return sha256.Sum256(encoded), nil
}

// commitFlightTargets makes a shared capture one logical mutation. Joined
// refresh calls must not replay its exact invalidations or advance sequence a
// second time after the first caller has committed them.
func (s *NavigationService) commitFlightTargets(flight *navigationBuildFlight, hint navigationChangeHint) (hubapi.NavigationMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if flight.committed {
		return hubapi.NavigationMutation{GenerationID: s.generation, Targets: hubapi.NavigationArray[appwire.NavigationInvalidationTarget]{}}, nil
	}
	mutation, err := s.commitTargetsLocked(flight.changes, hint)
	if err == nil {
		flight.committed = true
	}
	return mutation, err
}

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
	for {
		_, err := s.ensureSnapshot(ctx, false)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !s.waitRetryOrWake(ctx) {
				return
			}
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
			_, _ = s.Refresh(ctx, navigationChangeHint{Time: true})
		}
	}
}

func (s *NavigationService) waitRetryOrWake(ctx context.Context) bool {
	timer := time.NewTimer(s.retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (s *NavigationService) waitBoundaryOrWake(ctx context.Context, boundary time.Time) (elapsed, keepGoing bool) {
	waitCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.waitUntil(waitCtx, boundary) }()
	select {
	case <-ctx.Done():
		cancel()
		return false, false
	case <-s.wake:
		cancel()
		return false, true
	case err := <-done:
		cancel()
		if err != nil {
			return false, ctx.Err() == nil
		}
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
		revision.Remote = s.web.cfg.RemoteThreadCache.Snapshot().Generation
	}
	return revision
}

func (s webNavigationSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	if s.web == nil {
		return navigationSourceSnapshot{}, errors.New("navigation web source is unavailable")
	}
	snapshot := s.web.navigationSnapshot(ctx)
	decisions := s.web.archiveDecisions()
	tree := hubcore.BuildTreeAtWithProjects(snapshot.metas, snapshot.live, decisions, now, snapshot.projects)
	_, attention := hubcore.DeriveAttention(snapshot.metas, snapshot.live, decisions)
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
