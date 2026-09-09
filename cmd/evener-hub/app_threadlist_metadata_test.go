package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/identifier"
)

func TestHubThreadListPastRowsKeepCheapMetadataWithoutHydratingDetail(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "distinctive persisted detail", Prompt: "ship it"},
	})
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatalf("load fixture metadata: %v", err)
	}
	meta.Goal = &schema.GoalSnapshot{Objective: "persisted list goal", Status: "active", Iterations: 3}
	meta.WorkMillis = 4200
	meta.CumulativeUsage = schema.CumulativeUsage{InputTokens: 600, OutputTokens: 400, TotalTokens: 1000}
	meta.VisionModel = "off"
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save fixture metadata: %v", err)
	}
	if _, err := cfg.Past.Rebuild(); err != nil {
		t.Fatalf("rebuild fixture index: %v", err)
	}

	response, err := hubThreadList(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("thread list = %+v, want one past row", response.Data)
	}
	row := response.Data[0]
	if row.ID != sessionID || row.ModelProvider != "gpt-5" || row.CWD != "/tmp/project" {
		t.Fatalf("past row metadata = %+v, want session/model/cwd preserved", row)
	}
	if row.Evener.Tasks != nil {
		t.Fatalf("past list hydrated task detail = %+v, want absent", row.Evener.Tasks)
	}
	if row.Evener.Capabilities == (appwire.ThreadCapabilities{}) {
		t.Fatal("past list dropped cheap capability metadata")
	}
	if row.Evener.Goal == nil || row.Evener.Goal.Objective != "persisted list goal" || row.Evener.WorkMillis != 4200 || row.Evener.Usage == nil || row.Evener.Usage.TotalTokens != 1000 || row.Evener.VisionModel != "off" {
		t.Fatalf("past list cheap metadata = %+v, want persisted goal/usage/work/cost/vision", row.Evener)
	}

	detail, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead = (%+v, %t, %v), want detail", detail, ok, err)
	}
	if detail.Evener.Tasks == nil || detail.Evener.Tasks.Total != 1 || detail.Evener.Tasks.Remaining != 1 {
		t.Fatalf("past thread detail = %+v, want persisted task", detail.Evener.Tasks)
	}
	if detail.Evener.Goal == nil || detail.Evener.Goal.Objective != "persisted list goal" || detail.Evener.WorkMillis != 4200 || detail.Evener.Usage == nil || detail.Evener.Usage.TotalTokens != 1000 || detail.Evener.Cost != row.Evener.Cost || detail.Evener.VisionModel != "off" {
		t.Fatalf("past thread cheap metadata = %+v, want same persisted values", detail.Evener)
	}
}

func TestPastThreadReadPreservesProjectIdentity(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, nil)
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	meta.EnvInfo.WorkingDir = t.TempDir()
	project, err := identifier.ResolveProject(meta.EnvInfo.WorkingDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead = (%t, %v), want detail", ok, err)
	}
	if thread.ProjectID != project.ID || thread.ProjectPath != project.CanonicalPath {
		t.Fatalf("project = (%q, %q), want (%q, %q)", thread.ProjectID, thread.ProjectPath, project.ID, project.CanonicalPath)
	}
}
