package hub

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
)

// Completion closures created before ownership resolution must use the final
// alias identity without changing the snapshot retained by the caller's context.
func TestThreadLifecycleLoggingResumeAliasTerminalCorrelation(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "daemon_read_error"}[readFails], func(t *testing.T) {
			const secret = "PRIVATE_ALIAS_READ_ERROR_opaque_sentinel"
			var currentID string
			cfg, id, calls := parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					if readFails {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(secret)
					}
					return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: currentID, SessionID: currentID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: currentID}}}, nil
				})
			})
			currentID = id
			requestedID := hubtest.SessionID(t)
			if requestedID == currentID {
				t.Fatal("alias fixture requires distinct requested and current session IDs")
			}
			locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			aliases := []string{requestedID, currentID}
			finish := locks.BeginForceStop(aliases)
			if err := locks.PersistForceStop(aliases, currentID); err != nil {
				t.Fatal(err)
			}
			if err := locks.ConfirmForceStop(currentID); err != nil {
				t.Fatal(err)
			}
			finish.Finish(true)
			cfg.ResumeLocks = locks
			var output bytes.Buffer
			ctx, trace := withThreadLifecycleLog(t.Context(), "resume", requestedID, &output)
			// Seed a request start in the past before sharing the immutable trace:
			// resetting it at alias resolution must not reset elapsed_ms. No sleep.
			trace.started = time.Now().Add(-time.Hour)
			original := *trace
			elapsedBefore := time.Since(original.started).Milliseconds()
			response, err := hubThreadResume(ctx, cfg, newHubSourceRegistry(cfg), appwire.ThreadResumeParams{Ref: "local:" + requestedID})
			elapsedAfter := time.Since(original.started).Milliseconds()
			if readFails {
				if err == nil {
					t.Fatal("daemon read failure unexpectedly succeeded")
				}
			} else if err != nil || response.Thread.SessionID != currentID {
				t.Fatalf("alias resume fixture: session=%s err=%v", response.Thread.SessionID, err)
			}
			if *calls != 1 {
				t.Fatalf("resume launches=%d, want 1", *calls)
			}
			if threadLifecycleFromContext(ctx) != trace || *trace != original {
				t.Error("alias resolution mutated the caller's lifecycle snapshot")
			}
			if strings.Contains(output.String(), secret) {
				t.Fatalf("raw daemon error leaked into lifecycle log: %s", &output)
			}
			records := assertThreadLifecycleRecords(t, output.String())
			result, class := "success", "none"
			if readFails {
				result, class = "error", "failed"
			}
			assertThreadLifecycleOutcome(t, records, "daemon_read", result, class)
			assertThreadLifecycleOutcome(t, records, "request", result, class)
			// Lock completion describes releasing the lock, not the RPC outcome.
			assertThreadLifecycleOutcome(t, records, "lock_held", "success", "none")
			for _, record := range records {
				if record["request_id"] != original.requestID || record["session_id"] != requestedID || record["operation"] != "resume" {
					t.Errorf("lost original request identity: %#v", record)
				}
				elapsed, parseErr := strconv.ParseInt(record["elapsed_ms"], 10, 64)
				if parseErr != nil || elapsed < elapsedBefore || elapsed > elapsedAfter {
					t.Errorf("request start changed: %#v, want elapsed_ms in [%d, %d]", record, elapsedBefore, elapsedAfter)
				}
				if record["stage"] != "request" && record["stage"] != "lock_held" {
					continue
				}
				wantResolved := "-"
				if record["state"] == "complete" {
					wantResolved = currentID
				}
				if record["resolved_session_id"] != wantResolved {
					t.Errorf("%s/%s resolved_session_id=%s, want %s (request_id=%s session_id=%s)", record["stage"], record["state"], record["resolved_session_id"], wantResolved, record["request_id"], record["session_id"])
				}
			}
		})
	}
}

func TestThreadLifecycleLoggingResumePreResolutionTerminalCorrelation(t *testing.T) {
	requestedID := hubtest.SessionID(t)
	var output bytes.Buffer
	ctx, trace := withThreadLifecycleLog(t.Context(), "resume", requestedID, &output)
	_, err := hubThreadResume(ctx, hubcore.WebConfig{}, nil, appwire.ThreadResumeParams{Session: requestedID, Ref: "local:"})
	if err == nil {
		t.Fatal("invalid ref unexpectedly succeeded")
	}
	records := assertThreadLifecycleRecords(t, output.String())
	assertThreadLifecycleOutcome(t, records, "request", "error", "failed")
	if len(records) != 2 {
		t.Fatalf("got %d records, want only request begin and complete", len(records))
	}
	for _, record := range records {
		if record["request_id"] != trace.requestID || record["session_id"] != requestedID || record["resolved_session_id"] != "-" {
			t.Errorf("lost pre-resolution request identity: %#v", record)
		}
	}
}
