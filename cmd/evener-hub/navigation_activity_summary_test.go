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
	projection, err := buildNavigationProjection(t.Context(), navigationBuildInputs{GenerationID: "g", Tree: hubcore.Tree{Live: []hubcore.TreeNode{node}}})
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
	projection, err := buildNavigationProjection(t.Context(), navigationBuildInputs{GenerationID: "g", Revision: 1, Tree: hubcore.Tree{Live: rows}})
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

// rowWithRunningJobs projects one live session carrying the given jobs and
// returns its summary row, the shape every running-job case below inspects.
func rowWithRunningJobs(t *testing.T, jobs ...appwire.EvenerJobInfo) hubapi.NavigationSessionSummary {
	t.Helper()
	node := hubcore.TreeNode{ID: "root", Kind: "session", State: "active", RunningJobs: jobs}
	projection, err := buildNavigationProjection(t.Context(), navigationBuildInputs{GenerationID: "g", Tree: hubcore.Tree{Live: []hubcore.TreeNode{node}}})
	if err != nil {
		t.Fatal(err)
	}
	page := projection.LivePage(0, 50)
	if len(page.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(page.Sessions))
	}
	return page.Sessions[0]
}

// A running job's own stated intent is the row's work text: the command says
// what is executing, the intent says why. The projector takes it from the same
// job whose command it takes, bounded like every other row why text.
func TestNavigationSummaryCarriesTheRunningJobIntent(t *testing.T) {
	row := rowWithRunningJobs(t, appwire.EvenerJobInfo{
		JobID:   "job-1",
		JobType: "shell",
		Status:  "running",
		Command: "go test ./agent/...",
		Intent:  "Checking the rail's row tests.",
	})
	if row.RunningJobIntent != "Checking the rail's row tests." {
		t.Fatalf("running_job_intent = %q, want the job's stated intent", row.RunningJobIntent)
	}
	if row.RunningJobCommand != "go test ./agent/..." {
		t.Fatalf("running_job_command = %q, want the command it belongs to", row.RunningJobCommand)
	}
}

// A command with no stated intent leaves the field absent, so a client keeps
// its command fallback rather than rendering an empty line.
func TestNavigationSummaryOmitsAnUnstatedRunningJobIntent(t *testing.T) {
	row := rowWithRunningJobs(t, appwire.EvenerJobInfo{JobID: "job-1", JobType: "shell", Status: "running", Command: "make test"})
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["running_job_intent"]; present {
		t.Fatalf("unstated intent reached the wire: %v", fields["running_job_intent"])
	}
}

// The intent is cut to the same bound the activity read uses, so one runaway
// statement cannot widen a row.
func TestNavigationSummaryBoundsTheRunningJobIntent(t *testing.T) {
	row := rowWithRunningJobs(t, appwire.EvenerJobInfo{
		JobID: "job-1", JobType: "shell", Status: "running",
		Command: "make test",
		Intent:  strings.Repeat("i", 5_000),
	})
	intent := row.RunningJobIntent
	if got := utf8.RuneCountInString(intent); got > appwire.MaxIntentRunes {
		t.Fatalf("running_job_intent is %d runes, want at most %d", got, appwire.MaxIntentRunes)
	}
	if intent == "" {
		t.Fatal("a stated intent was dropped")
	}
}

// The command and the intent come from ONE job: pairing them across jobs would
// name one command's purpose beside another's command. An intent-only job
// therefore wins the row outright and leaves the command empty.
func TestNavigationSummaryPairsCommandAndIntentFromOneJob(t *testing.T) {
	row := rowWithRunningJobs(t,
		appwire.EvenerJobInfo{JobID: "job-1", JobType: "shell", Status: "running", Intent: "Checking the rows."},
		appwire.EvenerJobInfo{JobID: "job-2", JobType: "shell", Status: "running", Command: "make test"},
	)
	if row.RunningJobIntent != "Checking the rows." {
		t.Fatalf("running_job_intent = %q, want the first job's", row.RunningJobIntent)
	}
	if row.RunningJobCommand != "" {
		t.Fatalf("running_job_command = %q, want the same job's (which stated none)", row.RunningJobCommand)
	}
}

// Job list order is not semantic, so the row's job is chosen by rule:
// foreground before background, then the lowest job ID. Two probes of one job
// set must never name different work.
func TestNavigationSummaryPicksTheRowJobByRule(t *testing.T) {
	foreground := appwire.EvenerJobInfo{JobID: "job-a", JobType: "shell", Status: "running", Command: "make test"}
	background := appwire.EvenerJobInfo{JobID: "job-b", JobType: "shell", Status: "running", Background: true, Intent: "Polling the logs."}
	// A silent job would blank the row, so it is skipped entirely.
	silent := appwire.EvenerJobInfo{JobID: "job-0", JobType: "shell", Status: "running"}
	for name, jobs := range map[string][]appwire.EvenerJobInfo{
		"foreground over background": {foreground, background},
		"reversed order":             {background, foreground},
		"a silent job":               {silent, foreground},
	} {
		row := rowWithRunningJobs(t, jobs...)
		if row.RunningJobCommand != "make test" || row.RunningJobIntent != "" {
			t.Errorf("%s: command=%q intent=%q, want the foreground job alone", name, row.RunningJobCommand, row.RunningJobIntent)
		}
	}
}
