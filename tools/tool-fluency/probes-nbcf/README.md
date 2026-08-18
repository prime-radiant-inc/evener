# kata nbcf: diagnosis-to-action phase-transition eval (scaffold)

Kata nbcf's acceptance has two halves. The first — positive phase-transition
prompting in `agent/prompts/sections/workflow.md.tmpl`,
`communicate.md.tmpl`, and `verification.md` — is implemented and covered by
deterministic tests in `agent/section_resolver_test.go`. This directory is
the **scaffold** for the second half: an opt-in agentic eval that measures
whether the new prompting actually reduces analysis-churn on a seeded
configuration-path failure, without a live model run happening in this
worktree (kata rule: no live LLM API calls from an implementer).

## What's here

- `diagnostic_fix.seeded_config_path.yaml` — one tool-fluency probe manifest
  (see `tools/tool-fluency/README.md` for the schema). It ships a tiny,
  self-contained `configpath` Go package with a deliberately seeded
  environment/configuration-path bug (modeled on the real HOME/XDG/
  SERF_PROVIDERS_CONFIG precedence bug that kata nbcf's incident report
  describes — see "Premise check" below) and a prompt describing the
  *symptom* only. The model must diagnose the root cause, write one
  regression test, apply the smallest fix, and verify, then report a
  `DIAGNOSIS_COMPLETE` token.
- `../cmd/serf-fluency/nbcf_probe_test.go` — an offline, deterministic test
  (`go test ./tools/tool-fluency/cmd/serf-fluency/ -run
  TestNBCFSeededConfigPathProbeLoadsAndValidates`) proving the manifest
  parses, the fixture ships the bug (not the fix), and the pass/fail gate
  actually discriminates a stalled transcript from a completed one. This is
  the only part of the scaffold that runs today; it makes no LLM calls.

It is deliberately **not** under `tools/tool-fluency/probes/`, so an ordinary
`--probe all` run of the core suite never picks it up — this probe is far
heavier (up to 40 tool rounds) and measures a different thing (phase
discipline on a multi-step diagnose-and-fix task, not single-tool-call
fluency).

## Premise check (per kata nbcf's METHOD instructions)

Before scaffolding this, I searched `agent/prompts/` for any existing
phase-transition or analysis-budget language and found none — the premise
that no such prompting exists on `origin/main` (`ea6cc396d`) holds.

I also found that the *specific* incident kata nbcf describes (an agent
stalling on HOME/XDG/`SERF_PROVIDERS_CONFIG` propagation during a
tool-fluency run) already has its underlying bug fixed on main:
`agent/internal/liveeval/paths.go` (commit `403e580c3`, "test(eval): gate
live suites and resolve local paths") is that forced bounded fix, with
`paths_test.go` pinning it. That fix is the *product* of the stall the kata
wants to prevent recurring — it does not itself add the missing prompting.
This probe seeds a fresh, analogous bug rather than reusing that one, both
because the real one is already fixed and because reusing a fixed bug would
make the eval untestable (there would be nothing left to diagnose).

## How to run it (controller / whoever has live credentials)

This eval compares CURRENT prompting (this repo before kata nbcf's commit)
against CANDIDATE prompting (this repo including it) on the same seeded
bug, across three models. Two binaries, not `--system-prompt-append`: the
new rules are baked into the shipped sections, not appended text, so a fair
A/B needs two `serf` builds.

```sh
# 1. Build the baseline (no phase-transition prompting) in a disposable
#    worktree at this kata's base commit. Do not touch other worktrees;
#    this creates a new one.
git worktree add /tmp/nbcf-baseline-wt ea6cc396d
(cd /tmp/nbcf-baseline-wt && go build -o /tmp/nbcf-baseline-serf ./cmd/serf)

# 2. Build the candidate from this branch (kata/uq-nbcf, or wherever this
#    landed on main).
go build -o /tmp/nbcf-candidate-serf ./cmd/serf

# 3. Run the probe against both binaries, for each model below. Repeat
#    --repetitions 3+ per arm; single-shot runs are not comparable.
for arm in baseline candidate; do
  bin=/tmp/nbcf-$arm-serf
  go run ./tools/tool-fluency/cmd/serf-fluency run \
    --serf-bin "$bin" \
    --model <provider/model> \
    --probes-dir tools/tool-fluency/probes-nbcf \
    --probe diagnostic_fix.seeded_config_path \
    --reasoning-effort low \
    --repetitions 3 \
    --max-rounds 40 \
    --out "/tmp/serf-nbcf-eval-$arm-<model-slug>"
done

# 4. Clean up the disposable worktree when done.
git worktree remove /tmp/nbcf-baseline-wt
```

### The three required models (kata nbcf acceptance)

| Kata's name | Model ref to actually use | Note |
| --- | --- | --- |
| GPT-5.6 Luna low | `openai/gpt-5.6-luna` | Real model (`llm/providers/openai/responses.go`'s `codexModelVariants`). |
| the smallest supported GPT mini low | `openai/gpt-5.4-mini` | **Premise correction**: `gpt-5.6-mini` is not a real model — confirmed absent from `llm/data/litellm_model_catalog.json` and explicitly called out as such in `docs/superpowers/plans/2026-08-06-ws7-launch-validation.md`. `gpt-5.4-mini` is the smallest GPT mini this codebase actually supports (it's already `serf-fluency`'s own default model). |
| Kimi K2.6 low | `kimi/kimi-k2.6` | Real model (`llm/providers/kimi/adapter_test.go`). |

`low` is a valid reasoning-effort string for all three providers'
`defaultEfforts` (`agent/provider/profile.go`). Pass `--fast-cheap-model`
matching each arm's primary model, and `--clear-openai-api-key` for the
OpenAI arms if running through OAuth per the main README's convention.

### Reading results

Compare, per arm and model: `results.jsonl`/`summary.json`'s
`status` (passed/failed), `canonical_tool_counts`/`model_tool_counts`
(`shell` count is a proxy for test-run iterations), and manually read the
`state_dir` transcripts for the specific things the kata acceptance wants
measured (see "What remains" — the runner does not compute these yet):
how many tool rounds elapsed before the first `go test` call, whether the
model read/greped the same file repeatedly without new information, and
whether it reported `DIAGNOSIS_COMPLETE` with a coherent root cause versus
converging on the wrong fix.

## What remains for the controller

This scaffold deliberately stops short of full kata nbcf acceptance:

1. **The runner does not yet compute phase-transition metrics.**
   `probeFile.Metrics` (`tools/tool-fluency/cmd/serf-fluency/main.go:161`)
   is parsed from YAML but never read — `grep '\.Metrics\b'` in that
   package has zero hits. The probe's `metrics:` block names the fields
   kata nbcf's acceptance wants (`wants_investigative_call_count`,
   `wants_repeated_read_or_grep_count`, `wants_tool_round_of_first_test_run`,
   `wants_tool_round_of_first_source_edit`,
   `wants_premature_fix_before_red_test_flag`) as a spec for that work, not
   as something already wired up. Implementing it means walking
   `doctor.TranscriptResult.Turns` (already available via
   `allTranscriptToolCounts` in the same file) turn-by-turn instead of only
   aggregating counts, and it should get its own offline unit test against a
   synthetic transcript fixture before any live run is trusted to interpret
   it.
2. **No live runs have happened.** This worktree made zero LLM API calls,
   per kata rule. The three-model A/B above needs to actually run.
3. **No baseline-vs-candidate churn comparison exists yet.** That requires
   (1) and (2) both done, then a report comparing analysis-churn metrics
   between the baseline and candidate binaries per model, per kata
   acceptance's "require a clear reduction in analysis churn without
   increasing premature or incorrect fixes."
4. **Report doc.** Once runs exist, write the comparison up (reports/
   already holds the convention for this — see
   `tools/tool-fluency/reports/2026-06-20-kimi-for-coding.md` for the
   shape) including the exact commands, session IDs, and any
   `blocked_infra`/`blocked_harness` results (which must not be read as
   product/model findings — see the verification.md smoke-case rule this
   kata just added).
