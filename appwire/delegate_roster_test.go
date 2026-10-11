package appwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSlimDelegateForRoster(t *testing.T) {
	long := strings.Repeat("é", DelegateRosterTextMaxRunes+50)
	tests := []struct {
		name            string
		task, desc      string
		wantTask, wantD string
	}{
		{"brief stored twice keeps the Task copy", "review the diff", "review the diff", "review the diff", ""},
		{"distinct task and description both survive", "the long brief", "short label", "the long brief", "short label"},
		{"only a description survives as the description", "", "just a label", "", "just a label"},
		{"only a task survives as the task", "just a task", "", "just a task", ""},
		{"texts differing past the cap stay distinct", "a" + long, "b" + long, "a" + strings.Repeat("é", DelegateRosterTextMaxRunes-2) + "…", "b" + strings.Repeat("é", DelegateRosterTextMaxRunes-2) + "…"},
		{"long brief is cut on a rune boundary once", long, long, strings.Repeat("é", DelegateRosterTextMaxRunes-1) + "…", ""},
		{"text at the cap is untouched", strings.Repeat("a", DelegateRosterTextMaxRunes), "", strings.Repeat("a", DelegateRosterTextMaxRunes), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SlimDelegateForRoster(EvenerDelegateInfo{Task: tc.task, Description: tc.desc})
			if got.Task != tc.wantTask || got.Description != tc.wantD {
				t.Fatalf("task=%q description=%q, want task=%q description=%q", got.Task, got.Description, tc.wantTask, tc.wantD)
			}
		})
	}
}

func TestSlimDelegateForRosterKeepsEverythingElse(t *testing.T) {
	running := int64(9)
	valid := true
	in := EvenerDelegateInfo{
		DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child", Lifecycle: "idle", Status: "idle",
		Outcome: "completed", Terminal: true, Resumable: true, ProjectionRevision: 5, AgentType: "explorer", Model: "m",
		RunningForMS: &running, StructuredValid: &valid, StructuredReason: "why", Warnings: []string{"w"},
		Diagnostics: []string{"d"}, Usage: &EvenerUsage{InputTokens: 3}, PacketKind: "final",
		Message: json.RawMessage(`"done"`), StructuredResult: json.RawMessage(`{"a":1}`),
		ReportPreview: "done", ReportPreviewTruncated: true, LogicalOwnerSessionID: "owner-session",
	}
	got := SlimDelegateForRoster(in)
	if len(got.Message) != 0 || len(got.StructuredResult) != 0 || got.PacketKind != "" {
		t.Fatalf("final packet payload survived: %+v", got)
	}
	if got.ReportPreview != "" || got.ReportPreviewTruncated || got.LogicalOwnerSessionID != "" {
		t.Fatalf("activity-store fields survived the roster: %+v", got)
	}
	want := in
	want.Message, want.StructuredResult, want.PacketKind = nil, nil, ""
	want.ReportPreview, want.ReportPreviewTruncated, want.LogicalOwnerSessionID = "", false, ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slim row = %+v, want %+v", got, want)
	}
	if len(in.Message) == 0 {
		t.Fatal("input row was mutated through its shared slices")
	}
}

// The roster carries no report payload and no activity-scoping identity, so its
// wire form must never contain the preview or logical-owner keys.
func TestSlimDelegateForRosterOmitsPreviewKeys(t *testing.T) {
	in := EvenerDelegateInfo{
		DelegateID: "dlg_1", OwnerSessionID: "root", ChildSessionID: "child", Type: "delegate",
		RunGeneration: 1, ProjectionRevision: 3, PacketKind: "reported",
		Message: json.RawMessage(`"done"`), ReportPreview: "done", ReportPreviewTruncated: true,
		LogicalOwnerSessionID: "owner-session",
	}
	encoded, err := json.Marshal(SlimDelegateForRoster(in))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reportPreview", "reportPreviewTruncated", "logicalOwnerSessionId"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("roster wire carries %q: %s", key, encoded)
		}
	}
}
