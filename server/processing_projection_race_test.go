package server

import (
	"testing"

	"primeradiant.com/evener/internal/appserver"
)

func TestSetProcessingTurnSerializesWithProjectionCommit(t *testing.T) {
	srv := NewServer(ServerConfig{})
	entered := make(chan struct{})
	release := make(chan struct{})
	commitDone := make(chan struct{})
	go func() {
		srv.AppServer().CommitProjection(func() []appserver.SequencedNotification {
			close(entered)
			<-release
			return nil
		})
		close(commitDone)
	}()
	<-entered
	setterStarted := make(chan struct{})
	setDone := make(chan struct{})
	go func() {
		close(setterStarted)
		srv.SetProcessingTurn("new-turn")
		close(setDone)
	}()
	<-setterStarted
	select {
	case <-setDone:
		t.Fatal("SetProcessingTurn interleaved with an active projection commit")
	default:
	}
	close(release)
	<-commitDone
	<-setDone

	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if srv.appPendingStableTurnID != "new-turn" || srv.appActiveTurnID != "new-turn" {
		t.Fatalf("processing identity = pending:%q active:%q, want new-turn", srv.appPendingStableTurnID, srv.appActiveTurnID)
	}
}
