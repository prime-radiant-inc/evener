package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

func TestNavigationSectionAppliesRecursiveBounds(t *testing.T) {
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Revision:     7,
		Tree:         hubcore.Tree{Live: []hubcore.TreeNode{deepNavigationNode(40)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	if !section.Truncated {
		t.Fatal("deep section must report truncation")
	}
	if got := countNavigationNodes(section.Sessions); got > maxNavigationNodes {
		t.Fatalf("nodes=%d, max=%d", got, maxNavigationNodes)
	}
	if got := navigationDepth(section.Sessions); got > maxNavigationDepth {
		t.Fatalf("depth=%d, max=%d", got, maxNavigationDepth)
	}
}

func TestNavigationProjectPagePreservesOrderAndUint32Offset(t *testing.T) {
	rows := make([]hubcore.TreeNode, 51)
	for i := range rows {
		rows[i] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", i), Title: fmt.Sprintf("row %03d", i), Kind: "session", State: "idle", UpdatedAt: time.Unix(int64(i), 0).UTC()}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation", Revision: 3,
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: rows}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := projection.ProjectPage("project", "current", 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(page.Sessions), 1; got != want {
		t.Fatalf("rows=%d, want %d", got, want)
	}
	if got, want := page.Sessions[0].SessionID, "session-050"; got != want {
		t.Fatalf("session=%q, want %q", got, want)
	}
	empty, err := projection.ProjectPage("project", "current", ^uint32(0), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Sessions) != 0 || empty.Remaining != 0 {
		t.Fatalf("large offset returned %#v", empty)
	}
}

func TestNavigationManifestHasNoRowsAndLocationHasSummary(t *testing.T) {
	project := hubcore.TreeProject{Key: "project", Name: "project", Current: []hubcore.TreeNode{{ID: "session-parent", Title: "parent", Kind: "session", State: "idle", Children: []hubcore.TreeNode{{ID: "session-child", Title: "child", Kind: "subagent", State: "ended"}}}}}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Revision: 2, Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := projection.Manifest()
	if got := manifest.Catalogs.Projects.Count; got != 1 {
		t.Fatalf("project count=%d", got)
	}
	location, ok := projection.Location("local:session-child")
	if !ok || location.Session == nil || location.TopLevel || location.TopLevelRef != "local:session-parent" || location.ProjectKey != "project" {
		t.Fatalf("location=%#v, found=%v", location, ok)
	}
	if _, ok := any(manifest).(hubapi.NavigationSessionSummary); ok {
		t.Fatal("manifest must not contain navigation rows")
	}
}

func TestNavigationProjectionCarriesActiveAndCompletedJobs(t *testing.T) {
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID:    "session-parent",
			Title: "parent",
			Kind:  "session",
			State: "idle",
			RunningJobs: []appwire.EvenerJobInfo{{
				JobID: "job-running", JobType: "shell", Status: "running", Command: "go test ./...", Intent: "Running the package tests to find the failure",
			}},
			CompletedJobs: []appwire.EvenerJobInfo{{
				JobID: "job-completed", JobType: "shell", Status: "completed", Command: "go fmt ./...",
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	row := resource.Current.Sessions[0]
	if len(row.RunningJobs) != 1 || row.RunningJobs[0].JobID != "job-running" || row.RunningJobs[0].Command != "go test ./..." {
		t.Fatalf("running jobs = %+v", row.RunningJobs)
	}
	if got := row.RunningJobs[0].Intent; got != "Running the package tests to find the failure" {
		t.Fatalf("running job intent = %q", got)
	}
	if len(row.CompletedJobs) != 1 || row.CompletedJobs[0].JobID != "job-completed" || row.CompletedJobs[0].Status != "completed" {
		t.Fatalf("completed jobs = %+v", row.CompletedJobs)
	}
}

// An old daemon (or a past-index entry) carries no watch rows. The summary
// must still build, with an empty watch list and no error.
func TestNavigationWatchProjectionAbsentWatchesYieldsEmptyList(t *testing.T) {
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-parent", Title: "parent", Kind: "session", State: "idle",
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatalf("projection with absent watches failed: %v", err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	row := resource.Current.Sessions[0]
	if len(row.Watches) != 0 {
		t.Fatalf("row.Watches = %+v, want empty when the tree node carries none", row.Watches)
	}
}

// A receiver watch is visible to two sessions, but each summary carries only
// its own daemon's rows. Carrying one session's rows onto another here would
// double count the watch in a subtree rollup.
func TestNavigationSummaryDoesNotAggregateWatchesAcrossSessions(t *testing.T) {
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{
			{
				ID: "session-a", Title: "a", Kind: "session", State: "idle",
				Watches: []appwire.EvenerWatchInfo{{
					ID: "watch-a", Source: "timer", Target: "session-b", SendTo: "session-b",
					Note: "owner watch", Cadence: []appwire.EvenerWatchCadence{{Kind: "every", Seconds: 600}},
					Deliveries: 2, CreatedAt: "2026-09-12T10:00:00Z", Active: true,
					DeliveryTimes: []string{"2026-09-12T10:00:01Z", "2026-09-12T10:00:02Z"},
				}},
			},
			{
				ID: "session-b", Title: "b", Kind: "session", State: "idle",
				Watches: []appwire.EvenerWatchInfo{{ID: "watch-b", Source: "output", Note: "receiver watch", CreatedAt: "2026-09-12T10:00:00Z"}},
			},
		},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	rows := make(map[string]hubapi.NavigationSessionSummary, len(resource.Current.Sessions))
	for _, row := range resource.Current.Sessions {
		rows[row.SessionID] = row
	}
	rowA, okA := rows["session-a"]
	rowB, okB := rows["session-b"]
	if !okA || !okB {
		t.Fatalf("sessions = %+v, want session-a and session-b", rows)
	}
	if len(rowA.Watches) != 1 || rowA.Watches[0].ID != "watch-a" {
		t.Fatalf("session-a watches = %+v, want only watch-a", rowA.Watches)
	}
	watch := rowA.Watches[0]
	if watch.Source != "timer" || watch.Note != "owner watch" || watch.SendTo != "session-b" ||
		len(watch.Cadence) != 1 || watch.Cadence[0].Kind != "every" || watch.Cadence[0].Seconds != 600 ||
		watch.Deliveries != 2 || !watch.Active {
		t.Fatalf("session-a projected watch = %+v, want the carried fields", watch)
	}
	wantDeliveryTimes := []string{"2026-09-12T10:00:01Z", "2026-09-12T10:00:02Z"}
	if !reflect.DeepEqual(watch.DeliveryTimes, wantDeliveryTimes) {
		t.Fatalf("session-a DeliveryTimes = %+v, want %+v", watch.DeliveryTimes, wantDeliveryTimes)
	}
	if len(rowB.Watches) != 1 || rowB.Watches[0].ID != "watch-b" {
		t.Fatalf("session-b watches = %+v, want only watch-b", rowB.Watches)
	}
	if rowB.Watches[0].DeliveryTimes == nil || len(rowB.Watches[0].DeliveryTimes) != 0 {
		t.Fatalf("session-b DeliveryTimes = %#v, want an empty non-nil list when the source row carries none", rowB.Watches[0].DeliveryTimes)
	}
	for _, carried := range rowB.Watches {
		if carried.ID == "watch-a" {
			t.Fatalf("session-b aggregates session-a's watch: %+v", rowB.Watches)
		}
	}
}

// TestNavigationWatchDeliveryTimesDropsUnrepresentableInstants pins the fix for
// the codec-break found in review: the web codec validates every delivery_times
// entry as strict RFC3339, so TRUNCATING an over-long value with an ellipsis made
// the whole watch-carrying snapshot fail to decode. An instant the codec cannot
// represent is dropped instead; a normal RFC3339 instant passes through unchanged.
func TestNavigationWatchDeliveryTimesDropsUnrepresentableInstants(t *testing.T) {
	long := strings.Repeat("a", maxNavigationLabelRunes+64)
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{{
				ID: "watch-a", Source: "output", CreatedAt: "2026-09-12T10:00:00Z",
				DeliveryTimes: []string{"2026-09-12T10:00:00Z", long},
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 || len(resource.Current.Sessions[0].Watches) != 1 {
		t.Fatalf("projected rows = %+v, want one session with one watch", resource.Current.Sessions)
	}
	got := resource.Current.Sessions[0].Watches[0].DeliveryTimes
	if !reflect.DeepEqual(got, []string{"2026-09-12T10:00:00Z"}) {
		t.Fatalf("DeliveryTimes = %+v, want only the representable instant", got)
	}
}

// A watch whose required created_at cannot be represented is dropped entirely:
// created_at has no absent form in the codec, so carrying an ellipsized value
// would reject the whole resource, and carrying a fabricated one would lie.
func TestNavigationWatchProjectionDropsWatchWithUnrepresentableCreatedAt(t *testing.T) {
	long := strings.Repeat("x", maxNavigationLabelRunes+64)
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{
				{ID: "watch-ok", Source: "output", CreatedAt: "2026-09-12T10:00:00Z"},
				{ID: "watch-bad", Source: "output", CreatedAt: long},
			},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	rows := resource.Current.Sessions
	if len(rows) != 1 {
		t.Fatalf("sessions = %+v, want one", rows)
	}
	if len(rows[0].Watches) != 1 || rows[0].Watches[0].ID != "watch-ok" {
		t.Fatalf("watches = %+v, want only watch-ok", rows[0].Watches)
	}
	if rows[0].Watches[0].CreatedAt != "2026-09-12T10:00:00Z" {
		t.Fatalf("CreatedAt = %q, want the valid instant unchanged", rows[0].Watches[0].CreatedAt)
	}
}

// TestNavigationWatchProjectionDropsSchemaInvalidRows pins the review fix for
// the projector's last unchecked path: navigationWatches byte-bounded and
// clamped representable values but never ran the hub schema's own watch
// predicate, so a row with an empty ID, a negative deliveries count, or a
// negative cadence seconds reached the wire and failed
// navigationSessionValueValid for the WHOLE session -- and through it every
// other session in the resource. Each such row is dropped and counted as
// omitted, exactly like an unrepresentable created_at.
func TestNavigationWatchProjectionDropsSchemaInvalidRows(t *testing.T) {
	valid := func(id string) appwire.EvenerWatchInfo {
		return appwire.EvenerWatchInfo{ID: id, Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true}
	}
	negativeDeliveries := valid("watch-bad")
	negativeDeliveries.Deliveries = -1
	negativeSeconds := valid("watch-bad")
	negativeSeconds.Cadence = []appwire.EvenerWatchCadence{{Kind: "every", Seconds: -1}}
	tests := []struct {
		name string
		bad  appwire.EvenerWatchInfo
	}{
		{name: "empty id", bad: appwire.EvenerWatchInfo{Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true}},
		{name: "negative deliveries", bad: negativeDeliveries},
		{name: "negative cadence seconds", bad: negativeSeconds},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := projectSessionWithWatches(t, []appwire.EvenerWatchInfo{valid("watch-ok"), tc.bad})
			if len(row.Watches) != 1 || row.Watches[0].ID != "watch-ok" {
				t.Fatalf("kept watches = %+v, want only the surviving watch-ok", row.Watches)
			}
			if row.OmittedWatches != 1 {
				t.Fatalf("OmittedWatches = %d, want 1 for the schema-invalid row", row.OmittedWatches)
			}
			// The projected summary is what reaches the client, so it must pass
			// the very predicate the malformed row would have failed.
			if !navigationSessionValueValid(row) {
				t.Fatalf("projected summary rejected by the hub schema: %+v", row)
			}
		})
	}
}

// The point of dropping the bad row rather than failing the resource: the OTHER
// sessions in the same resource stay listed and readable.
func TestNavigationWatchProjectionInvalidRowKeepsOtherSessions(t *testing.T) {
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{
			{
				ID: "session-bad", Title: "bad", Kind: "session", State: "idle",
				Watches: []appwire.EvenerWatchInfo{{Source: "self", CreatedAt: "2026-09-12T10:00:00Z"}},
			},
			{
				ID: "session-ok", Title: "ok", Kind: "session", State: "idle",
				Watches: []appwire.EvenerWatchInfo{{ID: "watch-ok", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"}},
			},
		},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want both sessions still listed", resource.Current.Sessions)
	}
	rows := make(map[string]hubapi.NavigationSessionSummary, len(resource.Current.Sessions))
	for _, row := range resource.Current.Sessions {
		rows[row.SessionID] = row
		if !navigationSessionValueValid(row) {
			t.Fatalf("session %q rejected by the hub schema: %+v", row.SessionID, row)
		}
	}
	if len(rows["session-bad"].Watches) != 0 || rows["session-bad"].OmittedWatches != 1 {
		t.Fatalf("session-bad watches = %+v / omitted %d, want none kept and the drop counted", rows["session-bad"].Watches, rows["session-bad"].OmittedWatches)
	}
	if len(rows["session-ok"].Watches) != 1 || rows["session-ok"].Watches[0].ID != "watch-ok" {
		t.Fatalf("session-ok watches = %+v, want the valid row untouched", rows["session-ok"].Watches)
	}
}

// TestNavigationWatchCadenceCarriesEventEveryAndFilter proves the events
// cadence's every-Nth count and filter summary reach the hub's wire summary,
// so the rail and session panel can distinguish a throttled or filtered event
// watch. The filter is a caller-supplied string, so it is bounded like every
// other rendered watch label.
func TestNavigationWatchCadenceCarriesEventEveryAndFilter(t *testing.T) {
	longFilter := strings.Repeat("f", maxNavigationLabelRunes+64)
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{{
				ID: "watch-a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z",
				Cadence: []appwire.EvenerWatchCadence{
					{Kind: "events", Every: 3, Filter: "tool_name=Bash, status=error"},
					{Kind: "events", Filter: longFilter},
				},
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 || len(resource.Current.Sessions[0].Watches) != 1 {
		t.Fatalf("projected rows = %+v, want one session with one watch", resource.Current.Sessions)
	}
	got := resource.Current.Sessions[0].Watches[0].Cadence
	want := []hubapi.NavigationWatchCadence{
		{Kind: "events", Every: 3, Filter: "tool_name=Bash, status=error"},
		{Kind: "events", Filter: truncateNavigationRunes(longFilter, maxNavigationLabelRunes)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Cadence = %+v, want %+v", got, want)
	}
}

// A watch's id and source are identity, not display text: the rail and the
// panel derive row keys from watch.id. Truncating either with an ellipsis let
// two distinct long ids collapse to the same label and collide, so both pass
// through untouched while the display fields keep their label bound.
func TestNavigationWatchProjectionKeepsIdentityUntruncated(t *testing.T) {
	longID := "watch-" + strings.Repeat("a", maxNavigationLabelRunes) + "-alpha"
	siblingID := "watch-" + strings.Repeat("a", maxNavigationLabelRunes) + "-bravo"
	longSource := "source-" + strings.Repeat("s", maxNavigationLabelRunes) + "-end"
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{
				{ID: longID, Source: longSource, CreatedAt: "2026-09-12T10:00:00Z"},
				{ID: siblingID, Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
			},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	watches := resource.Current.Sessions[0].Watches
	if len(watches) != 2 {
		t.Fatalf("watches = %+v, want two rows", watches)
	}
	if watches[0].ID != longID || watches[1].ID != siblingID {
		t.Fatalf("watch ids = %q, %q, want both untruncated and distinct", watches[0].ID, watches[1].ID)
	}
	if watches[0].Source != longSource {
		t.Fatalf("watch source = %q, want the untruncated source", watches[0].Source)
	}
}

// An identity OVER the bound is dropped, not cut. Two ids that share their first
// maxNavigationIdentityBytes bytes would truncate to the same value, and the rail
// and the panel key their rows by watch.id -- so cutting would show one row where
// the session holds two. The dropped row is counted as omitted instead, like a
// row whose created_at cannot be represented.
func TestNavigationWatchProjectionDropsOversizedIdentities(t *testing.T) {
	prefix := "watch-" + strings.Repeat("a", maxNavigationIdentityBytes) + "-"
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{
				{ID: prefix + "alpha", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true},
				{ID: prefix + "bravo", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
				{ID: "watch-kept", Source: strings.Repeat("s", maxNavigationIdentityBytes+1), CreatedAt: "2026-09-12T10:00:00Z"},
				{ID: "watch-ok", Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
			},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	session := resource.Current.Sessions[0]
	if len(session.Watches) != 1 || session.Watches[0].ID != "watch-ok" {
		t.Fatalf("watches = %+v, want only the representable row", session.Watches)
	}
	if session.OmittedWatches != 3 || session.OmittedArmedWatches != 1 {
		t.Fatalf("omitted = %d (%d armed), want 3 (1 armed): every dropped row is counted",
			session.OmittedWatches, session.OmittedArmedWatches)
	}
}

// validNavigationTimestamp must accept exactly what the web codec accepts. Both
// read the same shared fixture list so the Go check cannot drift from the
// codec's rfc3339Timestamp grammar.
func TestValidNavigationTimestampMatchesCodecFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "navigation", "timestamps.json"))
	if err != nil {
		t.Fatalf("read codec fixture: %v", err)
	}
	var fixtures []struct {
		Value string `json:"value"`
		Valid bool   `json:"valid"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode codec fixture: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("codec fixture is empty")
	}
	for _, fixture := range fixtures {
		if got := validNavigationTimestamp(fixture.Value); got != fixture.Valid {
			t.Errorf("validNavigationTimestamp(%q) = %v, want the codec's %v", fixture.Value, got, fixture.Valid)
		}
	}
}

// A cadence kind is an IDENTITY on the wire: the web codec's watchCadenceValue
// validates it with identity(value.kind), which caps it at 1024 BYTES, and the
// hub's navigationSessionValueValid mirrors that. The projector bounded it with
// truncateNavigationRunes at 512 runes, which is up to ~2 KiB for non-ASCII
// text, so an over-long multibyte kind produced a summary the codec rejected --
// and because the codec validates watch rows as part of the session entity, one
// bad kind failed the entire navigation response. This test mirrors the codec's
// identity bound the way TestValidNavigationTimestampMatchesCodecFixture mirrors
// its rfc3339Timestamp grammar.
func TestNavigationWatchCadenceKindMatchesCodecIdentityBytes(t *testing.T) {
	// 300 four-byte runes = 1200 bytes: past the 1024-byte identity cap while
	// still only ~300 runes, so the rune bound alone left it over budget.
	overLong := strings.Repeat("😀", 300)
	if len(overLong) <= maxNavigationIdentityBytes {
		t.Fatalf("fixture kind = %d bytes, want it over the %d-byte identity cap", len(overLong), maxNavigationIdentityBytes)
	}
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			Watches: []appwire.EvenerWatchInfo{{
				ID: "watch-a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z",
				Cadence: []appwire.EvenerWatchCadence{{Kind: overLong, Seconds: 1}},
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 || len(resource.Current.Sessions[0].Watches) != 1 {
		t.Fatalf("sessions = %+v, want one session with one watch", resource.Current.Sessions)
	}
	cadence := resource.Current.Sessions[0].Watches[0].Cadence
	if len(cadence) != 1 {
		t.Fatalf("cadence = %+v, want one step", cadence)
	}
	kind := cadence[0].Kind
	// The codec's identity(): non-empty and at most 1024 bytes.
	if kind == "" {
		t.Fatal("projected kind is empty; the codec requires a non-empty identity")
	}
	if len(kind) > maxNavigationIdentityBytes {
		t.Fatalf("projected kind = %d bytes, want at most %d; the codec rejects the whole response otherwise", len(kind), maxNavigationIdentityBytes)
	}
	if kind == overLong {
		t.Fatal("projected kind was not cut; the codec would reject it")
	}
	// The hub schema mirrors the codec, so the projected summary must pass it --
	// this is the value that reaches the client.
	if !navigationSessionValueValid(resource.Current.Sessions[0]) {
		t.Fatalf("projected summary rejected by the hub schema: %+v", resource.Current.Sessions[0])
	}
}

// The events cadence's every-Nth throttle is caller-supplied and the daemon
// bounds it only to the platform int range, so on 64-bit it can sit above the
// codec's safe integer range (2^53-1). Projecting it verbatim failed
// navigationSessionValueValid and made the whole resource -- every session in
// it -- unreadable. The projector must clamp it. Clamping, not dropping: the
// wire spells an absent/zero every as "no throttle" (the codec reads every > 0
// as a throttle), so dropping would misstate a throttled watch as firing on
// every matching event. This mirrors the codec's safe-integer bound the way
// TestValidNavigationTimestampMatchesCodecFixture mirrors its timestamp grammar.
func TestNavigationWatchCadenceEveryClampedToSafeInteger(t *testing.T) {
	oversized := int(maxNavigationSafeInteger) + 1
	node := hubcore.TreeNode{
		ID: "session-a", Title: "a", Kind: "session", State: "idle",
		Watches: []appwire.EvenerWatchInfo{{
			ID: "watch-a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z",
			Events:  []string{"assistant.tool"},
			Cadence: []appwire.EvenerWatchCadence{{Kind: "events", Every: oversized}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{node}}})
	if err != nil {
		t.Fatal(err)
	}
	resource := projection.LivePage(0, maxNavigationSectionRows)
	if len(resource.Sessions) != 1 || len(resource.Sessions[0].Watches) != 1 {
		t.Fatalf("live sessions = %+v, want one session with one watch", resource.Sessions)
	}
	cadence := resource.Sessions[0].Watches[0].Cadence
	if len(cadence) != 1 {
		t.Fatalf("cadence = %+v, want one step", cadence)
	}
	got := cadence[0].Every
	// 0 means "no throttle" on the wire, so a clamped throttle must stay
	// positive: dropping the field would misstate the watch's behaviour.
	if got <= 0 {
		t.Fatalf("projected every = %d, want a positive clamped throttle", got)
	}
	if uint64(got) > maxNavigationSafeInteger {
		t.Fatalf("projected every = %d, want at most %d; the codec rejects the whole response otherwise", got, maxNavigationSafeInteger)
	}
	if got == oversized {
		t.Fatal("projected every was not clamped; the codec would reject it")
	}
	// The hub schema mirrors the codec, so the projected summary must pass it --
	// this is the value that reaches the client.
	if !navigationSessionValueValid(resource.Sessions[0]) {
		t.Fatalf("projected summary rejected by the hub schema: %+v", resource.Sessions[0])
	}
}

// A session's project is an IDENTITY on the wire, not a rendered label: the web
// codec validates it with identity(value.project, true), which caps it at 1024
// BYTES, and the hub's navigationSessionValueValid mirrors that with a byte
// length check. The projector bounded it with truncateNavigationRunes at 512
// runes, which is up to ~2 KiB of multibyte text, so a multibyte-heavy project
// name produced a summary the codec rejects -- failing the whole navigation
// response for one field. This test mirrors the codec's identity bound the same
// way TestValidNavigationTimestampMatchesCodecFixture mirrors its timestamp
// grammar.
func TestNavigationSessionProjectMatchesCodecIdentityBytes(t *testing.T) {
	// 300 four-byte runes = 1200 bytes: past the 1024-byte identity cap while
	// still only ~300 runes, so the rune bound alone left it over budget.
	overLong := strings.Repeat("😀", 300)
	if len(overLong) <= maxNavigationIdentityBytes {
		t.Fatalf("fixture project = %d bytes, want it over the %d-byte identity cap", len(overLong), maxNavigationIdentityBytes)
	}
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Project: overLong, Kind: "session", State: "idle",
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one session", resource.Current.Sessions)
	}
	summary := resource.Current.Sessions[0]
	if summary.Project == overLong {
		t.Fatalf("projected project = %d bytes, want it cut to the %d-byte identity cap", len(summary.Project), maxNavigationIdentityBytes)
	}
	if summary.Project == "" || len(summary.Project) > maxNavigationIdentityBytes {
		t.Fatalf("projected project = %q (%d bytes), want a non-empty value within %d bytes", summary.Project, len(summary.Project), maxNavigationIdentityBytes)
	}
	if !navigationSessionValueValid(summary) {
		t.Fatalf("projected summary rejected by the hub schema: %+v", summary)
	}
}

// A location fit that dropped its session is not a fit. The deep link renders
// the session summary and nothing else, so serving the envelope without it would
// answer 200 with nothing to show -- where an irreducible overflow used to be
// reported -- and a deep link to a job-heavy session that cannot fit (with the
// watch payload already shed) is exactly how that happens.
func TestValidateNavigationPageProgressRejectsSessionlessLocation(t *testing.T) {
	if err := validateNavigationPageProgress(navigationResourceLocation, hubapi.NavigationSessionLocation{}); err == nil {
		t.Fatal("session-less location passed validation: the deep link would render nothing")
	}
	location := hubapi.NavigationSessionLocation{
		Session: &hubapi.NavigationSessionSummary{Ref: "local:root", SessionID: "root", State: "idle", Kind: "session"},
	}
	if err := validateNavigationPageProgress(navigationResourceLocation, location); err != nil {
		t.Fatalf("location carrying its session rejected: %v", err)
	}
}

// The nested job and watch rows carry their own identities, and the codec
// validates every one of them with identity() at 1024 bytes. Only job_id is
// guarded by the build's own validation, so job_type, status, watch id and
// watch source can otherwise reach the wire unbounded and poison the whole
// session entity exactly as an over-long cadence kind did.
//
// The two kinds of identity are treated differently on purpose: a DISPLAY
// identity (job_type, job status) is cut to the cap, while an identity the rail
// and the panel key their rows by (watch id, watch source) is DROPPED and
// counted -- cutting it would map two distinct long ids onto one row key.
func TestNavigationNestedIdentityFieldsMatchCodecIdentityBytes(t *testing.T) {
	overLong := strings.Repeat("😀", 300) // 1200 bytes, 300 runes
	if len(overLong) <= maxNavigationIdentityBytes {
		t.Fatalf("fixture identity = %d bytes, want it over the %d-byte identity cap", len(overLong), maxNavigationIdentityBytes)
	}
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID: "session-a", Title: "a", Kind: "session", State: "idle",
			RunningJobs:   []appwire.EvenerJobInfo{{JobID: "job-running", JobType: overLong, Status: "running"}},
			CompletedJobs: []appwire.EvenerJobInfo{{JobID: "job-done", JobType: "shell", Status: overLong}},
			Watches: []appwire.EvenerWatchInfo{
				{ID: overLong, Source: "self", CreatedAt: "2026-09-12T10:00:00Z"},
				{ID: "watch-b", Source: overLong, CreatedAt: "2026-09-12T10:00:00Z"},
			},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	if len(resource.Current.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one session", resource.Current.Sessions)
	}
	summary := resource.Current.Sessions[0]
	if len(summary.RunningJobs) != 1 || len(summary.CompletedJobs) != 1 || len(summary.Watches) != 0 {
		t.Fatalf("projected rows = %+v / %+v / %+v, want one running job, one completed job and no watch rows", summary.RunningJobs, summary.CompletedJobs, summary.Watches)
	}
	if summary.OmittedWatches != 2 || summary.OmittedArmedWatches != 0 {
		t.Fatalf("omitted watches = %d (%d armed), want both unrepresentable rows counted",
			summary.OmittedWatches, summary.OmittedArmedWatches)
	}
	fields := map[string]string{
		"job_type":   summary.RunningJobs[0].JobType,
		"job_status": summary.CompletedJobs[0].Status,
	}
	for name, value := range fields {
		if value == overLong {
			t.Errorf("projected %s = %d bytes, want it cut to the %d-byte identity cap", name, len(value), maxNavigationIdentityBytes)
			continue
		}
		if value == "" || len(value) > maxNavigationIdentityBytes {
			t.Errorf("projected %s = %q (%d bytes), want a non-empty value within %d bytes", name, value, len(value), maxNavigationIdentityBytes)
		}
	}
	if !navigationSessionValueValid(summary) {
		t.Fatalf("projected summary rejected by the hub schema: %+v", summary)
	}
}

func TestNavigationJobSummaryKeepsFullCommandForTooltip(t *testing.T) {
	long := strings.Repeat("a", 600)
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID:    "session-parent",
			Title: "parent",
			Kind:  "session",
			State: "idle",
			RunningJobs: []appwire.EvenerJobInfo{{
				JobID: "job-running", JobType: "shell", Status: "running", Command: long, Intent: "intent text",
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	job := resource.Current.Sessions[0].RunningJobs[0]
	if job.Command == long {
		t.Fatal("command must be truncated for the row label")
	}
	if got, want := len([]rune(job.Command)), maxNavigationLabelRunes; got != want {
		t.Fatalf("truncated command runes=%d, want %d", got, want)
	}
	if job.FullCommand != long {
		t.Fatalf("full_command = %q, want the untruncated command", job.FullCommand)
	}
}

func TestNavigationJobSummaryOmitsFullCommandWhenLabelFits(t *testing.T) {
	project := hubcore.TreeProject{
		Key:  "project",
		Name: "project",
		Current: []hubcore.TreeNode{{
			ID:    "session-parent",
			Title: "parent",
			Kind:  "session",
			State: "idle",
			RunningJobs: []appwire.EvenerJobInfo{{
				JobID: "job-running", JobType: "shell", Status: "running", Command: "go test ./...",
			}},
		}},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, ok := projection.Project("project")
	if !ok {
		t.Fatal("project missing")
	}
	job := resource.Current.Sessions[0].RunningJobs[0]
	if job.FullCommand != "" {
		t.Fatalf("full_command = %q, want empty when the command fits the label bound", job.FullCommand)
	}
}

func TestNavigationProjectionRejectsMalformedIdentity(t *testing.T) {
	_, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{{ID: "bad ref", Title: "bad", Kind: "session", State: "idle"}}}})
	if err == nil {
		t.Fatal("malformed identity accepted")
	}
}

func TestNavigationProjectionUsesStableTreeNodeRef(t *testing.T) {
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree: hubcore.Tree{Live: []hubcore.TreeNode{{
			ID: "new-instance", Ref: "local:old-instance", Title: "session", Kind: "session", State: "idle",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := projection.LivePage(0, 50)
	if len(page.Sessions) != 1 || page.Sessions[0].Ref != "local:old-instance" {
		t.Fatalf("live page = %#v, want stable ref", page.Sessions)
	}
	location, ok := projection.Location("local:old-instance")
	if !ok || location.Session == nil || location.Session.Ref != "local:old-instance" {
		t.Fatalf("location = %#v, found=%v, want stable ref", location, ok)
	}
}

func TestNavigationBoundsLimitRowsCatalogAndStrings(t *testing.T) {
	live := make([]hubcore.TreeNode, 51)
	projects := make([]hubcore.TreeProject, 101)
	for i := range live {
		live[i] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d", i), Title: strings.Repeat("a", maxNavigationTitleRunes), Kind: "session", State: "idle"}
	}
	for i := range projects {
		projects[i] = hubcore.TreeProject{Key: fmt.Sprintf("project-%03d", i), Name: strings.Repeat("b", maxNavigationLabelRunes)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: live, Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	if got, want := len(section.Sessions), maxNavigationSectionRows; got != want || section.Remaining != 1 {
		t.Fatalf("section rows=%d remaining=%d, want %d and 1", got, section.Remaining, want)
	}
	if got := len(section.Sessions[0].Title); got != maxNavigationTitleRunes {
		t.Fatalf("title bytes=%d, want %d", got, maxNavigationTitleRunes)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.Projects), maxNavigationCatalogRows; got != want || catalog.Remaining != 1 {
		t.Fatalf("catalog rows=%d remaining=%d, want %d and 1", got, catalog.Remaining, want)
	}
}

func TestNavigationProjectionSanitizesOversizedUnicodeStrings(t *testing.T) {
	title := strings.Repeat("界", maxNavigationTitleRunes+10) + string([]byte{0xff})
	label := strings.Repeat("界", maxNavigationLabelRunes+10) + string([]byte{0xfe})
	workingDir := strings.Repeat("界", maxNavigationWorkingDirBytes)
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Sources:      []hubapi.Source{{ID: "source", Label: label, Kind: "appwire"}},
		Tree: hubcore.Tree{
			Live:     []hubcore.TreeNode{{ID: "session", Title: title, Project: label, Branch: label, Kind: "session", State: "idle"}},
			Projects: []hubcore.TreeProject{{Key: "project", Name: label, WorkingDir: workingDir}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := projection.LivePage(0, 1).Sessions[0]
	if got := len([]rune(row.Title)); got != maxNavigationTitleRunes {
		t.Fatalf("title runes=%d, want %d", got, maxNavigationTitleRunes)
	}
	if !utf8.ValidString(row.Title) || !utf8.ValidString(row.Project) || !utf8.ValidString(row.Branch) {
		t.Fatalf("row strings are not valid UTF-8: %#v", row)
	}
	// Branch is a rendered label (the codec's boundedString(512) and the schema's
	// rune count both bound it in runes). Project is an identity on the wire: the
	// codec's identity(value.project, true) and navigationSessionValueValid both
	// bound it in BYTES, so it is length-checked as bytes here.
	if len([]rune(row.Branch)) != maxNavigationLabelRunes || len(row.Project) > maxNavigationIdentityBytes {
		t.Fatalf("row strings were not sanitized: %#v", row)
	}
	source := projection.Manifest().Sources[0]
	if !utf8.ValidString(source.Label) || len([]rune(source.Label)) != maxNavigationLabelRunes {
		t.Fatalf("source label was not sanitized: %q", source.Label)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 1)
	if err != nil || len(catalog.Projects) != 1 || !utf8.ValidString(catalog.Projects[0].WorkingDir) || len(catalog.Projects[0].WorkingDir) > maxNavigationWorkingDirBytes {
		t.Fatalf("working directory was not sanitized: %#v, %v", catalog, err)
	}
}

func TestNavigationProjectionPinsDecorationsAndOrder(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rows := []hubcore.TreeNode{
		{ID: "session-z", Title: "z", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "session-a", Title: "a", Kind: "session", State: "idle", UpdatedAt: now},
		{ID: "session-ended", Title: "ended", Kind: "session", State: "ended", UpdatedAt: now},
		{ID: "session-dangling", Title: "dangling", Kind: "session", State: "idle", UpdatedAt: now},
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree:         hubcore.Tree{Live: rows, Projects: []hubcore.TreeProject{{Key: "project", Name: "project", Current: rows}}},
		Live:         map[string]bool{"session-ended": true},
		SessionFavorite: map[string]bool{
			"session-a":        true,
			"session-dangling": true,
		},
		PinSections:    []hubcore.PinSection{{ID: "pin", Name: "Pinned", MemberCount: 3}, {ID: "empty", Name: "Empty", MemberCount: 0}},
		PinAssignments: map[string]hubcore.SessionPin{"session-a": {SectionID: "pin"}, "session-z": {SectionID: "pin"}, "session-dangling": {SectionID: "missing"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pins, ok := projection.PinSectionPage("pin", 0, 50)
	if !ok || len(pins.Sessions) != 2 || pins.Sessions[0].Ref != "local:session-a" || pins.Sessions[1].Ref != "local:session-z" {
		t.Fatalf("pin order=%+v, found=%v", pins.Sessions, ok)
	}
	empty, ok := projection.PinSectionPage("empty", 0, 50)
	if !ok || len(empty.Sessions) != 0 {
		t.Fatalf("empty pin section=%+v, found=%v", empty.Sessions, ok)
	}
	catalog := projection.PinCatalogPage(0, 50)
	if len(catalog.PinSections) != 2 || catalog.PinSections[0].ID != "empty" || catalog.PinSections[0].Count != 0 || catalog.PinSections[1].ID != "pin" || catalog.PinSections[1].Count != 3 {
		t.Fatalf("pin catalog=%+v, want every durable section and durable counts", catalog.PinSections)
	}
	if projection.Manifest().Sections.PinSections.Count != 2 {
		t.Fatalf("manifest pin count=%d, want 2 durable sections", projection.Manifest().Sections.PinSections.Count)
	}
	if pins.Sessions[0].Favorite {
		t.Fatal("named pin must clear legacy favorite")
	}
	live := projection.LivePage(0, 50)
	if live.Sessions[2].Live {
		t.Fatal("ended roster row must not be actionable live")
	}
	location, ok := projection.Location("local:session-dangling")
	if !ok || location.PinSectionID != "" || location.Session == nil || !location.Session.Favorite {
		t.Fatalf("dangling assignment leaked into location: %#v", location)
	}
}

func TestNavigationProjectionRetainsIndependentInputsAndReturnsCopies(t *testing.T) {
	tree := hubcore.Tree{Live: []hubcore.TreeNode{{ID: "session", Title: "before", Kind: "session", State: "idle", Children: []hubcore.TreeNode{{ID: "child", Title: "child", Kind: "subagent", State: "ended"}}}}}
	inputs := navigationBuildInputs{GenerationID: "generation", Sources: []hubapi.Source{{ID: "source", Label: "before", Kind: "remote"}}, Tree: tree}
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Sources[0].Label = "after"
	tree.Live[0].Title = "after"
	tree.Live[0].Children[0].Title = "after child"
	if got := projection.Manifest().Sources[0].Label; got != "before" {
		t.Fatalf("source aliased input: %q", got)
	}
	if got := projection.LivePage(0, 50).Sessions[0].Title; got != "before" {
		t.Fatalf("tree aliased input: %q", got)
	}
	location, ok := projection.Location("local:session")
	if !ok || location.Session == nil {
		t.Fatal("missing location")
	}
	location.Session.Title = "mutated"
	again, _ := projection.Location("local:session")
	if again.Session.Title != "before" {
		t.Fatalf("location return aliased cache: %q", again.Session.Title)
	}
}

func TestNavigationProjectionEnforcesExactEncodedCeilings(t *testing.T) {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-%03d-%03d", root, child), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-root-%03d", root), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "session", State: "idle", Children: children}
	}
	projects := make([]hubcore.TreeProject, 100)
	for index := range projects {
		projects[index] = hubcore.TreeProject{Key: strings.Repeat(fmt.Sprintf("%03d", index), 342)[:maxNavigationIdentityBytes], Name: strings.Repeat("n", maxNavigationLabelRunes), WorkingDir: strings.Repeat("/", maxNavigationWorkingDirBytes)}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots, Projects: projects}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	encoded, _ := json.Marshal(section)
	if len(encoded) > maxNavigationResponseBytes || !section.Truncated || countNavigationNodes(section.Sessions) > maxNavigationNodes {
		t.Fatalf("section bytes=%d truncated=%v nodes=%d", len(encoded), section.Truncated, countNavigationNodes(section.Sessions))
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(catalog)
	if len(encoded) > maxNavigationCatalogBytes || catalog.Remaining == 0 {
		t.Fatalf("catalog bytes=%d remaining=%d", len(encoded), catalog.Remaining)
	}
	first, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	second, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil || fingerprint != nextFingerprint {
		t.Fatalf("fingerprints differ: %x %x (%v)", fingerprint, nextFingerprint, err)
	}
	if len(first.(hubapi.NavigationSectionResource).Sessions) != len(second.(hubapi.NavigationSectionResource).Sessions) {
		t.Fatal("resource output changed without input change")
	}
}

func TestNavigationByteTruncatedCatalogContinuationIsContiguous(t *testing.T) {
	projects := make([]hubcore.TreeProject, 100)
	for index := range projects {
		projects[index] = hubcore.TreeProject{
			Key:        fmt.Sprintf("project-%03d-%s", index, strings.Repeat("k", maxNavigationIdentityBytes-16)),
			Name:       strings.Repeat("n", maxNavigationLabelRunes),
			WorkingDir: strings.Repeat("/", maxNavigationWorkingDirBytes),
		}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree:         hubcore.Tree{Projects: projects},
	})
	if err != nil {
		t.Fatal(err)
	}

	const offset, limit = uint32(0), uint32(100)
	first, err := projection.CatalogPage(navigationResourceProjects, offset, int(limit))
	if err != nil {
		t.Fatal(err)
	}
	firstRows := first.Projects
	if got := len(firstRows); got == 0 || got >= int(limit) {
		t.Fatalf("fixture did not force byte truncation: got %d of %d", got, limit)
	}
	nextOffset := offset + uint32(len(firstRows))
	second, err := projection.CatalogPage(navigationResourceProjects, nextOffset, int(limit))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Projects) == 0 {
		t.Fatal("second byte-truncated catalog page did not advance")
	}

	// The authoritative project order is the independent unbounded reference.
	// Its same-length prefix must match page 1 + page 2 with no duplicate or gap.
	got := append(append(hubapi.NavigationArray[hubapi.NavigationProjectSummary](nil), firstRows...), second.Projects...)
	for index, row := range got {
		want := projects[int(offset)+index].Key
		if row.Key != want {
			t.Fatalf("row %d key=%q, want contiguous reference key %q", index, row.Key, want)
		}
	}
}

func TestNavigationByteTruncatedProjectPageContinuationIsContiguous(t *testing.T) {
	rows := make([]hubcore.TreeNode, 50)
	for root := range rows {
		// Forty roots of fifty total nodes exactly reach the node ceiling, so
		// any missing top-level root below is caused by the byte envelope.
		children := make([]hubcore.TreeNode, 49)
		for child := range children {
			children[child] = hubcore.TreeNode{
				ID:      fmt.Sprintf("session-%03d-%03d", root, child),
				Title:   strings.Repeat("t", maxNavigationTitleRunes),
				Project: strings.Repeat("p", maxNavigationLabelRunes),
				Branch:  strings.Repeat("b", maxNavigationLabelRunes),
				Kind:    "subagent",
				State:   "idle",
			}
		}
		rows[root] = hubcore.TreeNode{
			ID:       fmt.Sprintf("session-root-%03d", root),
			Title:    strings.Repeat("t", maxNavigationTitleRunes),
			Project:  strings.Repeat("p", maxNavigationLabelRunes),
			Branch:   strings.Repeat("b", maxNavigationLabelRunes),
			Kind:     "session",
			State:    "idle",
			Children: children,
		}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{
		GenerationID: "generation",
		Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{
			Key:     "project",
			Name:    "project",
			Current: rows,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	const offset, limit = uint32(0), uint32(40)
	first, err := projection.ProjectPage("project", "current", offset, int(limit))
	if err != nil {
		t.Fatal(err)
	}
	firstRows := first.Sessions
	if got := len(firstRows); got == 0 || got >= int(limit) {
		t.Fatalf("fixture did not force byte truncation: got %d of %d", got, limit)
	}
	nextOffset := offset + uint32(len(firstRows))
	second, err := projection.ProjectPage("project", "current", nextOffset, int(limit))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) == 0 {
		t.Fatal("second byte-truncated project page did not advance")
	}

	// The authoritative tier order is the independent unbounded reference. Its
	// same-length prefix must match page 1 + page 2 with no duplicate or gap.
	got := append(append(hubapi.NavigationArray[hubapi.NavigationSessionSummary](nil), firstRows...), second.Sessions...)
	for index, row := range got {
		want := "local:" + rows[int(offset)+index].ID
		if row.Ref != want {
			t.Fatalf("row %d ref=%q, want contiguous reference ref %q", index, row.Ref, want)
		}
	}
}

func TestNavigationProjectionValidatesIdentitiesAndTruncatesWorkingDir(t *testing.T) {
	badUTF8 := string([]byte{0xff})
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: badUTF8}); err == nil {
		t.Fatal("invalid UTF-8 generation accepted")
	}
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Sources: []hubapi.Source{{ID: "source", Label: "label", Kind: badUTF8}}}); err == nil {
		t.Fatal("invalid UTF-8 source kind accepted")
	}
	if _, err := buildNavigationProjection(navigationBuildInputs{GenerationID: strings.Repeat("g", maxNavigationIdentityBytes+1)}); err == nil {
		t.Fatal("overlength generation accepted")
	}
	// An over-limit working dir is sanitized at the wire boundary.
	oversizedDir := strings.Repeat("界", maxNavigationWorkingDirBytes)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", WorkingDir: oversizedDir}}}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := projection.CatalogPage(navigationResourceProjects, 0, 1)
	if err != nil || len(catalog.Projects) != 1 || len(catalog.Projects[0].WorkingDir) > maxNavigationWorkingDirBytes {
		t.Fatalf("over-limit working directory was not sanitized: %#v, %v", catalog, err)
	}
	// A within-limit working dir passes validation and is safely bounded in the catalog.
	workingDir := strings.Repeat("/", maxNavigationWorkingDirBytes)
	projection, err = buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Projects: []hubcore.TreeProject{{Key: "project", Name: "project", WorkingDir: workingDir}}}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = projection.CatalogPage(navigationResourceProjects, 0, 1)
	if err != nil || len(catalog.Projects) != 1 || len(catalog.Projects[0].WorkingDir) > maxNavigationWorkingDirBytes {
		t.Fatalf("working dir was not safely truncated: %#v, %v", catalog, err)
	}
}

func TestNavigationProjectionCapsChildrenAndPreservesRowFields(t *testing.T) {
	children := make([]hubcore.TreeNode, maxNavigationChildren+1)
	for index := range children {
		children[index] = hubcore.TreeNode{ID: fmt.Sprintf("session-child-%03d", index), Title: "child", Kind: "subagent", State: "ended"}
	}
	updated := time.Unix(123, 0).UTC()
	root := hubcore.TreeNode{ID: "session-root", Title: "title", Project: "project", Branch: "branch", State: "awaiting", Kind: "session", ClusterCount: 2, AskPending: true, Dormant: true, UpdatedAt: updated, MoreSubagents: 3, Children: children}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{root}}, Live: map[string]bool{"session-root": true}, Renameable: map[string]bool{"session-root": true}, SessionFavorite: map[string]bool{"session-root": true}})
	if err != nil {
		t.Fatal(err)
	}
	row := projection.LivePage(0, 50).Sessions[0]
	if len(row.Children) != maxNavigationChildren || row.OmittedDescendants != 1 {
		t.Fatalf("children=%d omitted=%d", len(row.Children), row.OmittedDescendants)
	}
	if row.Ref != "local:session-root" || row.HostID != "local" || row.SessionID != "session-root" || row.Title != root.Title || row.Project != root.Project || row.State != root.State || row.Kind != root.Kind || row.Branch != root.Branch || row.ClusterCount != root.ClusterCount || !row.Favorite || !row.Rename || !row.Live || !row.AskPending || !row.Dormant || row.UpdatedAt == nil || !row.UpdatedAt.Equal(updated) || row.MoreSubagents != root.MoreSubagents {
		t.Fatalf("row fields diverged: %#v", row)
	}
}

func TestNavigationProjectionSnapshotsAuthoritativeTierRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	metas := make([]schema.SessionMeta, 0, 60)
	for index := range 60 {
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("session-snapshot-%03d", index), CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w/snapshot"}})
	}
	tree := hubcore.BuildTreeAt(metas, nil, map[hubcore.ArchiveKey]bool{}, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree})
	if err != nil {
		t.Fatal(err)
	}
	key := tree.Projects[0].Key
	before, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: key, Tier: "current", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := tree.Projects[0].TierRows("current")
	rows[0].Title = "mutated authoritative source"
	after, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: key, Tier: "current", Limit: 50})
	if err != nil || fingerprint != nextFingerprint || before.(hubapi.NavigationProjectPage).Sessions[0].Title != after.(hubapi.NavigationProjectPage).Sessions[0].Title {
		t.Fatalf("authoritative mutation changed projection: %x %x %v", fingerprint, nextFingerprint, err)
	}
	location, ok := projection.Location(before.(hubapi.NavigationProjectPage).Sessions[0].Ref)
	if !ok || location.Session == nil || location.Session.Title == "mutated authoritative source" {
		t.Fatalf("authoritative mutation changed location: %#v", location)
	}
}

func TestNavigationProjectionFittingUsesLogarithmicEnvelopeProbes(t *testing.T) {
	roots := oversizeNavigationRoots()
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots}})
	if err != nil {
		t.Fatal(err)
	}
	originalMarshal := navigationEnvelopeMarshal
	probes := 0
	navigationEnvelopeMarshal = func(value any) ([]byte, error) {
		probes++
		return json.Marshal(value)
	}
	defer func() { navigationEnvelopeMarshal = originalMarshal }()
	section := projection.LivePage(0, 50)
	encoded, err := json.Marshal(section)
	if err != nil || len(encoded) > maxNavigationResponseBytes || !section.Truncated {
		t.Fatalf("invalid bounded section bytes=%d truncated=%v err=%v", len(encoded), section.Truncated, err)
	}
	if probes > 14 {
		t.Fatalf("full-envelope probes=%d, want logarithmic bound <=14", probes)
	}
}

func TestNavigationProjectionCutsTwoThousandNodesBeforeByteLimit(t *testing.T) {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-node-%03d-%03d", root, child), Title: "small", Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-node-root-%03d", root), Title: "small", Kind: "session", State: "idle", Children: children}
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: roots}})
	if err != nil {
		t.Fatal(err)
	}
	section := projection.LivePage(0, 50)
	encoded, _ := json.Marshal(section)
	if got := countNavigationNodes(section.Sessions); got != maxNavigationNodes || !section.Truncated || len(encoded) >= maxNavigationResponseBytes {
		t.Fatalf("nodes=%d truncated=%v bytes=%d", got, section.Truncated, len(encoded))
	}
}

func TestNavigationProjectionFingerprintSurvivesReturnedOutputMutation(t *testing.T) {
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.Tree{Live: []hubcore.TreeNode{{ID: "session", Title: "before", Kind: "session", State: "idle"}}}})
	if err != nil {
		t.Fatal(err)
	}
	resource, fingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	resource.(hubapi.NavigationSectionResource).Sessions[0].Title = "mutated returned output"
	_, nextFingerprint, err := projection.Resource(navigationResourceKey{Kind: navigationResourceLive, Limit: 50})
	if err != nil || fingerprint != nextFingerprint {
		t.Fatalf("returned output changed fingerprint: %x %x %v", fingerprint, nextFingerprint, err)
	}
}

func oversizeNavigationRoots() []hubcore.TreeNode {
	roots := make([]hubcore.TreeNode, 40)
	for root := range roots {
		children := make([]hubcore.TreeNode, 50)
		for child := range children {
			children[child] = hubcore.TreeNode{ID: fmt.Sprintf("session-log-%03d-%03d", root, child), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "subagent", State: "idle"}
		}
		roots[root] = hubcore.TreeNode{ID: fmt.Sprintf("session-log-root-%03d", root), Title: strings.Repeat("t", maxNavigationTitleRunes), Project: strings.Repeat("p", maxNavigationLabelRunes), Branch: strings.Repeat("b", maxNavigationLabelRunes), Kind: "session", State: "idle", Children: children}
	}
	return roots
}

func deepNavigationNode(depth int) hubcore.TreeNode {
	node := hubcore.TreeNode{ID: fmt.Sprintf("session-%02d", depth), Title: "node", Kind: "subagent", State: "idle"}
	if depth > 1 {
		node.Children = []hubcore.TreeNode{deepNavigationNode(depth - 1)}
	}
	return node
}

func countNavigationNodes(rows []hubapi.NavigationSessionSummary) int {
	count := 0
	var visit func([]hubapi.NavigationSessionSummary)
	visit = func(nodes []hubapi.NavigationSessionSummary) {
		for _, node := range nodes {
			count++
			visit(node.Children)
		}
	}
	visit(rows)
	return count
}

func navigationDepth(rows []hubapi.NavigationSessionSummary) int {
	maxDepth := 0
	var visit func([]hubapi.NavigationSessionSummary, int)
	visit = func(nodes []hubapi.NavigationSessionSummary, depth int) {
		for _, node := range nodes {
			if depth > maxDepth {
				maxDepth = depth
			}
			visit(node.Children, depth+1)
		}
	}
	visit(rows, 1)
	return maxDepth
}

// TestCloneNavigationLiveEntriesOwnsWatches proves the navigation-input clone
// deep-copies each live entry's watch list. Without the Watches line the clone
// shares the roster's backing slices, so mutating the original's delivery
// instants (or appending a watch) would reach the cloned projection.
func TestCloneNavigationLiveEntriesOwnsWatches(t *testing.T) {
	original := []hubcore.LiveEntry{{
		Watches: []appwire.EvenerWatchInfo{{
			ID:            "w1",
			Source:        "self",
			Cadence:       []appwire.EvenerWatchCadence{{Kind: "every", Seconds: 10}},
			Events:        []string{"assistant.tool"},
			DeliveryTimes: []string{"1970-01-01T00:16:40Z"},
			Active:        true,
		}},
	}}
	clone := cloneNavigationLiveEntries(original)

	if !reflect.DeepEqual(clone, original) {
		t.Fatalf("clone = %+v, want a copy of %+v", clone, original)
	}
	original[0].Watches[0].DeliveryTimes[0] = "mutated"
	original[0].Watches[0].Cadence[0].Kind = "mutated"
	original[0].Watches[0].Events[0] = "mutated"
	if clone[0].Watches[0].DeliveryTimes[0] != "1970-01-01T00:16:40Z" ||
		clone[0].Watches[0].Cadence[0].Kind != "every" ||
		clone[0].Watches[0].Events[0] != "assistant.tool" {
		t.Fatalf("clone watch changed through the original: %+v", clone[0].Watches[0])
	}
	original[0].Watches = append(original[0].Watches, appwire.EvenerWatchInfo{ID: "w2"})
	if len(clone[0].Watches) != 1 {
		t.Fatalf("appending to the original's watches changed the clone: %d rows", len(clone[0].Watches))
	}
	if cloneNavigationLiveEntries(nil) != nil {
		t.Fatal("cloneNavigationLiveEntries(nil) must stay nil")
	}
}

// The projection build must honor cancellation inside the duplicate-Key merge,
// not only around it. navigationMergeProjectBucketsContext folds every tree
// group that collides on a wire Key into one catalog row, and a tree whose
// groups all present the shared "no-project" Key - the shape one attached host
// produces - makes that merge the largest walk in the build. A context that
// goes away mid-merge must surface there, instead of letting the merge fold
// every group to completion first.
//
// The flip point is calibrated the way the sibling tombstone test's is
// (TestNavigationNextStatesChecksContextWhileCreatingTombstones): on this
// fixture the pre-merge walks (validate, clone, pin candidates) make 525
// ctx.Err() calls and the post-merge walks (catalog map, pin sections, location
// index) make 129, while the merge region between them contributes none of its
// own - so a limit of 690 can only be reached by checks the merge itself makes.
// The arithmetic is fixture-coupled on purpose: it pins that the merge carries
// its own loop-boundary checks rather than leaning on the walks around it.
func TestNavigationProjectionChecksContextWhileMergingDuplicateKeys(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	const groups = 40
	projects := make([]hubcore.TreeProject, 0, groups)
	for index := range groups {
		updated := now.Add(-time.Duration(index) * time.Minute)
		projects = append(projects, hubcore.TreeProject{
			Key:  "no-project",
			Name: fmt.Sprintf("unresolved-%02d", index),
			Current: []hubcore.TreeNode{{
				ID:        fmt.Sprintf("session-merge-cancel-%02d", index),
				Title:     "row",
				Project:   "no-project",
				Kind:      "session",
				State:     "idle",
				CreatedAt: updated,
				UpdatedAt: updated,
			}},
		})
	}
	inputs := navigationBuildInputs{GenerationID: "generation", Revision: 1, Tree: hubcore.Tree{Projects: projects}}
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if merged := projection.projects["no-project"]; len(merged.Current) != groups {
		t.Fatalf("merged project holds %d current rows, want all %d groups folded into one row", len(merged.Current), groups)
	}

	ctx := &cancelAfterChecksContext{Context: context.Background(), limit: 690}
	if _, err := buildNavigationProjectionContext(ctx, inputs); !errors.Is(err, context.Canceled) {
		t.Fatalf("projection error = %v, want cancellation while merging duplicate keys", err)
	}
	if got := ctx.checks.Load(); got > ctx.limit+2 {
		t.Fatalf("projection continued after cancellation: %d checks", got)
	}
}

// The four functions below are the merge this file used before the single-pass
// rewrite, copied verbatim: each later group that collides on a Key folded into
// the accumulated row one pair at a time. They are the equivalence reference
// for navigationMergeProjectGroupsContext - production now folds all of a Key's
// groups in one pass, and
// TestNavigationMergeProjectGroupsSinglePassMatchesPairwiseFold holds the
// two to identical output. Keep them in sync with the merge's semantics or
// delete them together with that test.
func navigationMergeProjectGroupsPairwiseReference(projects []hubcore.TreeProject) []hubcore.TreeProject {
	at := make(map[string]int, len(projects))
	out := make([]hubcore.TreeProject, 0, len(projects))
	for _, project := range projects {
		if index, ok := at[project.Key]; ok {
			out[index] = navigationMergeProjectPairwiseReference(out[index], project)
			continue
		}
		at[project.Key] = len(out)
		out = append(out, project)
	}
	return out
}

func navigationMergeProjectPairwiseReference(first, next hubcore.TreeProject) hubcore.TreeProject {
	lastActivity, age := first.LastActivity, first.Age
	if next.LastActivity.After(lastActivity) {
		lastActivity, age = next.LastActivity, next.Age
	}
	rollupState := first.RollupState
	if hubapi.RollupRank(next.RollupState) > hubapi.RollupRank(rollupState) {
		rollupState = next.RollupState
	}
	mergedTiers := navigationMergeClusterRowsReference(
		navigationMergeProjectTierPairwiseReference(first, next, "current"),
		navigationMergeProjectTierPairwiseReference(first, next, "recent"),
		navigationMergeProjectTierPairwiseReference(first, next, "archived"),
	)
	current, recent, archived := mergedTiers[0], mergedTiers[1], mergedTiers[2]
	return hubcore.TreeProject{
		Name:         first.Name,
		Key:          first.Key,
		WorkingDir:   first.WorkingDir,
		Current:      current,
		Recent:       recent,
		Archived:     archived,
		IsArchived:   first.IsArchived,
		IsTestRun:    first.IsTestRun,
		LastActivity: lastActivity,
		RollupState:  rollupState,
		RollupLive:   first.RollupLive + next.RollupLive,
		RollupAttn:   first.RollupAttn + next.RollupAttn,
		Sources:      navigationMergeProjectSources(first.Sources, next.Sources),
		Expanded:     first.Expanded || next.Expanded,
		MoreCurrent:  navigationTierOverflow(len(current), hubcore.SidebarSessionPageSize),
		MoreRecent:   navigationTierOverflow(len(recent), hubcore.SidebarSessionPageSize),
		MoreArchived: navigationTierOverflow(len(archived), hubcore.SidebarSessionPageSize),
		Age:          age,
		Worktrees:    first.Worktrees + next.Worktrees,
	}
}

func navigationMergeProjectTierPairwiseReference(first, next hubcore.TreeProject, tier string) []hubcore.TreeNode {
	firstRows, _ := first.TierRows(tier)
	nextRows, _ := next.TierRows(tier)
	rows := make([]hubcore.TreeNode, 0, len(firstRows)+len(nextRows))
	rows = append(rows, firstRows...)
	rows = append(rows, nextRows...)
	sort.SliceStable(rows, func(i, j int) bool { return navigationTreeNodeLess(rows[i], rows[j]) })
	return rows
}

func navigationMergeClusterRowsReference(tiers ...[]hubcore.TreeNode) [][]hubcore.TreeNode {
	merged := make(map[string]hubcore.TreeNode)
	latest := make(map[string]time.Time)
	winnerTier := make(map[string]int)
	winnerIndex := make(map[string]int)
	collided := false
	for tierIndex, rows := range tiers {
		for rowIndex, row := range rows {
			if row.Kind != "cluster" || row.ID == "" {
				continue
			}
			previous, seen := merged[row.ID]
			if !seen {
				merged[row.ID] = row
				latest[row.ID] = row.UpdatedAt
				winnerTier[row.ID] = tierIndex
				winnerIndex[row.ID] = rowIndex
				continue
			}
			collided = true
			merged[row.ID] = navigationMergeClusterRowPairwiseReference(previous, row)
			if row.UpdatedAt.After(latest[row.ID]) {
				latest[row.ID] = row.UpdatedAt
				winnerTier[row.ID] = tierIndex
				winnerIndex[row.ID] = rowIndex
			}
		}
	}
	if !collided {
		return tiers
	}
	out := make([][]hubcore.TreeNode, len(tiers))
	for tierIndex, rows := range tiers {
		kept := make([]hubcore.TreeNode, 0, len(rows))
		for rowIndex, row := range rows {
			if row.Kind == "cluster" && row.ID != "" {
				if winnerTier[row.ID] != tierIndex || winnerIndex[row.ID] != rowIndex {
					continue // folded into the identity's one surviving row
				}
				row = merged[row.ID]
			}
			kept = append(kept, row)
		}
		out[tierIndex] = kept
	}
	return out
}

// navigationMergeClusterRowPairwiseReference is the pre-fix pairwise cluster
// fold, copied verbatim: two colliding rows union into a fresh slice that is
// copied and re-sorted on every fold.
func navigationMergeClusterRowPairwiseReference(previous, next hubcore.TreeNode) hubcore.TreeNode {
	union := previous
	union.Children = append(append(make([]hubcore.TreeNode, 0, len(previous.Children)+len(next.Children)), previous.Children...), next.Children...)
	sort.SliceStable(union.Children, func(i, j int) bool { return navigationTreeNodeLess(union.Children[i], union.Children[j]) })
	union.ClusterCount = previous.ClusterCount + next.ClusterCount
	if next.UpdatedAt.After(previous.UpdatedAt) {
		union.UpdatedAt, union.Age = next.UpdatedAt, next.Age
	}
	return union
}

// The single-pass merge must produce exactly the value the pairwise fold did:
// the rewrite changed the merge's shape - every colliding group now reaches
// one navigationMergeProjectContext call, so each tier is concatenated and
// sorted once instead of re-sorted after every fold - but not its output. The
// fixture covers the shapes where the two could drift apart: cluster ids that
// collide within a tier and across tiers - three occurrences of one id in one
// tier, with tied UpdatedAt deciding the surviving row and tied members
// deciding the folded children's order by encounter order - session rows whose
// comparator keys tie exactly (so only the stable sort's input order - the
// tree's own group order - fixes their merged position), a tier that overflows
// the sidebar cap, scalar folds with LastActivity ties, and a bucket whose
// Keys are unique (the untouched fast path).
func TestNavigationMergeProjectGroupsSinglePassMatchesPairwiseFold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	row := func(id string, age time.Duration) hubcore.TreeNode {
		return hubcore.TreeNode{ID: id, Title: id, Project: "no-project", Kind: "session", State: "idle", CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
	}
	cluster := func(id string, age time.Duration, members ...hubcore.TreeNode) hubcore.TreeNode {
		return hubcore.TreeNode{ID: id, Title: "repeated title", Project: "no-project", Kind: "cluster", State: "idle", ClusterCount: len(members), Children: members, CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
	}
	overflow := make([]hubcore.TreeNode, 0, hubcore.SidebarSessionPageSize+10)
	for index := range hubcore.SidebarSessionPageSize + 10 {
		overflow = append(overflow, row(fmt.Sprintf("r-%03d", index), time.Duration(25+index)*time.Minute))
	}
	buckets := navigationProjectBucket{
		active: []hubcore.TreeProject{
			{
				Key: "no-project", Name: "one", Sources: []string{"b", "a"},
				RollupState: "idle", RollupLive: 2, RollupAttn: 1, Worktrees: 1,
				LastActivity: now.Add(-time.Hour), Age: "1h",
				Current: []hubcore.TreeNode{
					cluster("cluster-shared", 6*time.Minute, row("c1-a", 7*time.Minute), row("c1-b", 8*time.Minute)),
					row("tied-row", 10*time.Minute),
				},
				Recent: []hubcore.TreeNode{row("a-r0", 26*time.Hour)},
			},
			{
				Key: "no-project", Name: "two", Sources: []string{"a", "c"},
				RollupState: "warning", RollupLive: 1, RollupAttn: 3, Worktrees: 2, Expanded: true,
				LastActivity: now.Add(-2 * time.Hour), Age: "2h",
				Current: []hubcore.TreeNode{
					cluster("cluster-shared", 5*time.Minute, row("c2-a", 9*time.Minute)),
					cluster("cluster-shared", 5*time.Minute, row("tied-member", 9*time.Minute)),
					row("tied-row", 10*time.Minute),
					row("b-0", 10*time.Minute),
				},
				Recent:   overflow,
				Archived: []hubcore.TreeNode{cluster("cluster-cross", 3*time.Hour, row("c2-ax", 3*time.Hour)), row("b-ax", 4*time.Hour)},
			},
			{
				Key: "no-project", Name: "three", Sources: []string{"b"},
				// LastActivity ties group one's, so the merged Age must stay
				// group one's, not this group's.
				RollupState: "errored", LastActivity: now.Add(-time.Hour), Age: "3h",
				Current: []hubcore.TreeNode{
					cluster("cluster-shared", 4*time.Minute, row("c3-m", 12*time.Minute), row("tied-member", 9*time.Minute)),
					row("c-0", 10*time.Minute),
				},
				Recent:   []hubcore.TreeNode{cluster("cluster-cross", 2*time.Hour, row("c3-r", 2*time.Hour))},
				Archived: []hubcore.TreeNode{cluster("cluster-arch", 3*time.Hour, row("c3-a0", 3*time.Hour))},
			},
			{Key: "solo", Name: "solo", Current: []hubcore.TreeNode{row("s-0", time.Minute)}},
		},
		archived: []hubcore.TreeProject{
			{Key: "dupe", Name: "one", IsArchived: true, Current: []hubcore.TreeNode{cluster("arch-cluster", time.Hour, row("z-0", time.Hour))}},
			{Key: "dupe", Name: "two", IsArchived: true, Current: []hubcore.TreeNode{cluster("arch-cluster", time.Hour, row("z-1", 30*time.Minute)), row("z-2", 45*time.Minute)}},
		},
		testRuns: []hubcore.TreeProject{
			{Key: "test", Name: "only", IsTestRun: true, Current: []hubcore.TreeNode{row("t-0", time.Minute)}},
		},
	}

	merged, err := navigationMergeProjectBucketsContext(context.Background(), buckets)
	if err != nil {
		t.Fatal(err)
	}

	// The fixture is not vacuous: colliding Keys actually collapsed, the recent
	// tier overflowed the cap, cluster rows folded across tiers and groups, and
	// the tied rows kept group order.
	if len(merged.active) != 2 || len(merged.archived) != 1 || len(merged.testRuns) != 1 {
		t.Fatalf("bucket sizes = %d/%d/%d, want 2/1/1", len(merged.active), len(merged.archived), len(merged.testRuns))
	}
	collapsed := merged.active[0]
	if collapsed.Key != "no-project" || collapsed.MoreRecent == 0 {
		t.Fatalf("collapsed row = Key %q MoreRecent %d, want the overflowed no-project union", collapsed.Key, collapsed.MoreRecent)
	}
	clusters, tied := 0, 0
	for _, row := range collapsed.Current {
		if row.ID == "cluster-shared" {
			clusters++
			if row.ClusterCount != 6 {
				t.Errorf("cluster-shared count = %d, want all 6 members from the three occurrences", row.ClusterCount)
			}
			wantMembers := []string{"c1-a", "c1-b", "c2-a", "tied-member", "tied-member", "c3-m"}
			got := make([]string, len(row.Children))
			for index, child := range row.Children {
				got[index] = child.ID
			}
			if !reflect.DeepEqual(got, wantMembers) {
				t.Errorf("cluster-shared members = %v, want %v (recency order, ties keeping encounter order)", got, wantMembers)
			}
			if row.UpdatedAt != now.Add(-4*time.Minute) {
				t.Errorf("cluster-shared UpdatedAt = %s, want the latest occurrence's", row.UpdatedAt)
			}
		}
		if row.ID == "tied-row" {
			tied++
		}
	}
	if clusters != 1 {
		t.Errorf("cluster-shared rows = %d, want one reconciled row", clusters)
	}
	if tied != 2 {
		t.Errorf("tied rows = %d, want both retained", tied)
	}
	var crossTier string
	for _, row := range collapsed.Recent {
		if row.ID == "cluster-cross" {
			crossTier = "recent"
		}
	}
	for _, row := range collapsed.Archived {
		if row.ID == "cluster-cross" {
			crossTier = "archived"
		}
	}
	if crossTier != "recent" {
		t.Errorf("cluster-cross survived in %q, want recent (the later of the colliding moments)", crossTier)
	}
	if collapsed.Age != "1h" {
		t.Errorf("merged Age = %s, want group one's (LastActivity ties keep the first-seen Age)", collapsed.Age)
	}

	reference := navigationProjectBucket{
		active:   navigationMergeProjectGroupsPairwiseReference(buckets.active),
		archived: navigationMergeProjectGroupsPairwiseReference(buckets.archived),
		testRuns: navigationMergeProjectGroupsPairwiseReference(buckets.testRuns),
	}
	for _, bucket := range []struct {
		name   string
		merged []hubcore.TreeProject
		want   []hubcore.TreeProject
	}{
		{"active", merged.active, reference.active},
		{"archived", merged.archived, reference.archived},
		{"testRuns", merged.testRuns, reference.testRuns},
	} {
		if len(bucket.merged) != len(bucket.want) {
			t.Fatalf("%s bucket = %d rows, reference fold = %d", bucket.name, len(bucket.merged), len(bucket.want))
		}
		for index := range bucket.want {
			if !reflect.DeepEqual(bucket.merged[index], bucket.want[index]) {
				t.Fatalf("%s bucket project %d (%q) diverged from the pairwise fold:\nmerged:    %#v\nreference: %#v", bucket.name, index, bucket.want[index].Key, bucket.merged[index], bucket.want[index])
			}
		}
	}
}
