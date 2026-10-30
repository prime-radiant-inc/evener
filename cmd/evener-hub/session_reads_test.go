package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
)

// A source records the arguments actually passed to existing source owners.
// Only external source answers are synthetic; sessionOperations is always real.
type managerReadSource struct {
	*scriptedAppSource
	tasks                []appwire.Task
	turns                []appwire.Turn
	pages                map[string]appwire.ThreadTurnsListResponse
	turnCalls            []appwire.ThreadTurnsListParams
	output               map[string]string
	outputCalls          []appwire.JobsOutputParams
	jobs                 map[string]appwire.JobActivityJob
	models               appwire.ModelListResponse
	modelCalls           []appwire.ModelListParams
	unavailable          bool
	partial              bool
	empty                bool
	activityCalls        []appwire.SessionActivityRequest
	activityNextCursor   string
	badOutputFloor       bool
	instanceReads        int
	changeInstanceOnRead int
}

func (s *managerReadSource) ThreadActivityRead(_ context.Context, p appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	s.activityCalls = append(s.activityCalls, appwire.SessionActivityRequest{Ref: p.Ref, Scope: p.Scope, Resource: appwire.SessionActivityResourceSummary})
	if s.partial {
		return appwire.SessionActivitySummary{Context: appwire.SessionActivityContext{Ref: p.Ref, Availability: "retained"}, Issues: []appwire.SessionActivityIssue{{Ref: p.Ref, Code: "source_unavailable"}}, RefreshPending: true, Jobs: appwire.SessionActivityCounts{Known: false}}, nil
	}
	return appwire.SessionActivitySummary{Context: appwire.SessionActivityContext{Ref: p.Ref, RootRef: "local:manager", ParentRef: "local:manager", AncestryKnown: true, Availability: "live"}, Jobs: appwire.SessionActivityCounts{Known: true, Total: 1}}, nil
}
func (s *managerReadSource) ThreadJobsList(_ context.Context, p appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	s.activityCalls = append(s.activityCalls, appwire.SessionActivityRequest{Ref: p.Ref, Scope: p.Scope, Cursor: p.Cursor, Limit: p.Limit, Resource: appwire.SessionActivityResourceJobs})
	page := appwire.SessionActivityPage{Complete: true}
	if p.Cursor == "" && s.activityNextCursor != "" {
		page.Complete, page.NextCursor = false, s.activityNextCursor
	}
	return appwire.SessionJobsResponse{Context: appwire.SessionActivityContext{Ref: p.Ref, RootRef: "local:manager", ParentRef: "local:manager", AncestryKnown: true, Availability: "live"}, Page: page, Jobs: []appwire.JobActivityJob{{JobID: "child-job", OwnerRef: p.Ref}}}, nil
}

func (s *managerReadSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.instanceReads++
	if s.changeInstanceOnRead > 0 && s.instanceReads == s.changeInstanceOnRead {
		s.thread.Evener.InstanceID = "replacement-instance"
	}
	read, err := s.scriptedAppSource.ReadThread(ctx, p)
	if p.IncludeTurns && s.pages != nil {
		read.Thread.Turns = s.pages[""].Data
		read.OlderCursor = s.pages[""].NextCursor
	}
	return read, err
}

func (s *managerReadSource) ItemCandidatesFromRead(_ context.Context, _ appwire.ThreadReadParams, response appwire.ThreadReadResponse) (appsource.ItemCandidateResult, error) {
	candidates, err := appitempaging.CandidatesFromTurns(response.Thread.Turns)
	if err != nil {
		return appsource.ItemCandidateResult{}, err
	}
	return appsource.ItemCandidateResult{Candidates: appitempaging.TranscriptItemWindow{Candidates: candidates, OlderCursor: response.OlderCursor}, Identity: appitempaging.CursorIdentity{ThreadRef: s.thread.Evener.Ref, Incarnation: "fixture", ProjectionVersion: 1}, Exhausted: response.OlderCursor == ""}, nil
}

func (s *managerReadSource) ListThreads(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.unavailable {
		return appwire.ThreadListResponse{}, appwire.SessionUnavailable("source offline")
	}
	if s.empty {
		return appwire.ThreadListResponse{Data: []appwire.Thread{}}, nil
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}
func (s *managerReadSource) ListTasks(_ context.Context, _ appwire.TaskListParams) (appwire.TaskListResponse, error) {
	if s.unavailable {
		return appwire.TaskListResponse{}, appwire.SessionUnavailable("source offline")
	}
	return appwire.TaskListResponse{Data: s.tasks}, nil
}
func (s *managerReadSource) ListTurns(_ context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	s.turnCalls = append(s.turnCalls, p)
	if s.unavailable {
		return appwire.ThreadTurnsListResponse{}, appwire.SessionUnavailable("source offline")
	}
	return s.pages[p.Cursor], nil
}
func (s *managerReadSource) JobOutput(_ context.Context, p appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	if p.BeforeBytes != nil {
		v := *p.BeforeBytes
		p.BeforeBytes = &v
	}
	s.outputCalls = append(s.outputCalls, p)
	text, ok := s.output[p.JobID]
	if !ok {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found")
	}
	end := int64(len(text))
	if p.BeforeBytes != nil {
		end = min(end, *p.BeforeBytes)
	}
	start := max(int64(0), end-p.MaxBytes)
	if p.MaxBytes == 0 {
		start = max(int64(0), end-4096)
	}
	floor := int64(0)
	if s.badOutputFloor {
		floor = start + 1
	}
	return appwire.JobsOutputResponse{Data: appwire.JobOutputPage{OffsetBytes: start, BytesReturned: end - start, TotalBytes: int64(len(text)), RetainedStartBytes: floor, Encoding: "utf8", Data: text[start:end]}}, nil
}
func (s *managerReadSource) JobGet(_ context.Context, p appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	job, ok := s.jobs[p.JobID]
	if !ok {
		return appwire.JobsGetResponse{}, appwire.InvalidParams("job not found")
	}
	return appwire.JobsGetResponse{Data: job}, nil
}
func (s *managerReadSource) ListModels(_ context.Context, p appwire.ModelListParams) (appwire.ModelListResponse, error) {
	s.modelCalls = append(s.modelCalls, p)
	return s.models, nil
}

func managerReadFixture(t *testing.T) (*sessionOperations, *managerReadSource, *managerReadSource, appwire.ManagerView) {
	t.Helper()
	const manager = "local:manager"
	first := &managerReadSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{ID: "shared", SessionID: "shared", Source: "local", Evener: appwire.EvenerThread{Ref: "local:shared", InstanceID: "instance-local"}}}}
	second := &managerReadSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: appwire.Thread{ID: "shared", SessionID: "shared", Source: "remote", Evener: appwire.EvenerThread{Ref: "remote:shared", InstanceID: "instance-remote"}}}}
	sources := appsource.NewRegistry()
	sources.Add(first)
	sources.Add(second)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "empty", SSH: "empty.example"}, {Name: "remote", SSH: "remote.example"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test", RemoteHostRegistry: hosts, RemoteHosts: hosts.All()}
	cfg.SessionSeen = hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "seen.db"))
	server := appserver.NewServer(appserver.ServerConfig{})
	owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
	return owner, first, second, appwire.ManagerView{Ref: manager, InstanceID: "manager-instance", Manager: true}
}
func managerInvoke(t *testing.T, o *sessionOperations, caller appwire.ManagerView, request appwire.SessionOperationRequest) appwire.SessionOperationResponse {
	t.Helper()
	got, err := o.Invoke(t.Context(), caller, request)
	if err != nil {
		t.Fatalf("operation %s: %v", operationKind(request), err)
	}
	return got
}
func operationKind(r appwire.SessionOperationRequest) string {
	raw, _ := json.Marshal(r)
	return string(raw)
}

func TestManagerTaskPageMatchesAccumulatedEncoding(t *testing.T) {
	owner, local, _, _ := managerReadFixture(t)
	base, err := json.Marshal(appwire.SessionTasksResponse{Data: []appwire.Task{{ID: 1}}, Snapshot: strings.Repeat("x", 64)})
	if err != nil {
		t.Fatal(err)
	}
	boundaryPrompt := managerTaskPageMaxJSONBytes - len(base)
	// This is the old page algorithm, kept independent of the new size
	// accounting. Compare the actual wire encoding, not Go struct equality.
	oldPage := func(tasks []appwire.Task, offset, limit int) (appwire.SessionTasksResponse, error) {
		identity := struct {
			Ref        string
			InstanceID string
			Data       []appwire.Task
		}{"local:shared", "instance-local", tasks}
		raw, err := json.Marshal(identity)
		if err != nil {
			return appwire.SessionTasksResponse{}, err
		}
		sum := sha256.Sum256(raw)
		result := appwire.SessionTasksResponse{Snapshot: hex.EncodeToString(sum[:]), Data: []appwire.Task{}}
		if offset >= len(tasks) {
			return result, nil
		}
		for i := offset; i < min(len(tasks), offset+limit); i++ {
			result.Data = append(result.Data, tasks[i])
			encoded, err := json.Marshal(result)
			if err != nil {
				return appwire.SessionTasksResponse{}, err
			}
			if len(encoded) > managerTaskPageMaxJSONBytes {
				result.Data = result.Data[:len(result.Data)-1]
				if len(result.Data) == 0 {
					return appwire.SessionTasksResponse{}, appwire.InvalidRequest("task exceeds bounded result; read the task on its owning session")
				}
				break
			}
		}
		if next := offset + len(result.Data); next < len(tasks) {
			result.NextOffset = &next
		}
		return result, nil
	}
	for _, tc := range []struct {
		name   string
		tasks  []appwire.Task
		offset int
	}{
		{"empty", []appwire.Task{}, 0},
		{"escaped and optional fields", []appwire.Task{{ID: 1, Description: "<&\né", Notes: []string{"\"quoted\""}}, {ID: 2, DependsOn: []int{1}}}, 0},
		{"exact cutoff", []appwire.Task{{ID: 1, Prompt: strings.Repeat("x", boundaryPrompt)}}, 0},
		{"one byte past cutoff", []appwire.Task{{ID: 1, Prompt: strings.Repeat("x", boundaryPrompt+1)}}, 0},
		{"second task over cutoff", []appwire.Task{{ID: 1, Prompt: strings.Repeat("x", managerTaskPageMaxJSONBytes-300)}, {ID: 2, Prompt: "next"}}, 0},
		{"offset page", []appwire.Task{{ID: 1, Prompt: "first"}, {ID: 2, Prompt: strings.Repeat("é<&", 20000)}, {ID: 3, Prompt: strings.Repeat("x", managerTaskPageMaxJSONBytes-300)}}, 1},
		{"single task over cutoff", []appwire.Task{{ID: 1, Prompt: strings.Repeat("x", managerTaskPageMaxJSONBytes)}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local.tasks = tc.tasks
			p := appwire.SessionTasksRequest{Ref: "local:shared", Offset: tc.offset, Limit: 200}
			want, wantErr := oldPage(tc.tasks, p.Offset, p.Limit)
			got, gotErr := owner.tasks(t.Context(), p)
			if (wantErr == nil) != (gotErr == nil) || wantErr != nil && wantErr.Error() != gotErr.Error() {
				t.Fatalf("error changed: old=%v new=%v", wantErr, gotErr)
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("page bytes changed: old=%s new=%s", wantJSON, gotJSON)
			}
		})
	}
}

func TestManagerReadContracts(t *testing.T) {
	owner, local, remote, caller := managerReadFixture(t)
	seenBefore, err := owner.cfg.SessionSeen.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	remote.empty = true
	discover := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}})
	if discover.Discover == nil {
		t.Fatal("discovery missing")
	}
	sources := map[string]bool{}
	for _, s := range discover.Discover.Sources {
		sources[s.ID] = true
	}
	emptyHostMissing := !sources["empty"] || !sources["remote"] || !sources["local"]
	seenAfter, err := owner.cfg.SessionSeen.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	discoveryMarkedSeen := !reflect.DeepEqual(seenBefore, seenAfter)
	raw, _ := json.Marshal(discover)
	if strings.Contains(string(raw), "empty.example") || strings.Contains(string(raw), "keyPath") || strings.Contains(string(raw), "hubToken") {
		t.Fatalf("discovery leaked host administration or credentials: %s", raw)
	}
	if emptyHostMissing || discoveryMarkedSeen {
		t.Fatal("read changed ownership")
	}
	remote.empty = false
	// Two sources may have the same thread ID; the ref, not the bare ID, is authority.
	for _, ref := range []string{"local:shared", "remote:shared"} {
		got := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: ref}})
		if got.Read == nil || got.Read.Thread.Evener.Ref != ref {
			t.Fatalf("cross-source read %s: %+v", ref, got.Read)
		}
	}
	// A descendant's activity context resolves to the descendant; its follow-up
	// must never grant write access to an ancestor through a root alias.
	local.thread.Evener.Ref = "local:child"
	local.thread.ID = "child"
	local.thread.SessionID = "child"
	child := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:child"}})
	descendantWriteRef := ""
	if len(child.FollowUps) > 0 {
		descendantWriteRef = child.FollowUps[0].Arguments.Hub.Ref
	}
	parentRef := "local:manager"
	if descendantWriteRef == parentRef || descendantWriteRef != "local:child" {
		t.Fatalf("read changed ownership: follow-up=%+v", child.FollowUps)
	}
	summary := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "local:child", Resource: appwire.SessionActivityResourceSummary}}).Activity
	jobs := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "local:child", Resource: appwire.SessionActivityResourceJobs, Scope: appwire.SessionActivityScopeSession, Limit: 1}}).Activity
	if summary == nil || summary.Summary == nil || !summary.Summary.Jobs.Known || jobs == nil || jobs.Jobs == nil || len(jobs.Jobs.Jobs) != 1 || jobs.Jobs.Jobs[0].OwnerRef != "local:child" || len(local.activityCalls) != 2 || local.activityCalls[1].Limit != 1 {
		t.Fatalf("descendant activity did not use existing activity owners: summary=%+v jobs=%+v calls=%+v", summary, jobs, local.activityCalls)
	}
	local.partial = true
	partial := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "local:child", Resource: appwire.SessionActivityResourceSummary}}).Activity
	if partial == nil || partial.Summary == nil || partial.Summary.Jobs.Known || !partial.Summary.RefreshPending || len(partial.Summary.Issues) != 1 || partial.Summary.Issues[0].Ref != "local:child" {
		t.Fatalf("partial activity was turned into an authoritative count: %+v", partial)
	}
	local.partial = false
	remote.unavailable = true
	offline := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}})
	seenOffline := false
	for _, s := range offline.Discover.Sources {
		if s.ID == "remote" {
			seenOffline = true
		}
	}
	if !seenOffline {
		t.Fatalf("offline host disappeared from discovery: %+v", offline.Discover)
	}
	if _, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "remote:shared"}}); err == nil {
		t.Fatal("offline tasks became an authoritative empty page")
	}
	// Refuse local-only exact JSONL, arbitrary expansion and ranges at the hub boundary.
	exactJSONLAccepted := false
	unsupportedExpansionAccepted := false
	for _, request := range []string{
		`{"turns":{"ref":"local:child","format":"jsonl"}}`,
		`{"turns":{"ref":"local:child","expandTurn":7}}`,
		`{"jobOutput":{"ref":"local:child","jobId":"job-1","range":"0-200"}}`,
		`{"turns":{"ref":"local:child","cursor":"opaque","includeConversation":false}}`,
		`{"turns":{"ref":"local:child","cursor":"opaque","itemsView":"full"}}`,
	} {
		var decoded appwire.SessionOperationRequest
		if json.Unmarshal([]byte(request), &decoded) == nil {
			if strings.Contains(request, "jsonl") {
				exactJSONLAccepted = true
			} else {
				unsupportedExpansionAccepted = true
			}
		}
	}
	if exactJSONLAccepted || unsupportedExpansionAccepted {
		t.Fatal("wrong source read contract")
	}
}

func TestManagerReadActivityFollowUpPreservesScope(t *testing.T) {
	owner, local, _, caller := managerReadFixture(t)
	local.activityNextCursor = "older-subtree-jobs"
	first := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{
		Ref: "local:shared", Resource: appwire.SessionActivityResourceJobs, Scope: appwire.SessionActivityScopeSubtree, Limit: 1,
	}})
	if first.Activity == nil || first.Activity.Jobs == nil || first.Activity.Jobs.Page.NextCursor != local.activityNextCursor || len(first.FollowUps) != 1 {
		t.Fatalf("activity first page lacks continuation: %+v", first)
	}
	encoded, err := json.Marshal(first.FollowUps[0].Arguments.Hub)
	if err != nil || !strings.Contains(string(encoded), `"scope":"subtree"`) || !strings.Contains(string(encoded), `"limit":1`) {
		t.Fatalf("activity continuation lost scope or page size: %s, err=%v", encoded, err)
	}
	var next struct {
		Mode string `json:"mode"`
		appwire.SessionActivityRequest
	}
	if err := json.Unmarshal(encoded, &next); err != nil || next.Mode != "activity" {
		t.Fatalf("activity continuation is not a second-page request: %+v err=%v", next, err)
	}
	managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Activity: &next.SessionActivityRequest})
	if len(local.activityCalls) != 2 || local.activityCalls[1].Ref != "local:shared" || local.activityCalls[1].Resource != appwire.SessionActivityResourceJobs || local.activityCalls[1].Scope != appwire.SessionActivityScopeSubtree || local.activityCalls[1].Limit != 1 || local.activityCalls[1].Cursor != "older-subtree-jobs" {
		t.Fatalf("second activity page changed owner, scope or limit: %+v", local.activityCalls)
	}
}

func TestManagerReadTaskSnapshot(t *testing.T) {
	owner, local, remote, caller := managerReadFixture(t)
	long := strings.Repeat("complete task instructions and notes ", 300)
	local.tasks = make([]appwire.Task, 6)
	for i := range local.tasks {
		local.tasks[i] = appwire.Task{ID: i + 1, Type: appwire.TaskTypeImplement, Description: fmt.Sprintf("task-%d", i), Prompt: long + fmt.Sprint(i), Status: appwire.TaskOpen, Notes: []string{long}}
	}
	firstResult := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Limit: 2}})
	first := firstResult.Tasks
	if first == nil || len(first.Data) != 2 || first.NextOffset == nil || first.Snapshot == "" || first.Data[0].Prompt != long+"0" {
		t.Fatalf("first task page incomplete: %+v", first)
	}
	if len(firstResult.FollowUps) != 1 || firstResult.FollowUps[0].Tool != "job_list" || firstResult.FollowUps[0].Arguments.Hub.Ref != "local:shared" || firstResult.FollowUps[0].Arguments.Hub.Snapshot != first.Snapshot || firstResult.FollowUps[0].Arguments.Hub.Offset != 2 {
		t.Fatalf("task continuation lacks owner or snapshot: %+v", firstResult.FollowUps)
	}
	continuation, err := json.Marshal(firstResult.FollowUps[0].Arguments.Hub)
	if err != nil || !strings.Contains(string(continuation), `"limit":2`) {
		t.Fatalf("task continuation lost requested page limit: %s err=%v", continuation, err)
	}
	tokenInput := struct {
		Ref        string
		InstanceID string
		Data       []appwire.Task
	}{"local:shared", "instance-local", local.tasks}
	serialized, err := json.Marshal(tokenInput)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(serialized)
	expected := hex.EncodeToString(sum[:])
	if first.Snapshot != expected {
		t.Fatalf("snapshot %q != complete-list SHA-256 %q", first.Snapshot, expected)
	}
	second := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Offset: *first.NextOffset, Limit: 2, Snapshot: first.Snapshot}}).Tasks
	laterPageItemPresent := second != nil && len(second.Data) == 2 && second.Data[0].ID == 3 && second.Data[0].Prompt == long+"2"
	if !laterPageItemPresent || second.NextOffset == nil {
		t.Fatalf("second task page incomplete: %+v", second)
	}
	third := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Offset: *second.NextOffset, Limit: 2, Snapshot: second.Snapshot}}).Tasks
	if third == nil || len(third.Data) != 2 || third.Data[1].Prompt != long+"5" || third.NextOffset != nil {
		t.Fatalf("final complete task page missing: %+v", third)
	}
	// Reconnect is modeled by reconstructing the stateless owner over unchanged sources.
	owner = &sessionOperations{cfg: owner.cfg, sources: owner.sources, server: owner.server, relays: owner.relays}
	stable := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Limit: 2}}).Tasks
	unchangedReconnectToken := ""
	if stable != nil {
		unchangedReconnectToken = stable.Snapshot
	}
	local.tasks[5].Status = appwire.TaskDone // Outside the requested next page.
	changed, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Offset: 2, Limit: 2, Snapshot: first.Snapshot}})
	changedPageReturned := err == nil && changed.Tasks != nil && len(changed.Tasks.Data) > 0
	snapshotChanged := false
	var wire appwire.WireError
	if errors.As(err, &wire) {
		encoded, _ := json.Marshal(wire.Data)
		var detail struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(encoded, &detail)
		snapshotChanged = detail.Reason == "snapshot_changed"
	}
	if changedPageReturned || !snapshotChanged {
		t.Fatalf("task snapshot mixed: response=%+v err=%v", changed, err)
	}
	remote.tasks = append([]appwire.Task{}, local.tasks...)
	remoteToken := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "remote:shared", Limit: 2}}).Tasks
	if remoteToken == nil || remoteToken.Snapshot == "" || remoteToken.Snapshot == first.Snapshot {
		t.Fatalf("source ownership not included in task hash: %+v", remoteToken)
	}
	cross, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "remote:shared", Offset: 2, Snapshot: first.Snapshot}})
	crossOwnerTokenAccepted := err == nil && cross.Tasks != nil && len(cross.Tasks.Data) > 0
	local.tasks = nil
	unavailable := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}}).Tasks
	unavailableHasToken := unavailable == nil || unavailable.Snapshot != "" || unavailable.NextOffset != nil || unavailable.Data != nil
	local.tasks = []appwire.Task{}
	empty := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}}).Tasks
	if empty == nil || empty.Data == nil || len(empty.Data) != 0 || empty.Snapshot == "" {
		t.Fatalf("authoritative empty task list lost: %+v", empty)
	}
	if unavailableHasToken || crossOwnerTokenAccepted {
		t.Fatalf("task snapshot mixed: unavailable=%+v cross=%+v err=%v", unavailable, cross, err)
	}
	if unchangedReconnectToken != first.Snapshot || !laterPageItemPresent {
		t.Fatalf("unchanged continuation lost: token=%s initial=%s later=%+v", unchangedReconnectToken, first.Snapshot, second)
	}
}

func TestManagerReadTaskSnapshotInstanceChanges(t *testing.T) {
	owner, local, _, caller := managerReadFixture(t)
	local.tasks = []appwire.Task{{ID: 1, Type: appwire.TaskTypeImplement, Status: appwire.TaskOpen, Prompt: "full instruction"}}
	local.changeInstanceOnRead = 2
	first, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}})
	if err == nil || first.Tasks != nil && first.Tasks.Snapshot != "" {
		t.Fatalf("instance changed during read but minted a task token: result=%+v err=%v", first, err)
	}
	local.changeInstanceOnRead = 0
	restart := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}}).Tasks
	if restart == nil || restart.Snapshot == "" || len(restart.Data) != 1 {
		t.Fatalf("fresh first page after replacement unavailable: %+v", restart)
	}
	local.thread.Evener.InstanceID = ""
	unknown, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}})
	if err == nil || unknown.Tasks != nil && unknown.Tasks.Snapshot != "" {
		t.Fatalf("unknown instance minted a task token: result=%+v err=%v", unknown, err)
	}
}

func TestManagerReadTaskPageRespectsSerializedCap(t *testing.T) {
	owner, local, _, caller := managerReadFixture(t)
	long := strings.Repeat("unabridged prompt text ", 300)
	local.tasks = make([]appwire.Task, 60)
	for i := range local.tasks {
		local.tasks[i] = appwire.Task{ID: i + 1, Type: appwire.TaskTypeImplement, Status: appwire.TaskOpen, Prompt: long + fmt.Sprint(i)}
	}
	first := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared"}})
	if first.Tasks == nil || first.Tasks.NextOffset == nil || *first.Tasks.NextOffset >= 50 || *first.Tasks.NextOffset < 1 || len(first.FollowUps) != 1 {
		t.Fatalf("large task page did not report a bounded continuation: %+v", first.Tasks)
	}
	for i, task := range first.Tasks.Data {
		if task.Prompt != long+fmt.Sprint(i) {
			t.Fatalf("task %d was clipped to fit the cap", i)
		}
	}
	encoded, err := json.Marshal(first)
	if err != nil || len(encoded) > managerReadMaxJSONBytes {
		t.Fatalf("first task page exceeds serialized cap: bytes=%d err=%v", len(encoded), err)
	}
	next := *first.Tasks.NextOffset
	second := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:shared", Offset: next, Snapshot: first.Tasks.Snapshot}})
	if second.Tasks == nil || len(second.Tasks.Data) == 0 || second.Tasks.Data[0].ID != next+1 || second.Tasks.Data[0].Prompt != long+fmt.Sprint(next) {
		t.Fatalf("continuation skipped or clipped task %d: %+v", next, second.Tasks)
	}
}

func TestManagerReadRefreshAcrossPages(t *testing.T) {
	owner, local, remote, caller := managerReadFixture(t)
	ref := "local:shared"
	longQuestion := strings.Repeat("full question text ", 300)
	longApproval := strings.Repeat("complete approval decision ", 250)
	local.pages = map[string]appwire.ThreadTurnsListResponse{
		"":        {Data: []appwire.Turn{{ID: "turn-3", Items: []appwire.ThreadItem{{ID: "item-3", TranscriptKey: "key-3", Type: "assistantMessage", Text: "newest", Position: &appwire.ThreadItemPosition{Entry: 3}}}}}, NextCursor: "older-1", Authoritative: true},
		"older-1": {Data: []appwire.Turn{{ID: "turn-2", Items: []appwire.ThreadItem{{ID: "item-2", TranscriptKey: "key-2", Type: "systemMessage", EventKind: appwire.ThreadItemEventKindApprovalDecision, Text: longApproval, Position: &appwire.ThreadItemPosition{Entry: 2}}}}}, NextCursor: "older-2", Authoritative: true},
		"older-2": {Data: []appwire.Turn{{ID: "turn-1", Items: []appwire.ThreadItem{{ID: "item-1", TranscriptKey: "key-1", Type: "userMessage", Text: longQuestion, Position: &appwire.ThreadItemPosition{Entry: 1}}}}}, Authoritative: true},
	}
	identity := appitempaging.CursorIdentity{ThreadRef: ref, Incarnation: "fixture", ProjectionVersion: 1}
	firstCursor, err := appitempaging.EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 3})
	if err != nil {
		t.Fatal(err)
	}
	secondCursor, err := appitempaging.EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 2})
	if err != nil {
		t.Fatal(err)
	}
	local.pages[""], local.pages[firstCursor], local.pages[secondCursor] = local.pages[""], local.pages["older-1"], local.pages["older-2"]
	initial := local.pages[""]
	initial.NextCursor = firstCursor
	local.pages[""] = initial
	secondPage := local.pages[firstCursor]
	secondPage.NextCursor = secondCursor
	local.pages[firstCursor] = secondPage
	visible := map[string]bool{}
	cursor := ""
	for i := 0; i < 3; i++ {
		read := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: ref, Cursor: cursor, ItemLimit: 1}})
		page := read.Turns
		if page == nil || len(page.Data) != 1 {
			t.Fatalf("history page %d: %+v", i, page)
		}
		if visible[page.Data[0].ID] {
			t.Fatalf("duplicate visible item: %s", page.Data[0].ID)
		}
		visible[page.Data[0].ID] = true
		if page.NextCursor != "" && (len(read.FollowUps) != 1 || read.FollowUps[0].Tool != "read_transcript" || read.FollowUps[0].Arguments.Hub.Cursor != page.NextCursor || read.FollowUps[0].Arguments.Hub.Ref != ref) {
			t.Fatalf("history continuation lacks owner: %+v", read.FollowUps)
		}
		if i == 1 && page.Data[0].Items[0].Text != longApproval || i == 2 && page.Data[0].Items[0].Text != longQuestion {
			t.Fatal("history returned a preview instead of complete question or approval content")
		}
		cursor = page.NextCursor
	}
	local.output = map[string]string{"job-1": strings.Repeat("segment", 200)}
	local.jobs = map[string]appwire.JobActivityJob{"job-1": {JobID: "job-1", OwnerRef: ref, Command: longQuestion}}
	current := int64(len(local.output["job-1"]))
	var pieces []string
	for i := 0; i < 3; i++ {
		read := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{JobOutput: &appwire.JobsOutputParams{Ref: ref, JobID: "job-1", MaxBytes: 32, BeforeBytes: &current}})
		out := read.JobOutput
		if out == nil {
			t.Fatal("missing output page")
		}
		page := out.Data
		if page.OffsetBytes < page.RetainedStartBytes {
			t.Fatal("wrong source read contract")
		}
		if page.OffsetBytes+page.BytesReturned != current || page.Data == "" {
			t.Fatalf("output page %d: %+v", i, page)
		}
		if len(read.FollowUps) != 1 || read.FollowUps[0].Arguments.Hub.Ref != ref || read.FollowUps[0].Arguments.Hub.JobID != "job-1" || read.FollowUps[0].Arguments.Hub.BeforeBytes == nil || *read.FollowUps[0].Arguments.Hub.BeforeBytes != page.OffsetBytes {
			t.Fatalf("job follow-up omitted owner/job/byte boundary: %+v", read.FollowUps)
		}
		current = page.OffsetBytes
		pieces = append(pieces, page.Data)
	}
	if !reflect.DeepEqual(pieces, []string{local.output["job-1"][len(local.output["job-1"])-32:], local.output["job-1"][len(local.output["job-1"])-64 : len(local.output["job-1"])-32], local.output["job-1"][len(local.output["job-1"])-96 : len(local.output["job-1"])-64]}) {
		t.Fatalf("output pages changed bytes: %v", pieces)
	}
	if len(local.outputCalls) != 3 || local.outputCalls[2].BeforeBytes == nil || *local.outputCalls[2].BeforeBytes != int64(len(local.output["job-1"])-64) {
		t.Fatalf("job continuation lost owner or byte boundary: %+v", local.outputCalls)
	}
	local.badOutputFloor = true
	if _, err := owner.Invoke(t.Context(), caller, appwire.SessionOperationRequest{JobOutput: &appwire.JobsOutputParams{Ref: ref, JobID: "job-1", MaxBytes: 32}}); err == nil {
		t.Fatal("source output offset below retained floor was passed through")
	}
	local.badOutputFloor = false
	// On refresh and reconnect, a later-page item remains visible or the source
	// explicitly invalidates its cursor. Never silently splice a different page.
	refreshed := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: ref, Cursor: firstCursor, ItemLimit: 1}}).Turns
	mixedGenerations := refreshed != nil && len(refreshed.Data) > 0 && refreshed.Data[0].ID != "turn-2"
	duplicatedVisibleItem := len(visible) != 3
	silentlyLostLaterPageItem := !visible["turn-1"] || (refreshed == nil && len(local.turnCalls) < 4)
	if mixedGenerations || duplicatedVisibleItem || silentlyLostLaterPageItem {
		t.Fatal("paging recovery corrupted evidence")
	}
	job := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Job: &appwire.JobsGetParams{Ref: ref, JobID: "job-1"}}).Job
	if job == nil || job.Data.Command != longQuestion {
		t.Fatal("full command was replaced by preview")
	}
	remote.models = appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "remote-provider", Model: "remote-model"}}, Diagnostics: []appwire.ModelListDiagnostic{{Source: "remote", Message: "selected host needs sign-in"}}}
	model := managerInvoke(t, owner, caller, appwire.SessionOperationRequest{Models: &appwire.ModelListParams{Harness: "remote", Refresh: true}}).Models
	frozenLocalCatalogChanged := model == nil || len(model.Data) != 1 || model.Data[0].Provider != "remote-provider"
	hostCatalogSource := ""
	if len(remote.modelCalls) > 0 {
		hostCatalogSource = "remote"
	}
	if model == nil || len(model.Diagnostics) != 1 {
		hostCatalogSource = ""
	}
	selectedHost := "remote"
	if frozenLocalCatalogChanged || hostCatalogSource != selectedHost {
		t.Fatalf("catalog scope changed: models=%+v calls=%+v", model, remote.modelCalls)
	}
}
