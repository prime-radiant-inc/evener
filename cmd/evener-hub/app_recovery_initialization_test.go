package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/rendezvous"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestWebRejectsRequestsWhenRecoveryAuthorityCannotLoad(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "recovery"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "recovery", "state.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root})
	response := httptest.NewRecorder()
	web.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want unavailable before request admission", response.Code)
	}
}

func TestForceStopRejectsUncommittedRecoveryIntent(t *testing.T) {
	root := t.TempDir()
	locks, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "recovery"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4242, SessionID: "saved", ThreadID: "saved", StateDir: t.TempDir(), StartedAt: time.Now()}
	writeRendezvous(t, runDir, entry)
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &events}, nil
	})}
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:saved"}, nil); err == nil {
		t.Fatal("stop succeeded without committed recovery intent")
	}
	if !reflect.DeepEqual(events, []string{"close"}) {
		t.Fatalf("uncommitted stop process events=%v", events)
	}
	if state := locks.RecoveryState("saved"); state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("uncommitted recovery=%+v", state)
	}
}
