# Agent round-loop CPU audit

## Verdict

The dominant candidate is the repeated full-history message-assembly pipeline: every round snapshots, expands, projects, copies, and re-estimates the accumulated transcript. The context manager's fallback estimate is measurable but small, 45.4 µs at 800 turns, about 0.4% of an 11 ms round, so it does not explain the observed history-dependent rise by itself. Stable rounds do not rerender the system prompt or rebuild tool definitions; round timing fields currently misattribute request assembly to `LoopOverhead`, so the exact winning sub-pass remains unmeasured.

## Findings, ranked

Rank reflects expected CPU saved × frequency divided by fix risk. The end-to-end CPU baseline and the context-estimate microbenchmarks below were measured by Jesse and relayed for this audit; the source-path pass counts and byte totals are read/estimated, not measured in this lane.

### 1. Medium: Re-project and rebuild the full history on every model request

**Path:** `agent/session.go:1534` `processOneInput` round loop → `agent/session_model_call.go:109` `prepareModelRequestWithError` → history snapshot at `:198-205` → `expandHistory` at `:2161-2263` → media projection at `:793-805` and thinking projection at `:1955-1963` → `buildModelRequest` twice at `:297-300`, implemented at `:1322-1340`.

**Mechanism:** Each round copies all `s.history` turn structs into a new slice, scans the snapshot in `expandHistory`, and builds a new message slice. For a non-nil profile, `projectToolResultMediaFor` clones the message slice even when no media is removed. Targets that do not replay thinking also get a second projection clone. `buildModelRequest` then copies the full message slice twice: once for the target-projected primary request and once for the untrimmed fallback request. The second request exists to preserve fallback-specific thinking/media policy, but it is built unconditionally.

**Evidence:** **read**: the call path and copies are visible at the cited lines. **executed (Jesse's measurement):** `test/e2e/turncpu --rounds 200` on the Linux harness measured 2.25 s CPU over 201 rounds, 9.6–13.6 ms CPU/round; the mean rose from 9.6 ms at rounds 0–24 to 12.8–13.6 ms at rounds 100–199. This confirms history-sensitive CPU growth, but does not isolate this pass.

**Scale:** Per round, linear in retained turn count and message count. For an 800-turn/800-message proxy (actual tool-result expansion can produce more messages), a `llm.Message` is approximately 72 bytes of slice/descriptor fields on 64-bit Go. The two unconditional `buildModelRequest` copies plus media projection are about 3 × 57,600 = 172,800 bytes of message descriptors. A no-thinking-replay target adds another ~57,600 bytes. Expansion also allocates a new output slice, and the round and context-fold snapshots copy `schema.Turn` structs. The source text/blob bytes are mostly referenced by shallow copies, not duplicated by these slice copies. Jesse measured a 180 KB full-history fallback-estimate allocation at 800 turns; it is a useful scale reference, not a measurement of the total copying here. These byte totals are **read/estimated**, not measured.

**Fix design:** Keep the expanded history as the round's immutable source. Avoid materializing target-specific message slices when the target policy leaves them unchanged, and defer construction of untrimmed fallback messages until a fallback is actually selected. If safe, have `buildModelRequest` reuse the input slice when no system-prompt rewrite is needed; otherwise make only the necessary system-prefix copy. Key any retained projection by history revision and resolved target policy, and invalidate on fold, append, model switch, or media suppression. Test exact primary and fallback messages for thinking, media, continuation, and SystemPromptAsUser cases before retaining a cache.

**How to verify the win:** Run `go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchtime 5x -benchmem`, plus a history-size benchmark at 50/200/800 turns. Compare CPU and allocations per round and confirm fallback requests remain byte/semantically equivalent. Jesse's `test/e2e/turncpu --rounds 200` should show a lower high-history CPU slope.

### 2. Low: The measured context-manager fallback estimate is small; request budgeting still repeats scans

**Path:** `processOneInput` → `prepareModelRequestWithError` → `attachFullHistoryInputEstimate` at `agent/session_model_call.go:302-310` and `:749-765` → `budgetModelDispatchRequestWithBudget` at `:1753-1759` → `llm.ApplyTokenBudget` at `llm/token_budget.go:23-24`.

**Mechanism:** Request preparation applies token budgeting before and after Responses continuation planning. Each `ApplyTokenBudget` recomputes `EstimateInputTokensForResolved` over request messages. When a context manager has a provider-reported baseline, `attachFullHistoryInputEstimate` also calls `EstimateUsage(history, sysPromptChars)`. These are separate full-message/history walks, even when continuation planning leaves the request in full-history mode.

**Evidence:** **read**: `ApplyTokenBudget` calls `EstimateInputTokensForResolved` on every invocation, and the request path invokes it twice. **executed (Jesse's measurement):** the context-manager full-history estimate fallback costs 8.1 µs at 50 turns, 11.5 µs at 200, and 45.4 µs at 800, with one 180 KB allocation at 800. At 45.4 µs, this fallback is about 0.4% of an 11 ms round; it is not a leading optimization target by itself.

**Scale:** Linear in request message bytes for each budget estimate; the context-manager estimate is linear in history bytes. Jesse's fallback estimate is one small measured slice of this work. No per-pass timing for the two request-budget calls was obtained here.

**Fix design:** Carry a request-local token estimate through the first budget pass and reuse it when continuation planning does not change the estimated message content. If continuation changes the message list, estimate only that delta or the final list. Preserve target-specific media/thinking accounting and the full-history ceiling. Test full-history, delta, fallback, and provider-reported context paths against existing token-budget outputs.

**How to verify the win:** Add/extend a request-preparation benchmark with 50/200/800-turn histories; report allocations and time for each `ApplyTokenBudget` call separately. Compare against `go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchtime 5x -benchmem`.

### 3. Low: Re-copy the entire transcript while context management is checking it

**Path:** `processOneInput` → `prepareModelRequestWithError` → `foldHistoryBeforeRequest` at `agent/session_model_call.go:427-520` → history copy at `:459-464` → `strategy.ManageContext` at `:478-483`.

**Mechanism:** When a context strategy exists, every request enters `foldHistoryBeforeRequest`; each attempt copies all turns before asking the strategy to manage context. A conflict can cause a second full snapshot and strategy pass. The function also re-pairs history revision and append-log state so it can safely publish a fold; that correctness work is necessary, but the copied history is a per-request cost even when the strategy makes no change.

**Evidence:** **read**: copies and max-two-attempt loop are explicit. The round benchmark did not complete, and no context-strategy pass timing was obtained. The 45.4 µs Jesse measured is specifically the context manager's fallback estimate, not the total `ManageContext` cost.

**Scale:** One full `[]schema.Turn` shallow struct copy per request when a strategy is configured, up to two after a publication conflict; linear in turn count. Delegate requests may additionally obtain a controller snapshot before expansion.

**Fix design:** First measure no-op `ManageContext` on representative long histories. If strategy semantics permit, provide a revisioned immutable history view or let the strategy inspect a stable snapshot without a second copy; retain the existing revision-checked publication and two-attempt conflict behavior. Test concurrent appends/folds and no-op versus compaction strategies.

**How to verify the win:** Benchmark `foldHistoryBeforeRequest` with no-op and compacting strategies at 50/200/800 turns, with allocation reporting; compare to the current `BenchmarkRoundOverhead` path and verify history revision/conflict tests.

### 4. Low: Prompt text is cached, but SystemPromptAsUser concatenates it into every request

**Path:** `processOneInput` → `prepareModelRequestWithError` reads `s.cachedSystemPrompt` at `agent/session_model_call.go:133-140` → `buildModelRequest` at `:1322-1340` → `prependSystemPromptToUserMessage` at `agent/session_prompts.go:41-49`.

**Mechanism:** The prompt renderer is not called per round: `refreshSystemPromptCache` builds the cached value at `session_prompts.go:73-87`, and its callsites are initialization or state changes (model, environment, goal, tools). On a stable session, 100% of the cached prompt text is unchanged between rounds. But when `SystemPromptAsUser` is enabled and the first history message is user-role, `prependSystemPromptToUserMessage` constructs a new `systemPrompt + "\n\n"` string on each `buildModelRequest`; because the request is built twice, this can happen twice per round.

**Evidence:** **read**: cached prompt read and concatenation code; no per-round prompt render call. The `SystemPrompt` timer covers cache lookup, not prompt rendering. No prompt-copy benchmark was run.

**Scale:** Conditional; O(system-prompt bytes) per request, twice per round while both primary and fallback requests are assembled. Prompt bytes themselves are unchanged on stable rounds; only the user message to prepend can differ after history updates.

**Fix design:** Avoid constructing the untrimmed request eagerly, and reuse a prepared system/user prefix for the primary and fallback request when its content is identical. Do not cache across history changes unless keyed to the exact first user message and prompt revision. Test first-message environment context, normal user input, compacted history, and fallback parity.

**How to verify the win:** Benchmark `buildModelRequest` with `SystemPromptAsUser` on/off and a realistic prompt size; count allocations and compare requests byte-for-byte.

### 5. Low: Round timings hide request assembly inside LoopOverhead

**Path:** `processOneInput` sets `roundStart` at `agent/session_lifecycle.go:1549`, calls `prepareModelRequestWithError` at `:1604`, and computes residual `LoopOverhead` at `:1953-1956`. Request construction and budget passes occur in `session_model_call.go:292-310` after `HistoryExpand` is recorded at `:286`.

**Mechanism:** `events.RoundTimings.ToolDefs` is never assigned in the round path. `HistoryExpand` stops before target projections, building both requests, token budgeting, continuation planning, and delivery. Those operations are consequently charged to residual `LoopOverhead`, even though the `RoundTimings` comments describe `ToolDefs` as selection and `LoopOverhead` as loop-control work. The timings are wall time, not CPU time.

**Evidence:** **read**: `rg -n 'ToolDefs|HistoryExpand|ContextMgmt|SystemPrompt|RoundTimings' agent/session_model_call.go agent/session_lifecycle.go` showed assignments only for `SystemPrompt`, `ContextMgmt`, and `HistoryExpand`; the ToolDefs field stays zero. No emitted event from a live benchmark was captured.

**Scale:** One attribution error per round; it grows with history because the unmeasured request-assembly work grows with history. It does not itself burn CPU, but it prevents phase-level prioritization.

**Fix design:** Time projection/request assembly/budgeting explicitly in `prepareModelRequestWithError`; either assign ToolDefs only around actual tool-definition work or rename/split the phase. Keep wall-time labels distinct from CPU profile data. Add a test asserting nonzero phase attribution for a prepared request with tools/history and that phase sums plus residual reconcile with `TotalRound` within timer overhead.

**How to verify the win:** Capture `ROUND_TIMINGS` from the mock-provider benchmark and compare phase totals before/after; use a CPU profile separately for CPU attribution.

### 6. Low: The public StreamGenerate path rebuilds a partial response for every text delta, but the tree does not call it in production

**Path:** External caller of `llm.StreamGenerate` at `llm/stream_generate.go:153` → `openAndConsumeStream` at `:210-229` → `acc.PartialResponse()` per `StreamEventTextDelta` at `:222-228` → `StreamAccumulator.PartialResponse` and `buildResponse` at `llm/stream_accumulator.go:177-215`.

**Mechanism:** Each text delta marks the accumulator dirty; `PartialResponse` rebuilds accumulated response text by iterating all accumulated text buffers. With D deltas and growing output, total copied/rebuilt text can be quadratic in output size. The agent's own model-stream path uses `s.client.Stream` directly at `agent/session_stream.go:210` and its own consumption path, not `StreamGenerate`.

**Evidence:** **read**: per-delta call and full accumulated-string rebuild. `rg -n 'StreamGenerate\\(' --glob '*.go'` found the definition and test callsites but no non-test in-tree caller. `Generate` calls `gs.client.Complete` at `llm/generate.go:304-325`; `GenerateObject` delegates through Generate, not StreamGenerate. External package callers remain possible.

**Scale:** O(accumulated output bytes) per text delta, O(D²) for fixed-size deltas; zero in this repository's production call paths as currently found. That lack of an in-tree production caller makes this lower priority than the agent's repeated history passes.

**Fix design:** Keep one partial response snapshot per consumer request rather than rebuild it automatically for every delta, or publish deltas without a full response rebuild and build a snapshot only when `PartialResponse` is requested. Preserve the documented best-effort snapshot contract. Test response content over interleaved deltas and tool calls, and benchmark 100/500/1000 deltas.

**How to verify the win:** Add `BenchmarkStreamGenerate` with increasing text-delta counts and `-benchmem`; verify near-linear total time/bytes while preserving the existing StreamGenerate tests.

### 7. Low: CPU-profile benchmark run panics in the provider-stamping stream

**Path:** `BenchmarkRoundOverhead` → mock adapter → streaming client wrapper `newProviderStampStream` at `llm/client.go:843-851` → `providerStampStream.pump` dereferences `s.inner.Events()` at `:861-869`.

**Mechanism:** The benchmark command with `-cpuprofile` exited nonzero with a nil-pointer panic in `providerStampStream.pump` at `llm/client.go:868`. The call to `s.inner.Events()` has no nil guard. The observed stack does not by itself prove why `inner` was nil; whether the profile option exposes a benchmark/mock-stream lifecycle race remains open. This is a benchmark-only failure in this run, not evidence of a production crash.

**Evidence:** **executed:** `go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchtime 5x -benchmem -cpuprofile "$EVENER_SCRATCH_DIR/round-loop.cpu"` exited 1 after 76.679 s with `panic: runtime error: invalid memory address or nil pointer dereference`, stack `llm.(*providerStampStream).pump` at `llm/client.go:868`. No CPU profile was produced for analysis.

**Scale:** No production frequency established. It blocks this profile-enabled benchmark and therefore obscures CPU attribution, but it does not explain per-round CPU use.

**Fix design:** In the LLM streaming boundary, reject a nil stream before starting the pump or treat a nil inner stream as closed; add a test adapter that returns nil and exercise the wrapper's close/finish behavior. The root cause is outside this agent-loop lane and was not changed.

**How to verify the win:** Re-run the exact `BenchmarkRoundOverhead` command with `-cpuprofile` and confirm it exits zero and writes a readable profile; retain a nil-stream regression test in the LLM package.

## Clean

- **System prompt render:** `prepareModelRequestWithError` reads `cachedSystemPrompt` at `agent/session_model_call.go:133-140`. The normal per-round path does not call `renderSystemPrompt`; `refreshSystemPromptCache` is called during initialization and on model/environment/goal/tool changes (`agent/session_init.go:1893-1894`, `agent/session.go:2176`, `agent/session_env_swap.go:186-187`, `agent/session_goal.go:75-76`, `agent/session_tools.go:120-121`). On a stable session, the rendered prompt has no per-round rebuild and 0% content change.
- **Tool definitions:** `rebuildToolDefsCache` constructs `cachedToolDefs` at `agent/session_tool_definitions.go:219-362`; `prepareModelRequestWithError` reads that slice under the session lock at `agent/session_model_call.go:133-140`. No clone, sort, filter, or schema normalization occurs in the per-round path. Request building assigns the cached slice to `llm.Request.Tools` at `:1342-1348`.
- **Delegate-count work:** delegate-session requests may take and project a controller history snapshot (`agent/session_model_call.go:250-283`), but no per-round loop over all delegates was found in the normal request-preparation path. Delegate count is therefore not an identified multiplier here.
- **Fallback estimate size:** Jesse's measurement reports 8.1 µs at 50 turns, 11.5 µs at 200, 45.4 µs at 800, and one 180 KB allocation at 800. This is real linear work but only about 0.4% of an 11 ms round at 800 turns; prioritize larger request assembly passes first.

## Open questions

- Which specific history pass accounts for the remaining ~10 ms CPU/round? The source shows several complete passes, but this lane did not obtain successful benchmark or CPU-profile output to allocate time among them. The ~180 KB estimate timing is too small to explain the whole round.
- Does `SystemPromptAsUser` occur often enough in production to justify optimizing its conditional prompt concatenation?
- Should `StreamGenerate` retain eager partial snapshots for external API consumers, given no current production caller in this tree?
- What exactly makes the mock adapter's wrapped stream nil under `-cpuprofile`? The stack and unguarded dereference are known; the nil's origin is not.
- Should phase accounting be changed to report CPU samples, or should it remain wall-time instrumentation with request-preparation phases split out?

## Measurements and limits

Commands run:

- `go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchtime 5x -benchmem -cpuprofile "$EVENER_SCRATCH_DIR/round-loop.cpu"`, **failed** after 76.679 s with the nil dereference above.
- `go test ./agent -run '^$' -bench 'BenchmarkRoundOverhead' -benchtime 5x -benchmem`, first attempt failed package compilation because a temporary `agent/zz_audit_round_test.go` in the shared worktree had an invalid `json.RawMessage` literal; the owner fixed that file. The corrected retry was stopped at the parent's direction to stop benchmarking and produced no output.
- `go test ./agent -run '^$' -bench 'BenchmarkNewSession|BenchmarkProfileToolRegistry|BenchmarkContextStatusSampling' -benchtime 20x -benchmem`, stopped before producing output when the parent requested no further benchmarks.
- `python3 -c 'print("per 800-message slice:", 800*72, "bytes; four copies:", 4*800*72, "bytes; five copies:", 5*800*72, "bytes")'`, printed `per 800-message slice: 57600 bytes; four copies: 230400 bytes; five copies: 288000 bytes`.

No successful local per-round benchmark, per-phase event capture, or CPU profile was obtained. The round baseline and context-estimate measurements cited above are Jesse's Linux-harness measurements, not measurements from this audit process. The requested `BenchmarkNewSession|BenchmarkProfileToolRegistry|BenchmarkContextStatusSampling` run did not complete; Jesse separately supplied the transcript-lane `BenchmarkNewSession` result of 5.63 s, 7.53 MB, and 76,473 allocations. No source files were changed.
