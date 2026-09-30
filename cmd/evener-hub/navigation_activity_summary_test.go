package hub

import (
	"encoding/json"
	"strings"
	"testing"

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
	if !ok || len(command) > maxNavigationLabelRunes || command == "" {
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
