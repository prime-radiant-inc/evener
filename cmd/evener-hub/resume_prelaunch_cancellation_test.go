package hub

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// An already-ended caller must retain both its context cause and the hub's
// launch classification, even though no child reaches the rendezvous wait.
func TestResumeDaemonPrelaunchCancellationClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		deadline  bool
		cause     error
		waitError error
		class     string
	}{
		{"canceled", false, context.Canceled, errRendezvousCanceled, "canceled"},
		{"deadline", true, context.DeadlineExceeded, errRendezvousTimeout, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if tc.deadline {
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
				defer cancel()
			}
			var log bytes.Buffer
			dir := t.TempDir()
			_, err := resumeDaemon(ctx, filepath.Join(dir, "absent-executable"), filepath.Join(dir, "run"), hubcore.ResumeRequest{SessionID: hubtest.SessionID(t)}, 0, &log)
			if !errors.Is(err, tc.cause) {
				t.Fatalf("context cause lost: got %v, want %v", err, tc.cause)
			}
			if !errors.Is(err, tc.waitError) {
				t.Errorf("launch classification lost: got %v, want %v", err, tc.waitError)
			}
			if got := diagnostic.Classify(err.Error()).Source; got != diagnostic.SourceHub {
				t.Errorf("diagnostic source = %q, want %q", got, diagnostic.SourceHub)
			}
			records := threadLifecycleLogRecords(t, log.String())
			if len(records) != 4 {
				t.Fatalf("want daemon and launch begin/complete records, got %#v", records)
			}
			for _, record := range records {
				if record["state"] == "complete" && record["error_class"] != tc.class {
					t.Errorf("completion classification = %#v, want %s", record, tc.class)
				}
			}
		})
	}
}
