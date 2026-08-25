package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
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
	Source     navigationSource
	Generation func() (string, error)
	Now        func() time.Time
	WaitUntil  func(context.Context, time.Time) error
	Cache      *navigationRepresentationCache
}

type navigationResourceState struct {
	Revision          uint64
	Fingerprint       navigationFingerprint
	lastNotifiedBuild uint64
}

type navigationCoreSnapshot struct {
	inputs       navigationBuildInputs
	projection   navigationProjection
	source       navigationSourceRevision
	epoch        uint64
	nextBoundary time.Time
}

type navigationBuildFlight struct {
	done    chan struct{}
	changes map[navigationResourceKey]bool
	err     error
	id      uint64
}

type navigationServiceStats struct {
	CoreBuilds uint64
	Cache      navigationCacheStats
}

// NavigationService owns the coherent, revisioned navigation generation for a
// single hub. Source capture is deliberately separate from projection: it lets
// us reject a snapshot whenever an input changes between capture and publish.
type NavigationService struct {
	mu sync.Mutex

	source     navigationSource
	generation string
	genErr     error
	now        func() time.Time
	waitUntil  func(context.Context, time.Time) error
	cache      *navigationRepresentationCache

	core       *navigationCoreSnapshot
	resources  map[navigationResourceKey]navigationResourceState // semantic keys
	sequence   uint64
	epoch      uint64
	flight     *navigationBuildFlight
	buildID    uint64
	coreBuilds uint64
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
		cache = newNavigationRepresentationCache(256, 64<<20)
	}
	return &NavigationService{
		source:     cfg.Source,
		generation: id,
		genErr:     err,
		now:        now,
		waitUntil:  waitUntil,
		cache:      cache,
		resources:  make(map[navigationResourceKey]navigationResourceState),
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

// Invalidate marks a source-side change. The next reader or explicit refresh
// takes a new capture; an in-flight capture is re-read before it can publish.
func (s *NavigationService) Invalidate(navigationChangeHint) {
	s.mu.Lock()
	s.epoch++
	s.mu.Unlock()
}

func (s *NavigationService) Capability() appwire.NavigationCapability {
	s.mu.Lock()
	defer s.mu.Unlock()
	return appwire.NavigationCapability{Version: 1, GenerationID: s.generation, Sequence: s.sequence}
}

func (s *NavigationService) Stats() navigationServiceStats {
	s.mu.Lock()
	stats := navigationServiceStats{CoreBuilds: s.coreBuilds}
	s.mu.Unlock()
	stats.Cache = s.cache.Stats()
	return stats
}

// CurrentRevision returns the revision of one semantic resource. It is useful
// for assertions; transport callers should use VersionedKey so generation and
// revision come from one lock acquisition.
func (s *NavigationService) CurrentRevision(key navigationResourceKey) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resources[key.Semantic()].Revision
}

// VersionedKey atomically resolves the current generation and semantic resource
// revision after ensuring a coherent core snapshot exists. HTTP must use this
// rather than reading Capability and CurrentRevision independently.
func (s *NavigationService) VersionedKey(ctx context.Context, key navigationResourceKey) (navigationResourceKey, error) {
	if _, err := s.ensureSnapshot(ctx, false); err != nil {
		return navigationResourceKey{}, err
	}
	semantic := key.Semantic()
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.resources[semantic]
	if !ok {
		return navigationResourceKey{}, navigationNotFoundError{kind: semantic.Kind}
	}
	key = key.canonical()
	key.Generation = s.generation
	key.Revision = state.Revision
	return key, nil
}

// Representation resolves a supplied (possibly stale) request key to the
// current coherent version and asks the encoded-resource cache for that exact
// immutable representation.
func (s *NavigationService) Representation(ctx context.Context, key navigationResourceKey) (navigationRepresentation, error) {
	versioned, err := s.VersionedKey(ctx, key)
	if err != nil {
		return navigationRepresentation{}, err
	}
	s.mu.Lock()
	core := s.core
	s.mu.Unlock()
	if core == nil {
		return navigationRepresentation{}, errors.New("navigation core unavailable")
	}
	representation, err := s.cache.Get(ctx, versioned, func(context.Context) (navigationRepresentation, error) {
		// A resource revision is part of its wire object, but is not part of its
		// semantic fingerprint. Rebuild this pure projection from the retained
		// core inputs with the version selected above.
		inputs := cloneNavigationInputs(core.inputs)
		inputs.Revision = versioned.Revision
		projection, err := buildNavigationProjection(inputs)
		if err != nil {
			return navigationRepresentation{}, err
		}
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
	zw := gzip.NewWriter(&buffer)
	if _, err := zw.Write(input); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Refresh captures and commits a current snapshot before returning the exact
// mutation targets. Explicit refreshes always perform one build; concurrent
// callers join the same build flight.
func (s *NavigationService) Refresh(ctx context.Context, hint navigationChangeHint) (hubapi.NavigationMutation, error) {
	if s.wouldOverflowSequence(hint) {
		return hubapi.NavigationMutation{}, errors.New("navigation sequence exceeds JavaScript safe integer")
	}
	flight, err := s.ensureSnapshot(ctx, true)
	if err != nil {
		return hubapi.NavigationMutation{}, err
	}
	targets, err := s.commitTargets(flight.id, flight.changes, hint)
	if err != nil {
		return hubapi.NavigationMutation{}, err
	}
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	return hubapi.NavigationMutation{GenerationID: generation, Targets: targets}, nil
}

func (s *NavigationService) wouldOverflowSequence(hint navigationChangeHint) bool {
	if len(hint.Projects) == 0 && !hint.Sources && !hint.Time && !hint.AllLoadedProjects {
		return false
	}
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
		flight := &navigationBuildFlight{changes: map[navigationResourceKey]bool{}, id: s.buildID}
		s.mu.Unlock()
		return flight, nil
	}
	if existing := s.flight; existing != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, navigationUnavailable(ctx.Err())
		case <-existing.done:
			return existing, existing.err
		}
	}
	flight := &navigationBuildFlight{done: make(chan struct{})}
	s.flight = flight
	s.mu.Unlock()

	s.buildSnapshot(ctx, revision, flight)
	return flight, flight.err
}

func (s *NavigationService) generationError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.genErr
}

func (s *NavigationService) buildSnapshot(ctx context.Context, initial navigationSourceRevision, flight *navigationBuildFlight) {
	defer func() {
		s.mu.Lock()
		if s.flight == flight {
			s.flight = nil
		}
		close(flight.done)
		s.mu.Unlock()
	}()

	wanted := initial
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
		inputs.GenerationID = generation
		inputs.Revision = 0 // fingerprints must not change merely because a revision did.
		projection, err := buildNavigationProjection(inputs)
		if err != nil {
			flight.err = err
			return
		}
		fingerprints, err := navigationResourceFingerprints(projection)
		if err != nil {
			flight.err = err
			return
		}
		after := s.source.Revision()
		s.mu.Lock()
		invalidated := s.epoch != epoch || after != wanted
		if invalidated {
			s.mu.Unlock()
			wanted = after
			continue
		}
		changes, states, err := navigationNextStates(s.resources, fingerprints)
		if err != nil {
			s.mu.Unlock()
			flight.err = err
			return
		}
		s.resources = states
		s.buildID++
		flight.id = s.buildID
		flight.changes = changes
		s.core = &navigationCoreSnapshot{inputs: inputs, projection: projection, source: after, epoch: epoch, nextBoundary: snapshot.NextBoundary}
		s.coreBuilds++
		s.mu.Unlock()
		return
	}
}

func navigationNextStates(previous map[navigationResourceKey]navigationResourceState, fingerprints map[navigationResourceKey]navigationFingerprint) (map[navigationResourceKey]bool, map[navigationResourceKey]navigationResourceState, error) {
	next := make(map[navigationResourceKey]navigationResourceState, len(fingerprints))
	changes := make(map[navigationResourceKey]bool)
	for key, fingerprint := range fingerprints {
		old, exists := previous[key]
		if !exists {
			next[key] = navigationResourceState{Revision: 1, Fingerprint: fingerprint}
			changes[key] = true
			continue
		}
		if old.Fingerprint != fingerprint {
			if old.Revision >= maxNavigationSafeInteger {
				return nil, nil, errors.New("navigation resource revision exceeds JavaScript safe integer")
			}
			old.Revision++
			old.Fingerprint = fingerprint
			changes[key] = true
		}
		next[key] = old
	}
	return changes, next, nil
}

func navigationResourceFingerprints(projection navigationProjection) (map[navigationResourceKey]navigationFingerprint, error) {
	keys := []navigationResourceKey{
		{Kind: navigationResourceManifest}, {Kind: navigationResourceLive}, {Kind: navigationResourceNeedsYou},
		{Kind: navigationResourcePinCatalog}, {Kind: navigationResourceProjects}, {Kind: navigationResourceArchivedProjects}, {Kind: navigationResourceTestRuns},
	}
	for _, section := range projection.pinSections {
		keys = append(keys, navigationResourceKey{Kind: navigationResourcePinSection, SectionID: section.id})
	}
	for project := range projection.projects {
		keys = append(keys, navigationResourceKey{Kind: navigationResourceProject, ProjectKey: project})
	}
	for ref := range projection.locations {
		keys = append(keys, navigationResourceKey{Kind: navigationResourceLocation, ID: ref})
	}
	out := make(map[navigationResourceKey]navigationFingerprint, len(keys))
	for _, key := range keys {
		_, fingerprint, err := projection.Resource(key)
		if err != nil {
			return nil, err
		}
		out[key.Semantic()] = fingerprint
	}
	return out, nil
}

func (s *NavigationService) commitTargets(buildID uint64, changes map[navigationResourceKey]bool, hint navigationChangeHint) (hubapi.NavigationArray[appwire.NavigationInvalidationTarget], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hint.AllLoadedProjects {
		if s.sequence >= maxNavigationSafeInteger {
			return nil, errors.New("navigation sequence exceeds JavaScript safe integer")
		}
		s.sequence++
		return hubapi.NavigationArray[appwire.NavigationInvalidationTarget]{{Kind: appwire.NavigationTargetAllLoadedProjects}}, nil
	}
	selected := make(map[navigationResourceKey]bool)
	for _, project := range hint.Projects {
		selected[(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: project}).Semantic()] = true
	}
	if hint.Sources {
		selected[(navigationResourceKey{Kind: navigationResourceManifest}).Semantic()] = true
	}
	if hint.Time {
		for key, changed := range changes {
			if changed {
				selected[key] = true
			}
		}
	}
	keys := make([]navigationResourceKey, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	targets := make(hubapi.NavigationArray[appwire.NavigationInvalidationTarget], 0, len(keys))
	for _, key := range keys {
		state, exists := s.resources[key]
		if !exists || !changes[key] || state.lastNotifiedBuild == buildID {
			continue
		}
		target, ok := navigationTargetForResource(key, state.Revision)
		if !ok {
			continue
		}
		state.lastNotifiedBuild = buildID
		s.resources[key] = state
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return targets, nil
	}
	if s.sequence >= maxNavigationSafeInteger {
		return nil, errors.New("navigation sequence exceeds JavaScript safe integer")
	}
	s.sequence++
	return targets, nil
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

// Start owns the single lifecycle scheduler. Each elapsed boundary forces a
// fresh coherent capture, then uses that capture's exact next boundary.
func (s *NavigationService) Start(ctx context.Context) {
	for {
		flight, err := s.ensureSnapshot(ctx, false)
		if err != nil {
			return
		}
		_ = flight
		s.mu.Lock()
		core := s.core
		s.mu.Unlock()
		if core == nil || core.nextBoundary.IsZero() {
			return
		}
		if err := s.waitUntil(ctx, core.nextBoundary); err != nil {
			return
		}
		s.Invalidate(navigationChangeHint{Time: true})
		if _, err := s.Refresh(ctx, navigationChangeHint{Time: true}); err != nil {
			return
		}
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

// webNavigationSource bridges the existing request-owned tree assembly to the
// service boundary without letting the pure projector read mutable server state.
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
	tree, attention, live, authority := s.web.memoTreeWithAuthority(ctx)
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
	inputs := navigationBuildInputsFromTreeSnapshot(generation, 0, tree, s.web.apiTreeSources(), hubAttentionSummaryFromCore(attention), live, favoriteView, projectFavoritePresentation(favoriteView), sections, assignments)
	return navigationSourceSnapshot{Inputs: inputs, NextBoundary: navigationSnapshotBoundary(tree, now)}, nil
}

// Navigation age labels have two time-dependent cutovers. The service owns one
// timer for the nearest one; source-driven changes reset it through Invalidate.
func navigationSnapshotBoundary(tree hubcore.Tree, now time.Time) time.Time {
	var nearest time.Time
	consider := func(updated time.Time) {
		if updated.IsZero() {
			return
		}
		for _, age := range []time.Duration{24 * time.Hour, 14 * 24 * time.Hour} {
			boundary := updated.Add(age)
			if !boundary.After(now) {
				continue
			}
			if nearest.IsZero() || boundary.Before(nearest) {
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
