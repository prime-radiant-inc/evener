# Responses Continuation Phase 4B Context Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add session-owned Responses continuation eligibility gates for context boundaries, restored active history membership, missing anchor metadata, and `SystemPromptAsUser`.

**Architecture:** Keep runtime continuation disabled. Add a small pure helper in `agent` that inspects `[]schema.Turn` and `SessionConfig` and returns an anchor candidate plus the reason continuation must use `full_history`. Later phases can call this helper before request shaping; Phase 4B only proves the gates and does not send `previous_response_id`.

**Tech Stack:** Go, deterministic unit tests, existing `agent/schema.Turn` metadata, existing restore `resumeHistory` test seam.

---

## File Structure

- Create: `agent/responses_continuation_eligibility.go`
  - Owns `responseContextMarkerV1`, active-slice boundary detection, and pure anchor eligibility selection.
- Create: `agent/responses_continuation_eligibility_test.go`
  - Owns focused unit tests for old turns, checkpoint/summary boundaries, restore-derived active slice membership, missing marker metadata, and `SystemPromptAsUser`.
- No provider files change.
- No runtime request-shaping path changes in this phase.

## Non-Goals

- Do not select runtime anchors in `prepareModelRequest`.
- Do not set `llm.Request.PreviousResponseID`.
- Do not send `responses_delta`.
- Do not enable production endpoint-family registry entries.
- Do not persist new metadata beyond what earlier phases already added.
- Do not add live provider tests.

## Reason Strings

Use these exact `llm.ResponsesContinuationDecision.Reason` values in Phase 4B:

| Reason | Meaning |
|---|---|
| `continuation_system_prompt_as_user` | `SessionConfig.SystemPromptAsUser` is true. |
| `continuation_no_active_anchor` | No assistant turn exists after the latest checkpoint/summary boundary. |
| `continuation_anchor_metadata_missing` | Latest active assistant turn lacks required continuation-aware metadata. |
| `continuation_delta_empty` | The latest active assistant is the final active turn, so there is nothing to send as a delta. |
| `continuation_anchor_candidate` | A future phase may continue from this anchor; Phase 4B still does not dispatch it. |

## Required Helper Shape

Add this implementation target to `agent/responses_continuation_eligibility.go`:

```go
package agent

import (
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const responseContextMarkerV1 = "cont-ctx-v1"

type responsesContinuationAnchorCandidate struct {
	TurnIndex int
	Turn      schema.Turn
	Delta     []schema.Turn
}

func selectResponsesContinuationAnchorCandidate(cfg SessionConfig, history []schema.Turn) (responsesContinuationAnchorCandidate, llm.ResponsesContinuationDecision) {
	if cfg.SystemPromptAsUser {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_system_prompt_as_user",
		}
	}

	activeStart := latestContextBoundaryIndex(history) + 1
	anchorIndex := -1
	for i := len(history) - 1; i >= activeStart; i-- {
		if history[i].Kind == schema.TurnAssistant {
			anchorIndex = i
			break
		}
	}
	if anchorIndex < 0 {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_no_active_anchor",
		}
	}

	anchor := history[anchorIndex]
	if !turnHasResponsesContinuationMetadata(anchor) {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_anchor_metadata_missing",
		}
	}

	delta := append([]schema.Turn(nil), history[anchorIndex+1:]...)
	if len(delta) == 0 {
		return responsesContinuationAnchorCandidate{}, llm.ResponsesContinuationDecision{
			HistoryMode: llm.HistoryModeFullHistory,
			Reason:      "continuation_delta_empty",
		}
	}

	return responsesContinuationAnchorCandidate{
		TurnIndex: anchorIndex,
		Turn:      anchor,
		Delta:     delta,
	}, llm.ResponsesContinuationDecision{
		HistoryMode: llm.HistoryModeResponsesDelta,
		Reason:      "continuation_anchor_candidate",
	}
}

func latestContextBoundaryIndex(history []schema.Turn) int {
	for i := len(history) - 1; i >= 0; i-- {
		switch history[i].Kind {
		case schema.TurnCheckpoint, schema.TurnSummary:
			return i
		}
	}
	return -1
}

func turnHasResponsesContinuationMetadata(turn schema.Turn) bool {
	return strings.TrimSpace(turn.ResponseID) != "" &&
		strings.TrimSpace(turn.ResponseIDHash) != "" &&
		strings.TrimSpace(turn.ResponseEndpoint) != "" &&
		strings.TrimSpace(turn.ResponseStorageScopeFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseRequestFingerprint) != "" &&
		strings.TrimSpace(turn.ResponseContextMarker) == responseContextMarkerV1
}
```

Later phases may add endpoint-family/model/fingerprint/storage-age checks around this helper. Do not add those in Phase 4B.

---

### Task 1: Add Pure Context-Boundary Eligibility Tests

**Files:**
- Create: `agent/responses_continuation_eligibility_test.go`

- [ ] **Step 1: Write failing tests**

Create `agent/responses_continuation_eligibility_test.go` with:

```go
package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestResponsesContinuationAnchorCandidateRequiresMetadata(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	history[1].ResponseContextMarker = ""

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_anchor_metadata_missing" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateUsesLatestContextBoundary(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateTreatsSummaryAsBoundary(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateSelectsLatestActiveAssistant(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("before")),
		responsesContinuationEligibleAssistantTurn("resp_before"),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("after")),
		responsesContinuationEligibleAssistantTurn("resp_after"),
		schema.NewTurn(schema.TurnUserInput, llm.User("delta")),
	}

	anchor, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta ||
		decision.Reason != "continuation_anchor_candidate" {
		t.Fatalf("decision = %+v", decision)
	}
	if anchor.TurnIndex != 4 || anchor.Turn.ResponseID != "resp_after" {
		t.Fatalf("anchor = %+v", anchor)
	}
	if len(anchor.Delta) != 1 || anchor.Delta[0].Message.Text() != "delta" {
		t.Fatalf("delta = %+v", anchor.Delta)
	}
}

func TestResponsesContinuationAnchorCandidateSystemPromptAsUserUsesFullHistory(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{SystemPromptAsUser: true}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_system_prompt_as_user" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestResponsesContinuationAnchorCandidateRejectsEmptyDelta(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		responsesContinuationEligibleAssistantTurn("resp_1"),
	}

	_, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_delta_empty" {
		t.Fatalf("decision = %+v", decision)
	}
}

func responsesContinuationEligibleAssistantTurn(responseID string) schema.Turn {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant"))
	turn.Timestamp = time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	turn.ResponseID = responseID
	turn.ResponseIDHash = "cont-handle-v1:response_id:" + responseID
	turn.ResponseProvider = "openai"
	turn.ResponseModel = "gpt-5.4"
	turn.ResponseRequestModel = "gpt-5.4"
	turn.ResponseEndpoint = "https://api.openai.com/v1/responses"
	turn.ResponseStorageScopeFingerprint = "cont-scope-v1:storage_scope:" + responseID
	turn.ResponseRequestFingerprint = "cont-req-v1:" + responseID
	turn.ResponseContextMarker = responseContextMarkerV1
	return turn
}
```

- [ ] **Step 2: Run the failing tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Expected: FAIL because `selectResponsesContinuationAnchorCandidate` and `responseContextMarkerV1` do not exist.

- [ ] **Step 3: Implement the helper**

Create `agent/responses_continuation_eligibility.go` with the complete helper from "Required Helper Shape".

- [ ] **Step 4: Verify the tests pass**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git status --short
git add agent/responses_continuation_eligibility.go agent/responses_continuation_eligibility_test.go
git commit -m "feat(agent): add responses continuation context-boundary eligibility"
```

---

### Task 2: Add Restore-Derived Active Slice Test

**Files:**
- Modify: `agent/responses_continuation_eligibility_test.go`

- [ ] **Step 1: Write the failing restore test**

Append this test to `agent/responses_continuation_eligibility_test.go`:

```go
func TestResponsesContinuationAnchorCandidateUsesRestoredActiveBoundary(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	client := llm.NewClient()
	meta := schema.SessionMeta{
		ID:        "01JRESPONSES4B",
		ProfileID: "openai",
		Model:     "gpt-5.4",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 200},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	restoredHistory := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("pre-restore user")),
		responsesContinuationEligibleAssistantTurn("resp_before_restore_boundary"),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nrestored state")),
		schema.NewTurn(schema.TurnUserInput, llm.User("post-restore user")),
	}

	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.4"),
		execenv.NewLocalExecutionEnvironment(dir),
		meta,
		RestoreSessionConfig{
			StateDir:      stateDir,
			resumeHistory: restoredHistory,
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	_, decision := selectResponsesContinuationAnchorCandidate(sess.cfg, sess.history)
	if decision.HistoryMode != llm.HistoryModeFullHistory ||
		decision.Reason != "continuation_no_active_anchor" {
		t.Fatalf("decision = %+v", decision)
	}
}
```

Add the missing import:

```go
import "primeradiant.com/serf/agent/execenv"
```

- [ ] **Step 2: Run the restore test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidateUsesRestoredActiveBoundary' -count=1 -v
```

Expected before Task 1 implementation: FAIL because the helper does not exist. Expected after Task 1: PASS.

- [ ] **Step 3: Commit**

```sh
git status --short
git add agent/responses_continuation_eligibility_test.go
git commit -m "test(agent): cover restored continuation active boundary"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4b.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-4b-context-boundary.md`

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4b.md`:

```markdown
# Responses Continuation Phase 4B Proof

## Scope

Phase 4B adds pure session-owned eligibility gates for context boundaries, restored active boundary membership, missing continuation-aware anchor metadata, empty deltas, and `SystemPromptAsUser`.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not select anchors from `prepareModelRequest`, does not enable any endpoint-family registry entry, and does not make live provider requests.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- Assistant turns before the latest checkpoint/summary boundary are not active anchors.
- Restored histories use the restored checkpoint/summary boundary when evaluating active membership.
- Older assistant turns missing continuation-aware metadata are rejected.
- `SystemPromptAsUser` forces `full_history`.
- Empty deltas force `full_history`.
```

- [ ] **Step 2: Run focused verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationAnchorCandidate' -count=1 -v
git diff --check
```

- [ ] **Step 3: Mark this plan complete and commit**

Update all completed checkboxes in this plan, then:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-4b-context-boundary.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4b.md
git commit -m "docs: record responses continuation phase 4b proof"
```

## Self-Review

- Spec coverage: covers Phase 4B context-boundary marker eligibility, checkpoint/summary active-slice membership, restore-derived boundary membership, missing context marker metadata, and `SystemPromptAsUser` full-history behavior.
- Runtime safety: no `previous_response_id`, no `responses_delta`, no production registry enablement, and no live provider calls.
- Test quality: tests call a pure helper with structured `schema.Turn` fixtures rather than asserting rendered request JSON.
- Known gap by design: older-version transcript rewrite coverage is represented by assistant turns with absent optional metadata. No transcript migration is added because existing JSON omits those fields naturally.
