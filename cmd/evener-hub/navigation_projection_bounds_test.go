package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// navigationCancellationInputs is a small fixture that reaches every
// context-checked walk in a projection build: input validation, the input
// clone (sources, live entries, every decoration map, pin assignments), pin
// candidates, the duplicate-key merge, pin sections, and the location index.
func navigationCancellationInputs() navigationBuildInputs {
	now := time.Unix(1_700_000_000, 0).UTC()
	row := func(id string, children ...hubcore.TreeNode) hubcore.TreeNode {
		return hubcore.TreeNode{ID: id, Title: id, Kind: "session", State: "idle", UpdatedAt: now, Children: children}
	}
	return navigationBuildInputs{
		GenerationID: "generation",
		Revision:     1,
		Sources: []hubapi.Source{
			{ID: "source-a", Label: "A", Kind: "local", Online: true},
			{ID: "source-b", Label: "B", Kind: "remote", Online: false},
		},
		LiveEntries:     []hubcore.LiveEntry{{SessionID: "session-live-a", Status: "active"}},
		Live:            map[string]bool{"session-live-a": true},
		Renameable:      map[string]bool{"session-live-a": true},
		SessionFavorite: map[string]bool{"session-live-a": true},
		ProjectFavorite: map[string]bool{"project-a": true},
		PinSections:     []hubcore.PinSection{{ID: "pins", Name: "Pins"}, {ID: "other", Name: "Other"}},
		PinAssignments: map[hubcore.ArchiveKey]hubcore.SessionPin{
			hubcore.SessionPinKey("", "session-live-a"): {SessionID: "session-live-a", SectionID: "pins"},
			hubcore.SessionPinKey("", "session-live-b"): {SessionID: "session-live-b", SectionID: "missing"},
		},
		Tree: hubcore.Tree{
			Live: []hubcore.TreeNode{
				row("session-live-a", hubcore.TreeNode{ID: "child-a", Title: "child", Kind: "subagent", State: "idle", UpdatedAt: now}),
				row("session-live-b"),
			},
			NeedsYou: []hubcore.TreeNode{row("session-need-a")},
			Projects: []hubcore.TreeProject{
				{Key: "project-a", Name: "a", Current: []hubcore.TreeNode{row("session-project-a")}},
				{Key: "project-a", Name: "a-two", Current: []hubcore.TreeNode{row("session-project-b")}},
				{Key: "project-run", Name: "run", IsTestRun: true, Current: []hubcore.TreeNode{row("session-run-a")}},
			},
			ArchivedProjects: []hubcore.TreeProject{
				{Key: "project-z", Name: "z", IsArchived: true, Current: []hubcore.TreeNode{row("session-archived-a")}},
			},
		},
	}
}

// A projection build cancelled at any of its context checks must surface the
// cancellation as the build's error: no walk may swallow it and still hand back
// a projection. Sweeping every check count a full build makes — measured
// first, so the test never carries a hand-calibrated flip point — pins that
// property across all of them at once.
func TestNavigationProjectionBuildSurfacesCancellationAtEveryCheck(t *testing.T) {
	t.Parallel()
	inputs := navigationCancellationInputs()
	counting := &cancelAfterChecksContext{Context: context.Background(), limit: math.MaxInt32}
	if _, err := buildNavigationProjectionContext(counting, inputs); err != nil {
		t.Fatalf("uncancelled build: %v", err)
	}
	total := counting.checks.Load()
	if total < 1 {
		t.Fatal("fixture made no context checks; the sweep would prove nothing")
	}
	for limit := int32(1); limit <= total; limit++ {
		_, err := buildNavigationProjectionContext(&cancelAfterChecksContext{Context: context.Background(), limit: limit}, inputs)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("build cancelled at check %d of %d returned %v, want the cancellation surfaced", limit, total, err)
		}
	}
	// One check beyond everything a full build makes must never cancel.
	if _, err := buildNavigationProjectionContext(&cancelAfterChecksContext{Context: context.Background(), limit: total + 1}, inputs); err != nil {
		t.Fatalf("build under a never-reached cancellation limit failed: %v", err)
	}
}

// The section fitter keeps a page under the response byte ceiling by dropping
// whole trailing rows. Every rendered label is already bounded by the
// projector, but State passes through from the source, so rows only a
// misbehaving daemon could send are the shape that reaches the ceiling; the
// fitter must still answer a deterministic leading prefix, never an
// oversized page.
func TestNavigationSectionDropsTrailingRowsWhenOverTheResponseBudget(t *testing.T) {
	t.Parallel()
	rows := make([]hubcore.TreeNode, maxNavigationSectionRows)
	for index := range rows {
		rows[index] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", index), Kind: "session", State: strings.Repeat("s", 45_000)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: rows}})
	if err != nil {
		t.Fatal(err)
	}
	page := projection.LivePage(0, maxNavigationSectionRows)
	if !page.Truncated {
		t.Fatal("an over-budget section page is not marked truncated")
	}
	if len(page.Sessions) == 0 || len(page.Sessions) >= len(rows) {
		t.Fatalf("rows=%d of %d, want a non-empty leading prefix", len(page.Sessions), len(rows))
	}
	if got, want := page.Remaining, len(rows)-len(page.Sessions); got != want {
		t.Fatalf("remaining=%d, want the %d dropped rows", got, want)
	}
	for index, row := range page.Sessions {
		if got, want := row.SessionID, fmt.Sprintf("session-%03d", index); got != want {
			t.Fatalf("row %d = %q, want %q: the prefix must keep source order", index, got, want)
		}
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxNavigationResponseBytes {
		t.Fatalf("fitted section encodes to %d bytes, want at most %d", len(encoded), maxNavigationResponseBytes)
	}
}

// The project detail carries two tiers in one envelope, so its fitter spends
// one node budget across both: the current tier is served first and the
// recent tier inherits whatever budget is left, with every dropped row counted
// in its tier's remaining.
func TestNavigationProjectDetailTruncatesAcrossTiersWhenOverBudget(t *testing.T) {
	t.Parallel()
	rows := func(prefix string) []hubcore.TreeNode {
		rows := make([]hubcore.TreeNode, maxNavigationSectionRows)
		for index := range rows {
			rows[index] = hubcore.TreeNode{ID: fmt.Sprintf("%s-%03d", prefix, index), Kind: "session", State: strings.Repeat("s", 21_000)}
		}
		return rows
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{
			Key:     "project",
			Name:    "project",
			Current: rows("current"),
			Recent:  rows("recent"),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.ProjectIn("", "project")
	if !ok {
		t.Fatal("project missing")
	}
	if !resource.Truncated {
		t.Fatal("an over-budget project detail is not marked truncated")
	}
	served := len(resource.Current.Sessions) + len(resource.Recent.Sessions)
	if served == 0 || served >= 2*maxNavigationSectionRows {
		t.Fatalf("served rows=%d, want a non-empty prefix under both tiers' caps", served)
	}
	if got, want := resource.Current.Remaining+resource.Recent.Remaining, 2*maxNavigationSectionRows-served; got != want {
		t.Fatalf("remaining=%d, want the %d dropped rows", got, want)
	}
	if len(resource.Current.Sessions) < len(resource.Recent.Sessions) {
		t.Fatalf("current=%d recent=%d: the current tier must be served first", len(resource.Current.Sessions), len(resource.Recent.Sessions))
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxNavigationResponseBytes {
		t.Fatalf("fitted project detail encodes to %d bytes, want at most %d", len(encoded), maxNavigationResponseBytes)
	}
}

// The archived page fitter can cut a subtree in the middle: the row that
// survives keeps its already-admitted children and reports the rest through
// omitted_descendants. Nested forks make the cut land inside a chain, so the
// accounting has to fold whole omitted subtrees, not just count rows.
func TestNavigationArchivedPageReportsPartiallyOmittedForks(t *testing.T) {
	t.Parallel()
	const grandchildren = 120
	// State is the one field the projector passes through unbounded, so it is
	// the lever that pushes the page over the byte budget: 242 nodes of ~12 KB
	// far exceed the 2 MB response ceiling and force a mid-subtree cut.
	grandchild := func(index int) hubcore.TreeNode {
		return hubcore.TreeNode{
			ID:    fmt.Sprintf("fork-%03d", index),
			Title: fmt.Sprintf("fork-%03d", index),
			Kind:  "fork",
			State: strings.Repeat("s", 12_000),
			Children: []hubcore.TreeNode{{
				ID:    fmt.Sprintf("fork-%03d-a", index),
				Title: fmt.Sprintf("fork-%03d-a", index),
				Kind:  "fork",
				State: strings.Repeat("s", 12_000),
			}},
		}
	}
	chain := make([]hubcore.TreeNode, grandchildren)
	for index := range chain {
		chain[index] = grandchild(index)
	}
	root := hubcore.TreeNode{
		ID: "continuation", Title: "continuation", Kind: "session", State: "ended",
		UpdatedAt: time.Unix(200, 0).UTC(),
		Children: []hubcore.TreeNode{{
			ID: "chain", Title: "chain", Kind: "fork",
			State:    strings.Repeat("s", 12_000),
			Children: chain,
		}},
	}
	total := 2 + 2*grandchildren
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{
			Key:      "project",
			Name:     "project",
			Archived: []hubcore.TreeNode{root},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := projection.ArchivedList(t.Context(), navigationArchivedListRequest{Hint: navigationResourceProjects, ProjectKey: "project", Limit: maxNavigationSectionRows})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Ref != "local:continuation" {
		t.Fatalf("archived page rows = %+v, want the single continuation root", page.Sessions)
	}
	forkChain := page.Sessions[0].Children
	if len(forkChain) != 1 || forkChain[0].Ref != "local:chain" {
		t.Fatalf("continuation children = %+v, want the single fork chain", forkChain)
	}
	kept := forkChain[0].Children
	if len(kept) == 0 || len(kept) >= grandchildren {
		t.Fatalf("chain kept %d of %d children, want a partial mid-subtree prefix", len(kept), grandchildren)
	}
	if last := kept[len(kept)-1]; last.OmittedDescendants == 0 {
		t.Fatalf("the last kept grandchild reports %d omitted descendants, want the rest of its own subtree", last.OmittedDescendants)
	}
	assertArchivedForkAccounting(t, page.Sessions, total)
	if size := navigationEncodedSize(page.Sessions); size > maxNavigationResponseBytes {
		t.Fatalf("fitted archived rows encode to %d bytes, want at most %d", size, maxNavigationResponseBytes)
	}
}

// Every refusal a projection read can answer is a structural error or a
// false ok: an unknown section, project, location or kind never falls back to
// a sibling resource, and a pin-section read by its key ID still resolves.
func TestNavigationResourceReadsRefuseUnknownSelectors(t *testing.T) {
	t.Parallel()
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree: hubcore.Tree{
			// The pinned row rides a project tier: pin candidates come from
			// the retained pin-candidate rows and project tiers, never from the
			// live section alone.
			Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: []hubcore.TreeNode{
				{ID: "session-live", Title: "live", Kind: "session", State: "idle"},
				{ID: "session-project", Title: "row", Kind: "session", State: "idle"},
			}}},
		},
		PinSections: []hubcore.PinSection{{ID: "pins", Name: "Pins"}},
		PinAssignments: map[hubcore.ArchiveKey]hubcore.SessionPin{
			hubcore.SessionPinKey("", "session-live"): {SessionID: "session-live", SectionID: "pins"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func() error{
		"unknown pin section": func() error {
			_, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourcePinSection, SectionID: "missing"})
			return err
		},
		"unknown project": func() error {
			_, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "missing"})
			return err
		},
		"unknown location": func() error {
			_, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLocation, ID: "local:missing"})
			return err
		},
		"unknown resource kind": func() error {
			_, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourceKind("bogus")})
			return err
		},
		"unknown catalog": func() error {
			_, err := projection.CatalogPage(navigationResourceKind("bogus"), 0, maxNavigationCatalogRows)
			return err
		},
		"unknown project page key": func() error {
			_, err := projection.ProjectPageIn("", "missing", "current", 0, maxNavigationSectionRows)
			return err
		},
		"unknown project page tier": func() error {
			_, err := projection.ProjectPageIn("", "project", "bogus", 0, maxNavigationSectionRows)
			return err
		},
	} {
		if err := read(); err == nil {
			t.Errorf("%s: read succeeded, want a refusal", name)
		}
	}
	if _, ok := projection.PinSectionPage("missing", 0, maxNavigationSectionRows); ok {
		t.Error("unknown pin section page returned ok")
	}
	if _, ok := projection.Location("local:missing"); ok {
		t.Error("unknown location returned ok")
	}
	if _, ok := projection.ProjectIn("", "missing"); ok {
		t.Error("unknown project detail returned ok")
	}
	// A project in the active catalog is invisible to a read that names
	// another catalog: a catalog narrows, it never falls through.
	if _, ok := projection.ProjectIn(navigationResourceArchivedProjects, "project"); ok {
		t.Error("active project answered an archived-catalog read")
	}
	// A pin-section read without its SectionID falls back to the key's ID.
	section, _, err := projection.Resource(navigationResourceKey{Kind: navigationResourcePinSection, ID: "pins"})
	if err != nil {
		t.Fatalf("pin section read by key ID: %v", err)
	}
	rows, ok := section.(hubapi.NavigationSectionResource)
	if !ok || len(rows.Sessions) != 1 || rows.Sessions[0].Ref != "local:session-live" {
		t.Fatalf("pin section by key ID = %#v, want the pinned live row", section)
	}
	// A value outside the revision-stamping switch passes through untouched.
	if got := navigationResourceWithRevision(42, 9); got != any(42) {
		t.Fatalf("revision stamping touched an unknown value: %#v", got)
	}
}

// An alias location is only as good as the ref it is asked for: a malformed
// ref refuses rather than minting a location for an identity no client can
// read back.
func TestNavigationAliasLocationRefusesMalformedRefs(t *testing.T) {
	t.Parallel()
	root := hubapi.NavigationSessionLocation{GenerationID: "generation", Revision: 1, Ref: "local:root", Session: &hubapi.NavigationSessionSummary{Project: "project"}}
	if _, ok := navigationAliasLocation("bad ref", "subagent", root); ok {
		t.Fatal("malformed alias ref produced a location")
	}
}

// A fitted page never answers zero rows while it still owes data: the client
// would treat the empty page as the end of the list.
func TestValidateNavigationPageProgressRejectsEmptyRowsWithRemaining(t *testing.T) {
	t.Parallel()
	err := validateNavigationPageProgress(navigationResourceLive, hubapi.NavigationSectionResource{Remaining: 3})
	if _, ok := errors.AsType[navigationPageProgressInvariantError](err); !ok {
		t.Fatalf("error = %v, want the page progress invariant", err)
	}
}

// The fitting search probes candidate budgets; a probe that fails must fail
// the whole search from whichever loop issued it — the doubling walk up, the
// doubling walk down, and the binary refinement between them.
func TestNavigationFittingBudgetPropagatesProbeErrorsFromEverySearchLoop(t *testing.T) {
	t.Parallel()
	probeFailure := errors.New("probe failed")
	// nodes=8, maxBytes=100, fullBytes=200 lands the first guess at 4.
	counting := func(failAt int32) func(int) (int, error) {
		var calls int32
		return func(int) (int, error) {
			if calls++; calls >= failAt {
				return 0, probeFailure
			}
			return 1, nil // small probes fit; large-budget probes are made to fail instead
		}
	}
	// Second probe: inside the upward doubling loop after the guess fits.
	if _, err := navigationFittingBudget(8, 100, 200, counting(2)); !errors.Is(err, probeFailure) {
		t.Fatalf("upward loop error = %v, want the probe's error", err)
	}
	// nodes=8, maxBytes=100, fullBytes=400 lands the first guess at 2, which
	// does not fit; the second probe runs inside the downward doubling loop.
	var calls int32
	nothingFitsUntil := func(int) (int, error) {
		if calls++; calls >= 2 {
			return 0, probeFailure
		}
		return 101, nil
	}
	if _, err := navigationFittingBudget(8, 100, 400, nothingFitsUntil); !errors.Is(err, probeFailure) {
		t.Fatalf("downward loop error = %v, want the probe's error", err)
	}
	// Fourth probe: the upward loop converged (guess 4, then 5, then 7) and the
	// binary refinement probes the middle next.
	if _, err := navigationFittingBudget(8, 100, 200, counting(4)); !errors.Is(err, probeFailure) {
		t.Fatalf("binary search error = %v, want the probe's error", err)
	}
}

// Pin rows inside one section keep a total order: recency first, then the
// canonical ref, then the bare ID — so two rows that share an instant and a
// ref still order by ID instead of racing on sort stability.
func TestNavigationPinSectionRowsOrderByUpdateThenRefThenID(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	rows := []hubcore.TreeNode{
		{ID: "b", Ref: "local:same", Title: "b", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "a", Ref: "local:same", Title: "a", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "old", Ref: "local:old", Title: "old", Kind: "session", State: "idle", UpdatedAt: now.Add(-time.Hour)},
		{ID: "newest", Ref: "local:newest", Title: "newest", Kind: "session", State: "idle", UpdatedAt: now.Add(time.Hour)},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree:         hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: rows}}},
		PinSections:  []hubcore.PinSection{{ID: "pins", Name: "Pins"}},
		// The assignment is keyed by the (source, session id) pair a row's
		// canonical ref spells, so the two rows sharing a ref share one pin.
		PinAssignments: map[hubcore.ArchiveKey]hubcore.SessionPin{
			hubcore.SessionPinKey("local", "same"):   {SectionID: "pins"},
			hubcore.SessionPinKey("local", "old"):    {SectionID: "pins"},
			hubcore.SessionPinKey("local", "newest"): {SectionID: "pins"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := projection.PinSectionPage("pins", 0, maxNavigationSectionRows)
	if !ok {
		t.Fatal("pin section missing")
	}
	var gotRefs []string
	for _, row := range page.Sessions {
		gotRefs = append(gotRefs, row.Ref)
	}
	if want := []string{"local:newest", "local:same", "local:same", "local:old"}; !slices.Equal(gotRefs, want) {
		t.Fatalf("pin order = %v, want %v", gotRefs, want)
	}
	// Two rows that share an instant and a ref differ only in their bare IDs;
	// the wire summary cannot tell them apart, so the tiebreak is pinned on
	// the section's own rows.
	var section *navigationPinSection
	for index := range projection.pinSections {
		if projection.pinSections[index].id == "pins" {
			section = &projection.pinSections[index]
		}
	}
	if section == nil {
		t.Fatal("pin section missing from the projection")
	}
	var gotIDs []string
	for _, row := range section.rows {
		gotIDs = append(gotIDs, row.ID)
	}
	if want := []string{"newest", "a", "b", "old"}; !slices.Equal(gotIDs, want) {
		t.Fatalf("pin row order = %v, want %v", gotIDs, want)
	}
}

// The non-context clone is the service's capture boundary (navigation_service
// reads fresh inputs through it): it must own every decoration map, so a
// later roster or favorites refresh cannot rewrite a projection built from
// the earlier capture.
func TestCloneNavigationInputsOwnsDecorationMaps(t *testing.T) {
	t.Parallel()
	inputs := navigationBuildInputs{
		GenerationID:    "generation",
		Sources:         []hubapi.Source{{ID: "source", Label: "before", Kind: "local"}},
		Live:            map[string]bool{"session-a": true},
		Renameable:      map[string]bool{"session-a": true},
		SessionFavorite: map[string]bool{"session-a": true},
		ProjectFavorite: map[string]bool{"project-a": true},
		PinSections:     []hubcore.PinSection{{ID: "pins", Name: "Pins"}},
		PinAssignments: map[hubcore.ArchiveKey]hubcore.SessionPin{
			hubcore.SessionPinKey("", "session-a"): {SessionID: "session-a", SectionID: "pins"},
		},
	}
	clone := cloneNavigationInputs(inputs)
	inputs.Sources[0].Label = "after"
	delete(inputs.Live, "session-a")
	delete(inputs.Renameable, "session-a")
	delete(inputs.SessionFavorite, "session-a")
	delete(inputs.ProjectFavorite, "project-a")
	inputs.PinSections[0].Name = "After"
	delete(inputs.PinAssignments, hubcore.SessionPinKey("", "session-a"))
	if clone.Sources[0].Label != "before" {
		t.Fatal("clone shares the sources slice with its input")
	}
	if !clone.Live["session-a"] || !clone.Renameable["session-a"] || !clone.SessionFavorite["session-a"] || !clone.ProjectFavorite["project-a"] {
		t.Fatal("clone shares a decoration map with its input")
	}
	if clone.PinSections[0].Name != "Pins" {
		t.Fatal("clone shares the pin sections slice with its input")
	}
	if _, ok := clone.PinAssignments[hubcore.SessionPinKey("", "session-a")]; !ok {
		t.Fatal("clone shares the pin assignment map with its input")
	}
}

// The projector's own row limit and tier lookup are the inner guards under
// every page: more rows than the limit mark the traversal truncated, and a
// tier the tree cannot name yields an empty page rather than a panic.
func TestNavigationProjectorCapsRowsAndUnknownTiers(t *testing.T) {
	t.Parallel()
	rows := []hubcore.TreeNode{
		{ID: "session-a", Kind: "session", State: "idle"},
		{ID: "session-b", Kind: "session", State: "idle"},
		{ID: "session-c", Kind: "session", State: "idle"},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: rows}})
	if err != nil {
		t.Fatal(err)
	}
	projector := navigationProjector{projection: projection}
	served := projector.projectNodes(rows, 2)
	if len(served) != 2 || !projector.truncated {
		t.Fatalf("rows=%d truncated=%v, want 2 rows and the truncation mark", len(served), projector.truncated)
	}
	empty, remaining := projector.projectTier(hubcore.TreeProject{Key: "project", Current: rows}, "bogus", 0, maxNavigationSectionRows)
	if len(empty) != 0 || remaining != 0 {
		t.Fatalf("unknown tier served %d rows with %d remaining, want an empty page", len(empty), remaining)
	}
}
