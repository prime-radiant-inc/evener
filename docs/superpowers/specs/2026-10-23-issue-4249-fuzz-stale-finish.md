# Issue #4249: preserve the stale-finish snapshot

## Scope and authorization

Resolve `FuzzDelegateConversationTransitions`'s false stale-finish failure without changing delegate product behavior. Jesse authorized implementation after spec PAR. This isolated lane started at `e9d57af1e38ec863b7c7e371bb07c288e692a0f6`, fetched `origin/main` at `b62677cbcf6e46d47a20b611796536de4159fbd8`, and merged it as `b6c770f3ead94f43d926ec68ff711acfe8d5bd8e` before reproduction. Changes are limited to delegate transition tests/corpus and this spec.

Non-goals: change stale-finish errors, events, delivery timing, generation ownership, recovery, client behavior, production folding, or unrelated snapshot semantics. No new labels or broad local suites. Existing ownership and recovery contracts in product subsystem S12 and `docs/subagent-runtime-contracts.md` remain unchanged.

## Reproduction and diagnosis

Commit this exact regression at `agent/testdata/fuzz/FuzzDelegateConversationTransitions/137fa8b3ad09f989`:

```
go test fuzz v1
[]byte("07")
```

On refreshed main, the exact issue command fails (exit 1):

```sh
cd agent && go test -tags evenerfuzz -count=1 -run='FuzzDelegateConversationTransitions/137fa8b3ad09f989' .
```

The output points to `delegate_conversation_fuzz_test.go:122`, with nil error and all mutation plans nil. ASCII `0` is operation 48 modulo 12 = start; ASCII `7` is operation 55 modulo 12 = stale finish. The test starts a generation and then submits generation + 1.

The snapshot side is wrong, not production. Ranked hypotheses were: (1) JSON snapshot loses slice representation, (2) stale finish mutates durable state before its guard, (3) a timestamp representation changes. Temporary pre-call instrumentation confirmed (1): **before calling FinishGeneration**, `reflect.DeepEqual(before, c.durable)` is already false. The only aggregate-field mismatch is `PendingDeliveries`: the cloned value is `[]delegatestore.PendingDelivery(nil)`, and the actual value is `[]delegatestore.PendingDelivery{}`. Instrumentation was removed after this check.

Evidence at the reproduced base:

- `agent/delegate_tree_controller_test.go:830-841` clones state through JSON.
- `agent/internal/delegatestore/record.go:164` declares `PendingDeliveries` with `omitempty`, so JSON drops a non-nil empty slice; unmarshal produces nil.
- `agent/internal/delegatestore/fold.go:108-113` explicitly preserves non-nil empty delivery slices, and `:883` creates them in transaction copies. This is intentional production representation, not stale-finish mutation.
- `agent/delegate_tree_controller.go:879-887` rejects a generation mismatch without writes.
- `agent/delegate_tree_intents.go:513-520` returns immediately for the stale lease, suppressing its error through `:72-79`. `:167-170` returns empty plans without append/effects when events are nil.
- `agent/delegate_tree_finish.go:333-365` has no matching runtime for the stale lease and cannot install finalization state without a release decision and events.

## Fix and invariant preservation

Repair the **test snapshot**, not the production transition: after the existing JSON deep copy in `cloneDelegateControllerState`, preserve a source aggregate's non-nil empty `PendingDeliveries` slice in its clone. Limit restoration to this confirmed lossy field and shape. Non-empty deliveries remain deeply copied by JSON. Do not change any comparison in the stale-finish invariant.

No state becomes ignored. In particular, nil-to-empty and empty-to-nil mutations still compare unequal, just as delivery content, phase, generation and revision mutations do. This is stronger than comparing canonical JSON before and after, which would erase representation distinctions across unrelated fields. A hand-written full aggregate clone or reflection-based clone would add unnecessary machinery and maintenance burden for this one proven loss.

Add focused parallel table coverage for nil, non-nil empty, and populated pending-delivery snapshots. Assert full state equality immediately after cloning, then mutate source delivery state and assert the clone is unchanged and unequal. Exercise nested packet bytes for the populated case, ensuring the clone is not aliased. These are behavior/state assertions, not prose assertions.

For acceptance, temporarily inject a durable-state mutation into the production stale-lease branch, run the exact seed, observe the invariant fail, then remove that mutation and rerun green. The mutation must never be committed.

## Regression conventions and verification

`docs/developing-evener/fuzzing.md:76-90` requires retaining reproduced crashers under `testdata/fuzz/<target>/<hash>`. `make/fuzzing.mk:35-59` makes `fuzz-seeds` replay those files under `evenerfuzz`. Replay only this target locally rather than the entire cross-module seed suite; CI owns the full gate. `fuzz-goldens` at `:115-141` regenerates decoder/wire/prompt snapshots only after intended changes to those outputs; none changes here, so do not regenerate goldens. `fuzz-registry-check` at `:252-266` statically checks manifest/declarations; no target registration changes are needed.

Required checks:

1. Exact saved seed: fail before fix, pass after fix, fail under deliberate stale-state mutation, pass after removing mutation.
2. Focused snapshot table: fail before helper fix and pass after it.
3. All `FuzzDelegateConversationTransitions` seeds under `evenerfuzz`, deterministic replay.
4. `cd agent && go test -tags evenerfuzz -run='^$' -fuzz='^FuzzDelegateConversationTransitions$' -fuzztime=30s .` (cap worker parallelism if shared-machine resources require it).
5. `go vet ./agent` and `make build`.
6. `make fuzz-registry-check`.
7. Two independent competing PAR reviewers on this frozen spec, then on the implementation diff; simplify-code; commit; branch RoboRev; PR CI and shepherd-pr. Merge current main before final push as needed; merge only with current required checks green and review findings settled.

Report commands and actual results, issue/PR/merge identities, reviewed findings and any unverified work. If fixing this requires changing stale-finish product behavior, stop for a product choice instead.
