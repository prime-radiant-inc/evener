package hub

import (
	"context"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/strutil"
)

// fetchStatus reads the daemon's bounded typed thread snapshot, returning nil
// on any error so workspace hydration retains its roster-backed fallback.
func (s *WebServer) fetchStatus(le hubcore.LiveEntry) *daemonStatus {
	if s == nil || s.sources == nil {
		return nil
	}
	source, ok := s.sources.Source("local")
	if !ok {
		return nil
	}
	threadID := strutil.FirstNonEmpty(le.SessionID, le.Entry.SessionID, le.ThreadID)
	if threadID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: localAppRef(threadID)})
	if err != nil {
		return nil
	}
	thread := resp.Thread
	return &daemonStatus{
		SessionID:           strutil.FirstNonEmpty(thread.SessionID, thread.ID),
		Model:               thread.ModelProvider,
		Profile:             thread.Evener.Profile,
		State:               thread.Status.Type,
		Turns:               thread.Evener.TurnCount,
		WorkingDir:          thread.CWD,
		ContextPressure:     thread.Evener.ContextPressure,
		ContextUsed:         thread.Evener.ContextUsed,
		ContextWindow:       thread.Evener.ContextWindow,
		ContextRemaining:    thread.Evener.ContextRemaining,
		WorkMillis:          thread.Evener.WorkMillis,
		Usage:               thread.Evener.Usage,
		ActiveTurnStartedAt: thread.Evener.ActiveTurnStartedAt,
	}
}
