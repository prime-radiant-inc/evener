package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityDelegateNameSurvivesRetainedRead(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"inspect-cache", strings.Repeat("界", 220), ""} {
		t.Run(name[:min(len(name), 24)], func(t *testing.T) {
			s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md")}))
			descriptor := stableReadonlyDescriptor(s, "dlg_named")
			descriptor.Name = name
			descriptor.Task = "Inspect the complete cache ownership and repair plan"
			at := time.Unix(100, 0).UTC()
			seedStableReadonlyFinish(t, s, "dlg_named", descriptor, at, stableDelegateFinishFromRun(delegateTerminalRunInputs{descriptor: descriptor, result: "Report remains available", communicated: true, startedAt: at, endedAt: at.Add(time.Second)}), true)
			params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
			live, err := s.ListActivityDelegates(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			key := s.stateDir + "\x00" + s.ID()
			sessionActivityIndexes.Lock()
			if cached := sessionActivityIndexes.entries[key]; cached != nil {
				delete(sessionActivityIndexes.entries, key)
				sessionActivityIndexes.order.Remove(cached.element)
			}
			sessionActivityIndexes.Unlock()
			retained, err := LoadSessionActivityDelegates(t.Context(), s.stateDir, s.ID(), params)
			if err != nil {
				t.Fatal(err)
			}
			for _, page := range []appwire.SessionDelegatesResponse{live, retained} {
				if len(page.Delegates) != 1 {
					t.Fatalf("rows=%d", len(page.Delegates))
				}
				row := page.Delegates[0]
				raw, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				want := name
				if len([]rune(name)) > 200 {
					want = strings.Repeat("界", 199) + "…"
				}
				if name == "" {
					if _, exists := fields["name"]; exists {
						t.Fatal("unnamed descriptor acquired a display name")
					}
				} else if fields["name"] != want {
					t.Fatalf("name=%v, want %q", fields["name"], want)
				}
				if row.Task != descriptor.Task || row.RunGeneration != 1 || row.ReportPreview != "Report remains available" {
					t.Fatalf("name changed delegate facts: %+v", row)
				}
			}
		})
	}
}

func contextNameFields(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestSessionActivityContextDelegateNamesLiveAndRetained(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 2, AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	parent := stableToolDescriptor(s, "dlg_parent", "")
	parent.Name, parent.Description = "same name", "Parent description, not the name"
	leaf := stableToolDescriptor(s, "dlg_leaf", "dlg_parent")
	leaf.Name, leaf.Description = strings.Repeat("界", 220), "Leaf description, not the name"
	sibling := stableToolDescriptor(s, "dlg_sibling", "")
	sibling.Name, sibling.Description = parent.Name, "Sibling description, not the name"
	unnamed := stableToolDescriptor(s, "dlg_unnamed", "")
	unnamed.Description = "Unnamed description, not a name"
	s.delegateController.mu.Lock()
	_, err := s.delegateController.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_parent", TS: at, Created: &delegatestore.DelegateCreated{Descriptor: parent}},
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_leaf", TS: at.Add(time.Second), Created: &delegatestore.DelegateCreated{Descriptor: leaf}},
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_sibling", TS: at.Add(2 * time.Second), Created: &delegatestore.DelegateCreated{Descriptor: sibling}},
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_unnamed", TS: at.Add(3 * time.Second), Created: &delegatestore.DelegateCreated{Descriptor: unnamed}},
	)
	s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []delegatestore.Descriptor{parent, leaf, sibling, unnamed} {
		savePastActivityMetaWithTreeRevision(t, s.stateDir, d.ChildSessionID, "Thread title, not delegate name", s.ID(), 0)
	}
	ref := encodeRef("", s.ID())
	rows, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree})
	if err != nil || len(rows.Delegates) != 4 {
		t.Fatalf("canonical rows = %+v, error %v", rows, err)
	}
	byID := make(map[string]appwire.SessionDelegate)
	for _, row := range rows.Delegates {
		byID[row.DelegateID] = row
	}
	if byID["dlg_parent"].Name != byID["dlg_sibling"].Name || byID["dlg_parent"].DelegateID == byID["dlg_sibling"].DelegateID {
		t.Fatal("same-name distinct delegate fixture lost identity")
	}
	if byID["dlg_leaf"].Name != strings.Repeat("界", 199)+"…" {
		t.Fatalf("canonical Unicode row bound = %q", byID["dlg_leaf"].Name)
	}
	root, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: ref})
	if err != nil || !root.Context.AncestryKnown || root.Context.DelegateID != "" || contextNameFields(t, root.Context)["delegateName"] != nil {
		t.Fatalf("root acquired delegate metadata: %+v, %v", root.Context, err)
	}
	read := func(childID string, retained bool) appwire.SessionActivityContext {
		t.Helper()
		params := appwire.SessionActivityReadParams{Ref: encodeRef("", childID)}
		var summary appwire.SessionActivitySummary
		var err error
		if retained {
			summary, err = LoadSessionActivitySummary(t.Context(), s.stateDir, childID, params)
		} else {
			summary, err = s.ActivitySummary(t.Context(), params)
		}
		if err != nil {
			t.Fatal(err)
		}
		return summary.Context
	}
	for _, retained := range []bool{false, true} {
		if retained {
			// Reconstruct from journals, never from the live controller's cached index.
			key := s.stateDir + "\x00" + s.ID()
			sessionActivityIndexes.Lock()
			if cached := sessionActivityIndexes.entries[key]; cached != nil {
				delete(sessionActivityIndexes.entries, key)
				sessionActivityIndexes.order.Remove(cached.element)
			}
			sessionActivityIndexes.Unlock()
		}
		leafContext := read(leaf.ChildSessionID, retained)
		if !leafContext.AncestryKnown || leafContext.Ref != encodeRef("", leaf.ChildSessionID) || leafContext.RootRef != ref || leafContext.ParentRef != encodeRef("", parent.ChildSessionID) || leafContext.SessionID != leaf.ChildSessionID || leafContext.DelegateID != "dlg_leaf" || len(leafContext.Ancestors) != 2 {
			t.Fatalf("retained=%v lineage = %+v", retained, leafContext)
		}
		if got := contextNameFields(t, leafContext)["delegateName"]; got != byID["dlg_leaf"].Name {
			t.Errorf("retained=%v leaf delegateName=%v, canonical row=%q", retained, got, byID["dlg_leaf"].Name)
		}
		for i, ancestor := range leafContext.Ancestors {
			if i == 0 {
				if ancestor.Ref != ref || ancestor.SessionID != s.ID() || ancestor.DelegateID != "" || contextNameFields(t, ancestor)["delegateName"] != nil {
					t.Fatalf("root ancestor was named or rerouted: %+v", ancestor)
				}
			} else {
				if ancestor.Ref != encodeRef("", parent.ChildSessionID) || ancestor.SessionID != parent.ChildSessionID || ancestor.DelegateID != "dlg_parent" || ancestor.Title != parent.Description || contextNameFields(t, ancestor)["delegateName"] != byID["dlg_parent"].Name {
					t.Errorf("retained=%v ancestor name/title/identity drift: %+v, canonical name=%q", retained, ancestor, byID["dlg_parent"].Name)
				}
			}
		}
		for _, d := range []struct {
			descriptor delegatestore.Descriptor
			id         string
		}{{sibling, "dlg_sibling"}, {unnamed, "dlg_unnamed"}} {
			ctx := read(d.descriptor.ChildSessionID, retained)
			fields := contextNameFields(t, ctx)
			if !ctx.AncestryKnown || ctx.ParentRef != ref || ctx.DelegateID != d.id || len(ctx.Ancestors) != 1 || ctx.Ancestors[0].Ref != ref {
				t.Fatalf("retained=%v child identity = %+v", retained, ctx)
			}
			if d.id == "dlg_unnamed" {
				if _, present := fields["delegateName"]; present {
					t.Errorf("unnamed descriptor invented name: %+v", fields)
				}
			} else if fields["delegateName"] != byID[d.id].Name {
				t.Errorf("same-name sibling borrowed name or lost it: %+v", fields)
			}
		}
	}
}

func TestSessionActivityContextDelegateNamesIncompleteThenRecovered(t *testing.T) {
	t.Parallel()
	stateDir, rootID := t.TempDir(), "nameprogressroot"
	savePastActivityMeta(t, stateDir, rootID, "Root thread title")
	dir := jobsDir(stateDir, rootID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := delegatestore.Open(filepath.Join(dir, "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	batch := make([]delegatestore.Event, 2001)
	for i := range batch {
		id := fmt.Sprintf("dlg_name_%04d", i)
		d := delegatestore.Descriptor{OwnerSessionID: rootID, VisibleSessionID: rootID, ChildSessionID: "child-" + id, TranscriptRef: "local:child-" + id, Name: "other name", Description: "Other description", ResolvedModel: "gpt-5.2", AgentType: "general", Task: "retained task", Resumable: true, ToolNameCeiling: []string{"communicate"}}
		if i == 2000 {
			d.Name = "Recovered canonical name"
		}
		batch[i] = delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, TS: time.Unix(int64(i), 0).UTC(), Created: &delegatestore.DelegateCreated{Descriptor: d}}
	}
	if _, _, err := store.AppendBatch(make(delegatestore.State), batch); err != nil {
		t.Fatal(err)
	}
	childID := batch[2000].Created.Descriptor.ChildSessionID
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Different thread title", rootID, 0)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", childID)}
	first, err := LoadSessionActivitySummary(t.Context(), stateDir, childID, params)
	if err != nil {
		t.Fatal(err)
	}
	if first.Context.AncestryKnown || first.Context.ParentRef != "" || first.Context.DelegateID != "" || len(first.Context.Ancestors) != 0 {
		t.Fatalf("incomplete proof published identity: %+v", first.Context)
	}
	if _, present := contextNameFields(t, first.Context)["delegateName"]; present {
		t.Fatal("incomplete proof published a name")
	}
	second, err := LoadSessionActivitySummary(t.Context(), stateDir, childID, params)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Context.AncestryKnown || second.Context.DelegateID != batch[2000].DelegateID || second.Context.ParentRef != encodeRef("", rootID) || second.Context.Epoch != first.Context.Epoch || contextNameFields(t, second.Context)["delegateName"] != "Recovered canonical name" {
		t.Fatalf("proven recovery did not publish canonical name: %+v", second.Context)
	}
}

func TestSessionActivityContextDelegateNamesRejectInvalidLineage(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 2}))
	d := stableToolDescriptor(s, "dlg_invalid", "dlg_missing_parent")
	d.Name = "Unproven name"
	state := delegatestore.State{"dlg_invalid": &delegatestore.Aggregate{DelegateID: "dlg_invalid", Descriptor: d}}
	context := appwire.SessionActivityContext{SessionID: d.ChildSessionID, RootRef: encodeRef("", s.ID())}
	if activityContextFromDelegates(&context, s.ID(), state) || context.DelegateID != "" || context.ParentRef != "" || len(context.Ancestors) != 0 {
		t.Fatalf("invalid chain published partial lineage: %+v", context)
	}
	if _, present := contextNameFields(t, context)["delegateName"]; present {
		t.Fatal("invalid chain published partial name")
	}
}
