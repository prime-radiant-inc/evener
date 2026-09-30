package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/hubapi"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestNavigationActivitySummaryCutover(t *testing.T) {
	service := newNavigationReadTestService(t)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "test"})
	registerNavigationReadHandler(server, service)
	_, err := dispatchNavigationReadRaw(t, server, `{"resource":"manifest","representationVersion":2}`)
	assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	if _, err := dispatchNavigationReadRaw(t, server, `{"resource":"manifest","representationVersion":3}`); err != nil {
		t.Fatal(err)
	}
	if _, err := navigationReadKey(appwire.NavigationReadParams{Resource: "subagents", Ref: "local:root"}); err == nil {
		t.Fatal("removed child resource accepted")
	}
}

func TestNavigationActivitySummaryHasCountsWithoutDetail(t *testing.T) {
	jobs := make([]appwire.EvenerJobInfo, 2000)
	for i := range jobs {
		jobs[i] = appwire.EvenerJobInfo{JobID: "job", Status: "running", Command: strings.Repeat("x", 8000)}
	}
	jobs[0].Command = ""
	watches := watchListForCap(2000, 700)
	node := hubcore.TreeNode{ID: "root", Kind: "session", State: "idle", RunningJobs: jobs, CompletedJobs: jobs, Watches: watches}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "g", Tree: hubcore.Tree{Live: []hubcore.TreeNode{node}}})
	if err != nil {
		t.Fatal(err)
	}
	page := projection.LivePage(0, 50)
	if len(page.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(page.Sessions))
	}
	raw, err := json.Marshal(page.Sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]float64{"running_job_count": 2000, "watch_count": 2000, "armed_watch_count": 1300} {
		if fields[name] != want {
			t.Errorf("%s=%v want%v", name, fields[name], want)
		}
	}
	command, ok := fields["running_job_command"].(string)
	if !ok || utf8.RuneCountInString(command) > maxNavigationLabelRunes || command == "" {
		t.Errorf("command bound=%d present=%v", len(command), ok)
	}
	for _, name := range []string{"running_jobs", "completed_jobs", "watches", "omitted_watches", "omitted_armed_watches", "needs_you_subagents"} {
		if _, ok := fields[name]; ok {
			t.Errorf("detail field %s emitted", name)
		}
	}
	if len(raw) > 1600 {
		t.Errorf("summary cost=%d", len(raw))
	}
	fingerprints, _, err := navigationLogicalFingerprintsContext(t.Context(), projection)
	if err != nil {
		t.Fatal(err)
	}
	for key := range fingerprints {
		if string(key.Kind) == "subagents" {
			t.Errorf("child fingerprint=%+v", key)
		}
	}
}

func TestNavigationFittingPreservesCompactCounts(t *testing.T) {
	rows := navigationMaxFieldSectionNodes(time.Unix(1_700_000_000, 0).UTC())
	for i := range rows {
		rows[i].Watches = watchListForCap(30, 10)
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "g", Revision: 1, Tree: hubcore.Tree{Live: rows}})
	if err != nil {
		t.Fatal(err)
	}
	key := navigationResourceKey{Kind: navigationResourceLive, Limit: maxNavigationSectionRows, Generation: "g", Revision: 1}
	page := projection.LivePage(0, maxNavigationSectionRows)
	response := appwire.NavigationReadResponse{Status: "ok", GenerationID: "g", Revision: 1, ETag: "etag", Representation: appwire.NavigationRepresentationSnapshot}
	full, err := normalizeNavigationResource(key, page)
	if err != nil {
		t.Fatal(err)
	}
	response.Data, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	capBytes := len(encoded) / 2
	fitted, data, err := fitNavigationV3Snapshot(key, page, response, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	response.Data = data
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > capBytes {
		t.Fatalf("response=%d cap=%d", len(encoded), capBytes)
	}
	count := 0
	for _, entity := range fitted.Entities {
		if entity.Kind != "session" {
			continue
		}
		var summary hubapi.NavigationSessionSummary
		if err := json.Unmarshal(entity.Value, &summary); err != nil {
			t.Fatal(err)
		}
		if summary.RunningJobCount != 12 || summary.WatchCount != 30 || summary.ArmedWatchCount != 20 || summary.RunningJobCommand == "" || len(summary.Children) != 0 {
			t.Fatalf("fitted summary lost own facts: %+v", summary)
		}
		count++
	}
	if count == 0 || count >= len(rows) {
		t.Fatalf("fit did not shed a nonempty prefix: %d/%d", count, len(rows))
	}
	var metadata hubapi.NavigationSectionResource
	if err := json.Unmarshal(fitted.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Remaining != len(rows)-count || !metadata.Truncated {
		t.Fatalf("metadata=%+v kept=%d", metadata, count)
	}
	if err := validateNavigationResourceSnapshot(key, "g", 1, fitted); err != nil {
		t.Fatal(err)
	}
}
