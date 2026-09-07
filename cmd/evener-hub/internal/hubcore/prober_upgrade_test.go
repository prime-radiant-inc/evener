package hubcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// The network peer models an older daemon's initialize rejection. No old
// protocol is negotiated and no session mutation is sent to that daemon.
func TestRosterReportsRestartRequiredAfterProtocolUpgrade(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := wsjson.Read(r.Context(), conn, &request); err != nil {
			return
		}
		if request.Method != appwire.MethodInitialize {
			t.Errorf("method = %q, want initialize", request.Method)
		}
		if err := wsjson.Write(context.Background(), conn, map[string]any{
			"id":    request.ID,
			"error": map[string]any{"code": appwire.CodeInvalidRequest, "message": "incompatible protocol"},
		}); err != nil {
			t.Errorf("write rejection: %v", err)
		}
	}))
	defer peer.Close()
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1001, SessionID: "session-upgrade", ThreadID: "session-upgrade",
		Protocol: "evener-appwire-v3", Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http") + "/rpc",
	})
	roster := NewRoster(dir, &StatusProber{client: peer.Client()})
	roster.procAlive = func(int) bool { return true }
	roster.Refresh()
	entry, ok := roster.Find("session-upgrade")
	if !ok {
		t.Fatal("incompatible live daemon disappeared after hub upgrade")
	}
	if entry.Status != "restartRequired" {
		t.Fatalf("status = %q, want restartRequired", entry.Status)
	}
	if len(entry.RunningSubagentIDs) != 0 || len(entry.RunningJobs) != 0 {
		t.Fatal("unreadable activity must not be reported as current")
	}

	if NormalizeState(entry.Status) != "restartRequired" || attentionLevel(NormalizeState(entry.Status)) != "needs_you" {
		t.Fatal("protocol mismatch must remain visible in navigation attention")
	}
	_, replacement := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "session-upgrade", state: appwire.ThreadStatusActive,
		descendants: map[string]string{"child-working": appwire.ThreadStatusActive},
		source:      wireProbeEnvelopeSource{detailed: server.DetailedStatus{Jobs: []server.JobStatusInfo{{JobID: "shell-running", JobType: "shell", Status: "running"}}}},
	})
	replacement.PID = 1001
	replacement.Protocol = appwire.ProtocolVersion
	replacement.SessionID = "session-upgrade"
	replacement.ThreadID = "session-upgrade"
	writeRendezvous(t, dir, replacement)
	roster.Refresh()
	recovered, ok := roster.Find("session-upgrade")
	if !ok || recovered.Status != appwire.ThreadStatusActive || len(recovered.RunningSubagentIDs) != 1 || len(recovered.RunningJobs) != 1 {
		t.Fatalf("replacement activity did not recover: %+v", recovered)
	}
}
