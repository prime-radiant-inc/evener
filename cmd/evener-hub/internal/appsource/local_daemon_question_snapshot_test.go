package appsource

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestLocalDaemonQuestionSnapshotAndRemoteAskFence(t *testing.T) {
	t.Parallel()
	var item LocalDaemonEntry
	if err := json.Unmarshal([]byte(`{"Entry":{"thread_id":"thread-token","session_id":"instance-token"},"PendingAsk":true,"PendingQuestion":{"question":"q-token","count":3,"turnId":"turn-token","callId":"call-token","complete":false,"truncated":true},"PendingAskState":{"version":1,"instanceId":"instance-token","bootGeneration":"boot-token","revision":4,"calls":[{"turnId":"turn-token","callId":"call-token"}],"compositeTotal":2,"calllessCount":1}}`), &item); err != nil {
		t.Fatal(err)
	}
	source := NewLocalDaemonSourceWithEntries("local", nil, nil)
	row := source.threadFromEntry(item)
	p := row.Evener.PendingAsk
	if p == nil || p.BootGeneration != "boot-token" || p.Revision != 4 || p.CompositeTotal != 2 || p.CalllessCount != 1 || len(p.Calls) != 1 || p.Calls[0].TurnID != "turn-token" {
		t.Errorf("discovery row lost ask snapshot: %+v", row.Evener)
	}
	if row.Evener.PendingQuestion.TurnID != "turn-token" || row.Evener.PendingQuestion.Complete == nil {
		t.Fatalf("question metadata=%+v", row.Evener.PendingQuestion)
	}
	remote := maskRemoteThreadCapabilities(appwire.ThreadCapabilities{AskFence: true}, forwardedRemoteThreadCapabilities(appwire.FeatureSet{}))
	if !remote.AskFence {
		t.Fatal("remote capability rebuild masked ask fence")
	}
}

func TestLocalDaemonWorkspaceRefSharesSessionFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		entry        rendezvous.Entry
		thread, want string
	}{
		{rendezvous.Entry{WorkspaceRef: "local:workspace-token", SessionID: "instance-token", ThreadID: "thread-token"}, "thread-token", "local:workspace-token"},
		{rendezvous.Entry{SessionID: "instance-token", ThreadID: "thread-token"}, "thread-token", "local:instance-token"},
		{rendezvous.Entry{ThreadID: "thread-token"}, "thread-token", "local:thread-token"},
	} {
		if got := localDaemonWorkspaceRef("local", tc.entry, tc.thread); got != tc.want {
			t.Fatalf("workspace ref=%q want=%q", got, tc.want)
		}
	}
}

func TestRemoteHubOlderDaemonKeepsAskFenceAbsent(t *testing.T) {
	t.Parallel()
	// This is an older daemon's JSON, not an all-capabilities fixture with a
	// bit switched off. The absent bit must stay absent after remote rebounding.
	var old appwire.Thread
	if err := json.Unmarshal([]byte(`{"id":"legacy-thread","source":"local","evener":{"ref":"local:legacy-thread","instanceId":"legacy-instance","askPending":true,"pendingQuestion":{"question":"legacy-question","count":1},"capabilities":{"send":true}}}`), &old); err != nil {
		t.Fatal(err)
	}
	source := NewRemoteHubSource("host", nil, nil)
	row, err := source.fromRemoteThread(old, forwardedThreadCapabilitiesForTests())
	if err != nil {
		t.Fatal(err)
	}
	if row.Evener.Capabilities != (appwire.ThreadCapabilities{Send: true}) {
		t.Fatalf("older daemon capabilities=%+v", row.Evener.Capabilities)
	}
	encoded, err := json.Marshal(row.Evener.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["askFence"]; present {
		t.Fatal("older daemon acquired an advertised ask fence")
	}
	if !row.Evener.AskPending || row.Evener.PendingQuestion == nil || row.Evener.PendingQuestion.Question != "legacy-question" || row.Evener.PendingQuestion.Complete != nil || row.Evener.PendingQuestion.Truncated != nil {
		t.Fatalf("older daemon lost its question or gained unknown metadata: %+v", row.Evener)
	}
}
