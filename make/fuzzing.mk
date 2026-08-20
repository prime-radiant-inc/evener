.PHONY: test-fuzz mutation-floor fuzz fuzz-seeds fuzz-nightly fuzz-triage fuzz-continuous fuzz-drive fuzz-bisect fuzz-bisect-selftest fuzz-oracle-audit fuzz-oracle-audit-selftest fuzz-mutation-score fuzz-ledger fuzz-gap-check fuzz-registry-check fuzz-goldens fuzz-corpus-scan

# Fuzz replay is a deterministic evidence gate: never inherit a developer's
# persisted Go configuration or GOFLAGS, and always use this checkout's workspace.
override FUZZ_GOWORK := $(abspath $(CURDIR)/go.work)

# test-fuzz runs the seqfuzz/schemafuzz stateful rapid.Check family (delegate,
# watch, lifecycle, jobs descendant-merge, tool-args schema, jobstore, context
# compaction x2, appserver router, appserver multi-session) at FULL depth:
# EVENER_FUZZ_TESTS=1 opts each t.Skip()-gated test back in, and the absence of
# -short lets rapid.Check run its full default check count instead of the
# reduced one it uses under -short. These tests never run inside `make test`
# (Jesse ruling: no fuzz-family test, including smoke iterations, belongs in
# the default suite). This is distinct from `make fuzz`, which replays native
# FuzzXxx corpora and this SAME rapid family against a fixed coverage seed
# bank (scripts/fuzz/run-fuzz.sh) for deterministic CI coverage, not a search.
test-fuzz:
	@cd agent && EVENER_FUZZ_TESTS=1 go test . ./internal/contextmgr ./internal/jobstore -run 'SeqFuzz|ToolArgsSchemaFuzz' -count=1 -v
	@EVENER_FUZZ_TESTS=1 go test ./internal/appserver -run 'SeqFuzz' -count=1 -v

# FUZZ_SEED_REPLAY replays every module's COMMITTED seed corpus (and saved
# crashers) under the evenerfuzz tag. `go test -run '^Fuzz'` with no `-fuzz` does
# not search, so this is deterministic. `make fuzz` runs it as one of its steps;
# fuzz-seeds is the same work on its own.
FUZZ_SEED_REPLAY = for m in $(FUZZ_GO_MODULES); do (GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c "cd $$m && go test -run '^Fuzz' -tags evenerfuzz -count=1 ./...") || exit 1; done

# fuzz-seeds is the RUNTIME half of the tagged-source gate whose compile half is
# `make lint-evenerfuzz`. It stays out of `make test` on measured cost: 144s
# across the workspace (the root module ~39s and agent ~65s of that) against a
# ~70s `make test`, for a class `make fuzz` and CI already replay. lint-evenerfuzz
# catches a tagged call site stranded by a signature change in ~4s; this catches
# the rest — a tagged seed that still compiles but no longer passes.
fuzz-seeds:
	@$(FUZZ_SEED_REPLAY)

# fuzz runs every native FuzzXxx target's seed corpus plus saved crashers as
# ordinary deterministic tests — `go test -run '^Fuzz'` with no `-fuzz` does not
# random-search. It also replays every registered Rapid property surface with
# the fixed coverage seed bank. It is still fuzz coverage, so it stays out of
# the regular test gate and runs through this explicit entry point.
#
# Everything here builds with -tags evenerfuzz so the internal/invariant assertions
# (primeradiant.com/evener/invariant) are live: a tripped invariant panics and the
# never-panic oracle catches it. The first step verifies the mechanism itself
# fires under the tag; production builds and `make test` stay tag-free.
fuzz:
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd agent && go test -run "^$$" -tags evenerfuzz -count=1 ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd invariant && go test -tags evenerfuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'cd fuzz && go test -tags evenerfuzz ./...'
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c 'go test ./cmd/evener-fuzzcov ./cmd/evener-fuzz-harvest'
	@$(FUZZ_SEED_REPLAY)
	@scripts/fuzz/rapid-replay.sh
	@GOENV=off GOFLAGS= GOWORK="$(FUZZ_GOWORK)" sh -c "go test -run '^Test.*Golden\$$' ./appwire"

# fuzz-goldens regenerates the decode SNAPSHOT goldens — evener's differential
# oracle. Each decode target's committed seed corpus is replayed and its decoded
# output canonically re-encoded into appwire/testdata/golden/. A code change that
# silently alters a decoder's output (no panic, round-trip still holds) fails the
# `make fuzz` golden check; run this ONLY after an INTENDED decoder change, then
# commit the diff. See docs/fuzzing.md ("Choosing an oracle").
fuzz-goldens:
	@sh -c "go test -run '^Test.*Golden\$$' ./appwire -update-goldens"
	@sh -c "cd llm && go test -run '^Test.*Golden\$$' ./providers/difftest -update-goldens"

# fuzz-nightly runs the unbounded coverage-guided search per target, bounded by a
# per-target time budget. Manual / nightly only — never in the gate.
fuzz-nightly:
	@scripts/fuzz/run-fuzz.sh $(FUZZ_ARGS)

# fuzz-triage is the local, on-demand campaign + auto-triage tool (8.7): it
# searches each surface, flake-guards and dedups any crasher, and opens ONE
# reviewable PR per distinct deterministic bug via the developer's local `gh`.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-triage FUZZ_ARGS="--time 5m"`,
# `make fuzz-triage FUZZ_ARGS=--no-pr`, or `make fuzz-triage FUZZ_ARGS=--dry-run`.
fuzz-triage:
	@scripts/fuzz/fuzz-triage.sh $(FUZZ_ARGS)

# fuzz-continuous is the LOCAL, on-demand continuous loop: it rotates over every
# native target, giving each a bounded search turn round after round (the corpus
# deepens across turns via $GOCACHE/fuzz), and routes any new crasher through
# fuzz-triage's flake-guard / dedup / PR pipeline. Runs until Ctrl-C or --total.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-continuous FUZZ_ARGS="--total 2h"`.
fuzz-continuous:
	@scripts/fuzz/fuzz-continuous.sh $(FUZZ_ARGS)

# fuzz-drive generates REAL provider traffic (varied coding tasks through the
# evener one-shot CLI, recorders on) and harvests it into the seed corpus. Makes
# live, paid provider calls — run on demand, not in CI. Flags via FUZZ_ARGS, e.g.
# `make fuzz-drive FUZZ_ARGS="--providers openai/gpt-5.4-mini --runs 5"`.
fuzz-drive:
	@scripts/fuzz/fuzz-drive.sh $(FUZZ_ARGS)

# mutation-floor gates the gremlins kill score: MIN=95 fails any curated package
# whose test efficacy drops below 95%. Slow (nightly). No MIN = report only.
mutation-floor:
	@scripts/fuzz/fuzz-mutation-score.sh $(if $(MIN),--min-efficacy $(MIN)) $(MUT_ARGS)

# fuzz-bisect pinpoints the commit that introduced a crasher via git bisect,
# replaying one saved corpus entry per step. Supply args through FUZZ_ARGS, e.g.
# `make fuzz-bisect FUZZ_ARGS="--target llm:FuzzParseSSE --crasher <file> --good <ref>"`.
fuzz-bisect:
	@scripts/fuzz/fuzz-bisect.sh $(FUZZ_ARGS)

# fuzz-bisect-selftest verifies bisection end-to-end against a throwaway git repo
# whose fuzz target crashes only after a known commit (real git bisect + replay).
fuzz-bisect-selftest:
	@scripts/fuzz/fuzz-bisect-selftest.sh

# fuzz-oracle-audit proves every fuzz oracle reddens on its bug class (Phase 9 W1):
# each mutation in fuzz/mutations/ reintroduces a known fault in a throwaway
# worktree and the audit asserts the target FAILS. `FUZZ_ARGS=--gap-only` lists
# native targets that have no mutation yet; pass ids to audit only those.
fuzz-oracle-audit:
	@scripts/fuzz/fuzz-oracle-audit.sh $(FUZZ_ARGS)

# fuzz-oracle-audit-selftest verifies the audit's caught/blind/rot/build-failure
# classification against a throwaway module (real worktree + go test, stubbed
# registry).
fuzz-oracle-audit-selftest:
	@scripts/fuzz/fuzz-oracle-audit-selftest.sh

# fuzz-mutation-score (Phase 10 W5) measures detection sufficiency with gremlins:
# the per-package kill rate, and the surviving (LIVED) mutants are the weak-oracle
# worklist. Nightly/manual (slow); needs gremlins installed. Pass FUZZ_ARGS to
# score specific packages, e.g. `make fuzz-mutation-score FUZZ_ARGS="llm:./providercfg"`.
fuzz-mutation-score:
	@scripts/fuzz/fuzz-mutation-score.sh $(FUZZ_ARGS)

# fuzz-ledger pretty-prints the triage ledger (found/fixed/quarantined counts and
# the open-bug list) from fuzz/state/ledger.json.
fuzz-ledger:
	@jq -r 'to_entries | "found:     \([.[]|select(.value.status=="found")]|length)\nfixed:     \([.[]|select(.value.status=="fixed")]|length)\nquarantined: \([.[]|select(.value.status=="quarantined")]|length)"' fuzz/state/ledger.json
	@echo "--- open bugs (found) ---"
	@jq -r 'to_entries[] | select(.value.status=="found") | "  \(.key)\t\(.value.pr // "(no PR)")"' fuzz/state/ledger.json

# fuzz-gap-check is the FAST, STATIC gap gate (the blocking CI floor): it asserts
# every decode/parse package has a registered fuzz target (or a reasoned ignore),
# derived from scripts/fuzz/run-fuzz.sh --list without replaying any corpus. Seconds,
# deterministic.
fuzz-gap-check:
	@scripts/fuzz/fuzz-gap-check.sh

# fuzz-registry-check compares native and explicitly marked rapid targets in the
# authoritative manifest with AST-discovered workspace declarations. It does not
# run ordinary tests, a fuzz search, or any network activity.
fuzz-registry-check:
	@scripts/fuzz/fuzz-registry-check.sh

# fuzz-corpus-scan runs gitleaks over only the fuzz seed corpora — the
# corpus-scoped subset of secret-scan, for fast harvester feedback.
fuzz-corpus-scan:
	@scripts/ops/gitleaks-scan.sh corpus
