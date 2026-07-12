//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
)

// FuzzJobtoolsContractProgram covers the job-tool result and validation branches
// that do not require a live provider. It uses a real durable job manager for
// status, list, and idempotent terminal-stop behavior, then exercises the same
// bounded result encoders used by delegate and delegate_send.
func FuzzJobtoolsContractProgram(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 1, 0, 1, 0, 1, 0})
	f.Add([]byte{2, 1, 2, 1, 2, 1, 2, 1, 32})
	f.Add([]byte{255, 254, 253, 252, 251, 250, 249, 248, 247})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jobtools_reader{data: data}
		s := newSession(t)
		freezeClock(s.jobManager)

		jobID := "job_contract"
		started := frozenTestTime.Add(-2 * time.Second)
		ended := frozenTestTime.Add(-time.Second)
		exitCode := r.intn(4)
		if err := s.jobManager.appendEvents([]jobstore.Event{
			{
				Kind:             jobstore.EventJobStarted,
				TS:               started,
				JobID:            jobID,
				Type:             jobstore.JobShell,
				OwnerSessionID:   s.ID(),
				VisibleToSession: s.ID(),
				StartedAt:        &started,
				Command:          "contract shell",
				Description:      "contract job",
			},
			{
				Kind:        jobstore.EventJobFinished,
				TS:          ended,
				JobID:       jobID,
				Status:      jobstore.StatusCompleted,
				Reason:      "done",
				EndedAt:     &ended,
				ExitCode:    &exitCode,
				TerminalGen: jobstore.NewTerminalGeneration(),
			},
		}); err != nil {
			t.Fatalf("seed terminal job: %v", err)
		}

		for name, call := range map[string]func() (any, error){
			"status": func() (any, error) {
				return jobStatusTool(s, map[string]any{"job_id": jobID}, jobToolResultDefaultMaxChar)
			},
			"list": func() (any, error) {
				return jobListTool(s, map[string]any{
					"status": []any{string(jobstore.StatusCompleted)},
					"type":   []any{string(jobstore.JobShell)},
				}, jobToolResultDefaultMaxChar)
			},
			"stop": func() (any, error) {
				return jobStopTool(context.Background(), s, map[string]any{
					"job_id": jobID, "include_children": r.booln(),
				}, jobToolResultDefaultMaxChar)
			},
		} {
			result, err := call()
			if err != nil {
				t.Fatalf("%s terminal job: %v", name, err)
			}
			state, ok := result.(tool.StateResult)
			if !ok || state.Output == "" {
				t.Fatalf("%s result = %#v, want non-empty StateResult", name, result)
			}
			if _, err := json.Marshal(state.State); err != nil {
				t.Fatalf("%s state: %v", name, err)
			}
		}

		// Runtime callbacks and ordinary delegate sends take different projection
		// paths. Both must remain structured and deterministic for either delivery
		// outcome and for bounded foreground output.
		runtimeSend := sendMessageResult{
			MessageType: "runtime",
			Delivered:   r.booln(),
			Action:      "callback",
		}
		jobtoolsAssertStableStateResult(t, func() (any, error) {
			return marshalDelegateSendResult(runtimeSend, 1+r.intn(2048))
		})

		output := strings.Repeat(r.str()+"\n", 1+r.intn(16))
		ordinarySend := sendMessageResult{
			DelegateID:               "dlg_contract",
			StartedJobID:             "job_started",
			JobID:                    jobID,
			LatestJobID:              jobID,
			Type:                     string(jobstore.JobDelegate),
			Status:                   jobstore.StatusCompleted,
			Reason:                   "complete",
			Action:                   "started",
			Output:                   output,
			Truncated:                r.booln(),
			StructuredResult:         map[string]any{"value": r.str()},
			StructuredResultValidSet: true,
			StructuredResultValid:    r.booln(),
		}
		jobtoolsAssertStableStateResult(t, func() (any, error) {
			return marshalDelegateSendResult(ordinarySend, 1+r.intn(2048))
		})

		delegate := delegateResult{
			DelegateID:               "dlg_contract",
			JobID:                    jobID,
			Type:                     string(jobstore.JobDelegate),
			Status:                   jobstore.StatusCompleted,
			TranscriptRef:            "local:contract",
			Output:                   output,
			StructuredResult:         map[string]any{"value": r.str()},
			StructuredResultValidSet: true,
			StructuredResultValid:    r.booln(),
			Worktree:                 &delegateWorktreeReport{Path: "/tmp/contract", Branch: "contract", Ahead: r.intn(4), Dirty: r.booln()},
			Sandbox:                  &delegateSandboxReport{Mode: "workspace-write", Network: r.booln()},
		}
		maxChars := 800 + r.intn(1200)
		first, firstErr := marshalDelegateResult(delegate, maxChars)
		second, secondErr := marshalDelegateResult(delegate, maxChars)
		if (firstErr == nil) != (secondErr == nil) || first != second {
			t.Fatalf("delegate result is non-deterministic: (%q, %v) != (%q, %v)", first, firstErr, second, secondErr)
		}
		if firstErr == nil {
			if !json.Valid([]byte(first)) || jsonCharLen([]byte(first)) > maxChars {
				t.Fatalf("delegate result violates JSON budget %d: %q", maxChars, first)
			}
		}

		// Retained internal watch-send parsing remains a compatibility boundary
		// even though the public job_watch surface rejects the field.
		watchShapes := []map[string]any{
			{},
			{"send": map[string]any{}},
			{"send": map[string]any{"to": "dlg_contract", "message": r.str(), "include_excerpt": r.booln()}},
			{"send": map[string]any{"message": "missing target"}},
			{"send": "invalid"},
		}
		for _, args := range watchShapes {
			first, firstErr := watchSendArg(args)
			second, secondErr := watchSendArg(args)
			if (firstErr == nil) != (secondErr == nil) || !jobtoolsSameWatchSend(first, second) {
				t.Fatalf("watchSendArg is non-deterministic for %#v", args)
			}
		}
	})
}

func jobtoolsAssertStableStateResult(t *testing.T, call func() (any, error)) {
	t.Helper()
	first, firstErr := call()
	second, secondErr := call()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("state result errors: (%v, %v)", firstErr, secondErr)
	}
	firstState, firstOK := first.(tool.StateResult)
	secondState, secondOK := second.(tool.StateResult)
	if !firstOK || !secondOK {
		t.Fatalf("state result types = (%T, %T)", first, second)
	}
	firstJSON, err := json.Marshal(firstState.State)
	if err != nil {
		t.Fatalf("marshal first state: %v", err)
	}
	secondJSON, err := json.Marshal(secondState.State)
	if err != nil {
		t.Fatalf("marshal second state: %v", err)
	}
	if firstState.Output != secondState.Output || string(firstJSON) != string(secondJSON) {
		t.Fatalf("state result is non-deterministic")
	}
}

func jobtoolsSameWatchSend(a, b *watchSendArgs) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.To == b.To && a.Message == b.Message && a.IncludeExcerpt == b.IncludeExcerpt
}
