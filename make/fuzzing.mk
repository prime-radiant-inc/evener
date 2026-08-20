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
## Run the seqfuzz/schemafuzz stateful rapid.Check family at full depth:
## delegate, watch, lifecycle, jobs descendant-merge, tool-args schema,
## jobstore, two context-compaction surfaces, appserver router, appserver
## multi-session. See "The seqfuzz/schemafuzz Family Lives Only in make
## test-fuzz" in docs/developing-evener/fuzzing.md.
## proves: Each surface's rapid state machine runs its full default check
##   count (no -short reduction), catching sequence bugs the focused unit
##   suites cannot.
## trigger: Local pre-merge/post-merge for these surfaces; not run in CI's
##   default make test job. EVENER_FUZZ_TESTS=1 opts each test back in from
##   its default t.Skip.
## requires: No network, no provider calls; fully offline (deny exec env,
##   fake clock, scripted adapters).
## fails-when: Any surface's oracle/invariant failure or panic is nonzero.
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
## Replay every module's committed seed corpus (and saved crashers) under
## the evenerfuzz tag — the runtime half of the tagged-source gate whose
## compile half is make lint-evenerfuzz.
## proves: Every committed evenerfuzz-tagged seed still passes, not merely
##   still compiles.
## trigger: Absorbed as a step of make fuzz; stands alone for fast
##   iteration. Stays out of make test: 144s across the workspace against
##   make test's ~70s.
## requires: go test -run '^Fuzz' with no -fuzz, so deterministic, no
##   search.
## fails-when: Any tagged seed no longer passes.
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
## Replay every native FuzzXxx target's seed corpus plus saved crashers, and
## every registered Rapid property surface, as ordinary deterministic tests
## — the CI fuzz gate.
## proves: Fuzz invariants compile and execute, committed fuzz inputs remain
##   safe, Rapid properties replay under a fixed coverage seed bank, and
##   decode goldens remain stable.
## trigger: Required CI deterministic corpus gate; local pre-merge when
##   warranted.
## requires: Builds with -tags evenerfuzz so internal/invariant assertions
##   are live; no fuzz search or provider calls; sets EVENER_FUZZ_TESTS=1 so
##   the seqfuzz/schemafuzz family's default skip does not swallow the
##   replay.
## fails-when: Any compile, replay, invariant, Rapid, or golden failure is
##   nonzero.
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
# commit the diff. See docs/developing-evener/fuzzing.md ("Choosing an oracle").
## Regenerate the decode SNAPSHOT goldens from the current decoders. Run
## ONLY after an intended decoder change, then commit the diff.
fuzz-goldens:
	@sh -c "go test -run '^Test.*Golden\$$' ./appwire -update-goldens"
	@sh -c "cd llm && go test -run '^Test.*Golden\$$' ./providers/difftest -update-goldens"

# fuzz-nightly runs the unbounded coverage-guided search per target, bounded by a
# per-target time budget. Manual / nightly only — never in the gate.
## Run the unbounded coverage-guided search per target, bounded by a
## per-target time budget. Manual/nightly only — never in the gate.
fuzz-nightly:
	@scripts/fuzz/run-fuzz.sh $(FUZZ_ARGS)

# fuzz-triage is the local, on-demand campaign + auto-triage tool (8.7): it
# searches each surface, flake-guards and dedups any crasher, and opens ONE
# reviewable PR per distinct deterministic bug via the developer's local `gh`.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-triage FUZZ_ARGS="--time 5m"`,
# `make fuzz-triage FUZZ_ARGS=--no-pr`, or `make fuzz-triage FUZZ_ARGS=--dry-run`.
## The local, on-demand campaign + auto-triage tool: search each surface,
## flake-guard and dedup any crasher, and open one reviewable PR per
## distinct deterministic bug via the developer's local gh.
fuzz-triage:
	@scripts/fuzz/fuzz-triage.sh $(FUZZ_ARGS)

# fuzz-continuous is the LOCAL, on-demand continuous loop: it rotates over every
# native target, giving each a bounded search turn round after round (the corpus
# deepens across turns via $GOCACHE/fuzz), and routes any new crasher through
# fuzz-triage's flake-guard / dedup / PR pipeline. Runs until Ctrl-C or --total.
# Pass flags through FUZZ_ARGS, e.g. `make fuzz-continuous FUZZ_ARGS="--total 2h"`.
## The local, on-demand continuous loop: rotate over every native target
## with a bounded search turn per round, routing any new crasher through
## fuzz-triage. Runs until Ctrl-C or --total.
fuzz-continuous:
	@scripts/fuzz/fuzz-continuous.sh $(FUZZ_ARGS)

# fuzz-drive generates REAL provider traffic (varied coding tasks through the
# evener one-shot CLI, recorders on) and harvests it into the seed corpus. Makes
# live, paid provider calls — run on demand, not in CI. Flags via FUZZ_ARGS, e.g.
# `make fuzz-drive FUZZ_ARGS="--providers openai/gpt-5.4-mini --runs 5"`.
## Generate real provider traffic (varied coding tasks through the evener
## one-shot CLI, recorders on) and harvest it into the seed corpus. Makes
## live, paid provider calls — run on demand, not in CI.
fuzz-drive:
	@scripts/fuzz/fuzz-drive.sh $(FUZZ_ARGS)

# mutation-floor gates the gremlins kill score: MIN=95 fails any curated package
# whose test efficacy drops below 95%. Slow (nightly). No MIN = report only.
## Gate the gremlins kill score against a curated efficacy floor.
## proves: Each curated package's test efficacy (gremlins mutation kill
##   score) meets the floor.
## trigger: Nightly/manual; not required CI. Slow.
## requires: gremlins installed.
## fails-when: MIN=<n> is set and any curated package's kill efficacy drops
##   below it. With no MIN, this only reports.
mutation-floor:
	@scripts/fuzz/fuzz-mutation-score.sh $(if $(MIN),--min-efficacy $(MIN)) $(MUT_ARGS)

# fuzz-bisect pinpoints the commit that introduced a crasher via git bisect,
# replaying one saved corpus entry per step. Supply args through FUZZ_ARGS, e.g.
# `make fuzz-bisect FUZZ_ARGS="--target llm:FuzzParseSSE --crasher <file> --good <ref>"`.
## Find the commit that introduced a saved crasher via git bisect, replaying
## one corpus entry per step.
fuzz-bisect:
	@scripts/fuzz/fuzz-bisect.sh $(FUZZ_ARGS)

# fuzz-bisect-selftest verifies bisection end-to-end against a throwaway git repo
# whose fuzz target crashes only after a known commit (real git bisect + replay).
## Verify bisection end-to-end against a throwaway git repo whose fuzz
## target crashes only after a known commit.
## proves: fuzz-bisect names the correct commit using real git bisect and
##   real replay; only the registry source (run-fuzz.sh --list) is stubbed.
## trigger: make test-dev-tooling wave; on demand.
## requires: Offline and deterministic; builds a real throwaway git history.
## fails-when: fuzz-bisect fails to name the known-bad commit, or the suite
##   leaves files behind.
fuzz-bisect-selftest:
	@scripts/fuzz/fuzz-bisect-selftest.sh

# fuzz-oracle-audit proves every fuzz oracle reddens on its bug class (Phase 9 W1):
# each mutation in fuzz/mutations/ reintroduces a known fault in a throwaway
# worktree and the audit asserts the target FAILS. `FUZZ_ARGS=--gap-only` lists
# native targets that have no mutation yet; pass ids to audit only those.
## Prove every fuzz oracle reddens on its bug class by reintroducing a known
## fault from fuzz/mutations/ in a throwaway worktree.
## proves: Each mutation's target FAILS once the mutation is applied — an
##   oracle that stays green on a known bug is caught. FUZZ_ARGS=--gap-only
##   lists native targets with no mutation yet.
## trigger: Manual, on-demand.
## requires: A throwaway worktree per mutation; real go test runs.
## fails-when: A mutated target's oracle does not fail (a blind spot), or a
##   target fails to build under audit.
fuzz-oracle-audit:
	@scripts/fuzz/fuzz-oracle-audit.sh $(FUZZ_ARGS)

# fuzz-oracle-audit-selftest verifies the audit's caught/blind/rot/build-failure
# classification against a throwaway module (real worktree + go test, stubbed
# registry).
## Verify the oracle audit's caught/blind/rot/build-failure classification
## against a throwaway module.
## proves: The audit correctly classifies each outcome (caught, blind, rot,
##   build-failure) using a real worktree and go test, with only the
##   registry stubbed.
## trigger: make test-dev-tooling wave; on demand.
## requires: Offline and deterministic; a real throwaway module.
## fails-when: The audit's classification diverges from the fixture's
##   expected verdict, or the suite leaves files behind.
fuzz-oracle-audit-selftest:
	@scripts/fuzz/fuzz-oracle-audit-selftest.sh

# fuzz-mutation-score (Phase 10 W5) measures detection sufficiency with gremlins:
# the per-package kill rate, and the surviving (LIVED) mutants are the weak-oracle
# worklist. Nightly/manual (slow); needs gremlins installed. Pass FUZZ_ARGS to
# score specific packages, e.g. `make fuzz-mutation-score FUZZ_ARGS="llm:./providercfg"`.
## Measure detection sufficiency with gremlins: the per-package kill rate,
## where the surviving (LIVED) mutants are the weak-oracle worklist.
## Nightly/manual; needs gremlins installed.
fuzz-mutation-score:
	@scripts/fuzz/fuzz-mutation-score.sh $(FUZZ_ARGS)

# fuzz-ledger pretty-prints the triage ledger (found/fixed/quarantined counts and
# the open-bug list) from fuzz/state/ledger.json.
## Pretty-print the triage ledger — found/fixed/quarantined counts and the
## open-bug list — from fuzz/state/ledger.json.
fuzz-ledger:
	@jq -r 'to_entries | "found:     \([.[]|select(.value.status=="found")]|length)\nfixed:     \([.[]|select(.value.status=="fixed")]|length)\nquarantined: \([.[]|select(.value.status=="quarantined")]|length)"' fuzz/state/ledger.json
	@echo "--- open bugs (found) ---"
	@jq -r 'to_entries[] | select(.value.status=="found") | "  \(.key)\t\(.value.pr // "(no PR)")"' fuzz/state/ledger.json

# fuzz-gap-check is the FAST, STATIC gap gate (the blocking CI floor): it asserts
# every decode/parse package has a registered fuzz target (or a reasoned ignore),
# derived from scripts/fuzz/run-fuzz.sh --list without replaying any corpus. Seconds,
# deterministic.
## The fast, static gap gate: assert every decode/parse package has a
## registered fuzz target, or a reasoned ignore.
## proves: Every discovered decode/parse package has a registered fuzz
##   target or an explicit ignore, derived from scripts/fuzz/run-fuzz.sh
##   --list without replaying any corpus.
## trigger: Required CI; local quick check.
## requires: Seconds, deterministic; no network or corpus replay.
## fails-when: An uncovered package, or a registry/tool failure, is nonzero.
fuzz-gap-check:
	@scripts/fuzz/fuzz-gap-check.sh

# fuzz-registry-check compares native and explicitly marked rapid targets in the
# authoritative manifest with AST-discovered workspace declarations. It does not
# run ordinary tests, a fuzz search, or any network activity.
## Compare native and explicitly marked rapid targets in the authoritative
## manifest against AST-discovered workspace declarations.
## proves: scripts/fuzz/fuzz-targets.txt matches AST-discovered native/Rapid
##   declarations exactly.
## trigger: Wrapped by the required lint-fuzz-registry; also runs standalone.
##   Well under a second.
## requires: Static AST analysis only; no ordinary tests, fuzz search, or
##   network activity.
## fails-when: A discovered target has no registry row, or a registry row
##   has no discovered target.
fuzz-registry-check:
	@scripts/fuzz/fuzz-registry-check.sh

# fuzz-corpus-scan runs gitleaks over only the fuzz seed corpora — the
# corpus-scoped subset of secret-scan, for fast harvester feedback.
## Run gitleaks over only the fuzz seed corpora — the corpus-scoped subset
## of secret-scan, for fast harvester feedback.
## proves: The committed fuzz seed corpora contain no secret matching the
##   gitleaks ruleset. Unlike secret-scan, the corpora are not
##   path-allowlisted, so this genuinely inspects the seeds.
## trigger: Required CI; local harvester feedback.
## requires: gitleaks; local absence warns and returns zero unless
##   EVENER_GITLEAKS_REQUIRED=1.
## fails-when: A finding, or a required-tool absence under
##   EVENER_GITLEAKS_REQUIRED=1, is nonzero.
fuzz-corpus-scan:
	@scripts/ops/gitleaks-scan.sh corpus
