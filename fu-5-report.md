
## Round 3a (split A)

Split A fixed two findings in agent/session_tools.go (live tool-call event arg
display parity with reload). A second fixer handles the apptranscript projection
finding (split B).

### Finding A (intent gate)
- Gate the live intent-extraction unmarshal the same way reload does: added
  `json.Valid(originalArgs)` to the condition at the execTool start-event block
  (~:764), so live never unmarshals known-invalid bytes for intent.
- Note: finding A direction 1 (partial unmarshal surfacing intent) is not
  reproducible with Go's encoding/json — json.Unmarshal discards the map on any
  syntax error, never partially populating. The json.Valid gate is a defensive
  consistency change (avoids unmarshaling known-invalid bytes) rather than a
  behavior fix for dir 1. Direction 2 (oversized valid JSON) is pinned by a
  regression guard.
- Hook-input unmarshal gate (~:699) left unchanged: that path operates on the
  post-repair call.Arguments (not originalArgs) and is already guarded by
  RawArgumentsRejected; a partial parse there is harmless since a hook that
  updates input on rejected bytes is separately blocked.

### Finding B (canonical encoding)
- Compute argsJSON once from originalArgs: canonical (json.Marshal, which compacts
  and HTML-escapes, matching the transcript persistence path's json.Encoder) for
  VALID JSON; raw original bytes for INVALID JSON (the malformed cases where
  reload surfaces RawArguments verbatim). Used at all 4 ArgumentsJSON sites
  (start, canceled-end, closing-err-end, final end).
- Fixed the false comment claiming byte-identity with the prior
  json.Marshal(call.Arguments).
- Updated session_communicate_issue831_test.go: the over-limit case has valid
  JSON (trailing whitespace), so its expected ArgumentsJSON is now the canonical
  form, not the raw original.

### Gates
- gofmt -l: clean on all touched files.
- make vet: exit 0.
- make lint: exit 0 (all sub-lints pass).
- go test ./agent/ -count=1: pass.

## Round 3b (split B)

**Finding [Medium, all 3 reviewers]** — internal/apptranscript/apptranscript.go:571 — communicate raw fallback.

**Problem:** The raw fallback assumed `Arguments=={}` + `RawArguments` set means REJECTED, but that is also the durable shape of a healed-and-executed communicate (assistantHistoryMessage sets RawArguments for any invalid-JSON original). Two divergences: (a) a repaired-successful communicate rendered its raw malformed bytes as an agentMessage on reload, while live delivered the healed message; (b) a truly rejected communicate was suppressed by live but reload showed the raw bytes.

**Fix:** The raw fallback is now deferred from the assistant turn to the paired tool-result turn and gated on `IsError`. On the assistant turn, when a communicate has `Arguments={}` and `RawArguments` set, the raw bytes are stored in `toolNames` (keyed by call ID) instead of surfacing immediately. On the result turn, `IsError=true` surfaces the raw bytes as an agentMessage; `IsError=false` (healed/successful) renders nothing, matching live suppression of the delivered message.

**Synthesizer:** `synthesizeLiveEvents` (cmd/evener-hub/replay_fuzz_test.go) updated to model the same rule — the assistant turn emits nothing for a communicate with `Arguments={}` (both rejected and healed share that shape), and the multi-entry metamorphic threads the raw bytes into the live side's result turn to exercise the result-gated fallback.

**Tests:** Added `TestProjectTurn_RepairedCommunicateRendersNoRawFallback` (RED-then-green: healed communicate renders nothing on both turns). Updated `TestProjectTurn_RejectedCommunicateShowsRawFallback` to multi-turn (assistant renders nothing; result surfaces raw bytes). Added 4 seeds to `replayFuzzSeeds` (rejected + healed communicate, each as assistant+result entry pair). Added `TestHubReplay_RejectedCommunicateLiveVsReload` and `TestHubReplay_RepairedCommunicateLiveVsReload` multi-entry metamorphic tests.

**Files:** internal/apptranscript/apptranscript.go, internal/apptranscript/apptranscript_test.go, cmd/evener-hub/replay_fuzz_test.go.
