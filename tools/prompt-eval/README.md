# Prompt evaluation

These tasks measure how a system prompt shapes the way agents write and behave. Each task is a manifest with a fixture, a prompt written the way a colleague would ask, and checks that judge the outcome of the work. No check reads the system prompt; the prose tools count what the agents wrote, and the blind read judges it.

Memory and whiteboard behavior need several sessions sharing one checkout and memory root, which this runner doesn't do; measure those with the [memory lab](memory-lab/README.md).

The tasks run on the tool-fluency runner, `tools/tool-fluency/cmd/evener-fluency`. This guide walks through one experiment end to end: write or pick tasks, declare versions, run the matrix, count the prose, pack it for a blind read, build ranking sets, hand them to reviewers, score the result, and read what it tells you.

Build the runner once:

```bash
go build -o /tmp/lab/evener-fluency ./tools/tool-fluency/cmd/evener-fluency
```

Every command below assumes `/tmp/lab/evener-fluency` is that binary and that you run from the repository root.

## The tasks

| Task | What it exercises |
| --- | --- |
| `prose.smoke` | The model answers at all. Run it first for every model. |
| `prose.bugfix-tally` | A bug fix the tests already catch, reported back in plain words. |
| `prose.investigate-cache` | An investigation that ends in a written explanation, with no changes. |
| `prose.delegate-textutil` | Three independent pieces of work that can be split across delegates. |
| `prose.background-suite` | A slow test run, waited on without polling. |
| `prose.git-greeting` | A branch, a commit, and a merge that leave unrelated files alone. |
| `prose.research-proposals` | Reading two proposals and recommending one in a written note. |
| `prose.ambiguous-export` | A vague request with no one to ask. |
| `prose.handback-wordfreq` | A deliverable that must be left in place and working. |
| `prose.research-conflicting` | Notes that disagree; the answer must match the one that actually applies to this codebase. |
| `prose.edit-audience` | Rewriting an internal incident writeup for a non-technical customer audience. |
| `prose.ops-logs` | A cleanup request with a retention rule that is easy to miss. |
| `prose.changelog-skill` | A changelog entry, where a project skill sets the format. |
| `prose.release-skill` | Cutting a release, where a project skill sets the steps. |
| `prose.staging-config` | A new environment whose values only the person knows (`person:`); copying production fails. |
| `prose.customer-rename` | A rename whose scope only the person knows (`person:`); invoices must keep the old name. |
| `prose.git-greeting-asked`, `prose.bugfix-tally-asked`, `prose.research-proposals-asked` | Controls with a `person:` who has nothing to add: the agent should not need to ask. |

### Adding a task

Write the manifest in `tasks/`. A minimal one is just an id and a prompt:

```yaml
schema: 1
id: prose.smoke
tool: prompt-eval
prompt: |
  Reply with exactly SMOKE_OK and finish.
expect:
  final_contains: ["SMOKE_OK"]
```

A task that fixes a real bug adds a fixture and outcome checks, and can carry a `reference` shell script that solves it, so the offline test can prove the checks fail before the fix and pass after it:

```yaml
schema: 1
id: prose.bugfix-tally
tool: prompt-eval
prompt: |
  The totals in our weekly report are wrong. ...
fixture:
  git: true
  files:
    tally/sum.go: |
      ...
expect:
  checks:
    - name: tests pass
      run: go test ./...
reference: |
  cat > tally/sum.go <<'EOF'
  ...
  EOF
```

A task that should be answerable by asking someone adds a `person:` block. The CLI harness then runs `evener run --ask-responder`, pointed at `evener-fluency respond`. It plays the person from the brief, answering each question the agent asks with `ask_user`:

```yaml
person:
  brief: |
    You are Alex, head of ops. Northwind is being renamed to Acme Robotics Ltd.
    Only new records carry the new name; issued invoices keep the old one.
  model: lunarouter/glm-5.3   # optional; defaults to --fast-cheap-model, then --model
```

A bare `model` or `--fast-cheap-model` runs on the main model's provider. The questions and answers land in each probe's result as `asks`, with the call count as `ask_user_calls`. The live harness refuses a task with a `person:` block.

After adding or changing a task, run:

```bash
go test ./tools/tool-fluency/cmd/evener-fluency/ -run TestPromptEvalTasks
```

The test decodes every manifest strictly. When a task has a `reference` solution, it proves the checks fail before the solution and pass after it. When a task asks for no change, it proves the checks pass untouched. It also requires every Go file in a fixture to be gofmt-formatted, so an agent that formats the tree changes nothing it was not asked to.

## Declaring versions

A prompt version is an `evener` binary built from a particular commit. Give each version a label with a digit in it, such as `v0` for the baseline and `v1-A` for a draft: `review-pack` masks the label wherever it appears in what the agents wrote, and a label with no digit (`baseline`, `draft`) would mask an ordinary word too.

There are two ways to declare versions for `matrix`.

**Hand-built binaries**, with `--version LABEL=BIN`:

```bash
go build -o /tmp/lab/evener-v0 ./cmd/evener
git checkout my-prompt-draft
go build -o /tmp/lab/evener-v1-A ./cmd/evener
git checkout -
```

**A version manifest**, so the run builds the binaries itself and records exactly which commits it measured:

```yaml
schema: 1
versions:
  v0: main
  v1-A: my-prompt-draft
```

```bash
/tmp/lab/evener-fluency matrix \
  --version-manifest versions.yaml --version-cache /tmp/lab/version-cache \
  ...
```

Each ref must resolve to a commit, and the repository `matrix` builds from (`.` by default; `--repo` names another) must have no uncommitted changes: a run records exactly which commits it measured, and a dirty tree makes that ambiguous. Binaries are cached under `--version-cache`, keyed by commit, so the same commit never builds twice across labels or across later runs. `--version` and `--version-manifest` can be combined; a label may not appear in both.

## Running the matrix

Run every task on several models, three times each, at low concurrency:

```bash
/tmp/lab/evener-fluency matrix \
  --version v0=/tmp/lab/evener-v0 --version v1-A=/tmp/lab/evener-v1-A \
  --models lunarouter/deepseek-4.1-flash-background,lunarouter/glm-5.3-vision-background \
  --probes-dir tools/prompt-eval/tasks \
  --repetitions 3 --timeout 25m --max-concurrent 4 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash-background \
  --out $PWD/tools/prompt-eval/results/example
```

lunarouter rate-limits concurrent requests: 10 on the regular pool, shared with interactive sessions, or 30 on a model's `-background` pool. Keep `--max-concurrent` low regardless of which pool you use; a model on the `-background` pool that stalls usually means the pool itself is stuck, and the fix is to run the same model without the `-background` suffix.

A run refuses an `--out` that already holds results, so give each run its own directory, or add new version labels into an existing one. Results land under `tools/prompt-eval/results/`, which git ignores. Each fixture runs as its own project: the runner turns Go workspaces off and stops git from looking above the fixture, for the task's own checks and for the agent's commands. Use absolute paths for `--out`, since each result records where its sessions live.

## Counting the prose

Tic counts are a proxy. They can tell you a version dropped its em dashes or its bare identifiers; they cannot tell you the writing got better; a version can improve every count here and still write badly, and one that is worse on every count can still be the one you want (the blind read below is what settles that).

```bash
/tmp/lab/evener-fluency prose-stats --results v0=$PWD/tools/prompt-eval/results/example/v0 --results v1-A=$PWD/tools/prompt-eval/results/example/v1-A
```

This prints one row per label and model: how many runs passed, how many were blocked by infrastructure (which say nothing about the prompt), and counts of em dashes, contrastive negation, bold labels, headers, arrows, shouted words, and opaque identifiers per thousand words. `prose-count FILE...` runs the same counters over any file, such as a draft prompt section, before it ever reaches a model.

## The blind read

This is the judgment that matters: an editor reading each version's writing, side by side, blind to which version is which.

**Pack the transcripts.** Keep the key file somewhere the reviewers never see it.

```bash
/tmp/lab/evener-fluency review-pack \
  --results v0=$PWD/tools/prompt-eval/results/example/v0 --results v1-A=$PWD/tools/prompt-eval/results/example/v1-A \
  --mask-root $PWD/tools/prompt-eval/results/example \
  --packets /tmp/lab/review/packets --key /tmp/lab/review-key.json
```

**Build ranking sets.** For each (model, task), this groups one packet per version into a set, in random order, and writes a plain text file: the reviewer prompt, the answer format, then every set. `prose.smoke` is skipped by default, since there is no writing in it to rank.

```bash
/tmp/lab/evener-fluency rank-sets \
  --review-pack-key /tmp/lab/review-key.json --packets /tmp/lab/review/packets \
  --out /tmp/lab/rank-sets.txt --key /tmp/lab/rank-key.json
```

`--out` is what you hand a reviewer. `--key` is rank-sets' own key, mapping each set's letters back to real labels; keep it out of the reviewer's sight the same way you kept `review-pack`'s key out. `--prompt` points at the versioned judgment instructions: what counts as bad writing, kept separate from the mechanical answer format so it can evolve on its own.

Two reviewer prompts ship here. `rank-reviewer-prompt-v1.md` (the default) judges writing only: outcome first, plain words, no padding, no repetition. `rank-reviewer-prompt-judgment-v1.md` judges the whole packet the way you would judge a new senior colleague: judgment first (did it get the substance right, notice the thing that made the obvious answer wrong, push back when it should have), then writing. Reach for judgment-v1 in a round that changes what the agent does, not just how it writes; reach for v1 when behavior is already settled and the round is a pure prose change. Pass `--prompt` to choose:

```bash
/tmp/lab/evener-fluency rank-sets \
  --review-pack-key /tmp/lab/review-key.json --packets /tmp/lab/review/packets \
  --out /tmp/lab/rank-sets.txt --key /tmp/lab/rank-key.json \
  --prompt tools/prompt-eval/rank-reviewer-prompt-judgment-v1.md
```

A set in `/tmp/lab/rank-sets.txt` looks like this:

```
## Set 3 (model=lunarouter/glm-5.3-vision, task=prose.bugfix-tally)

### Packet A

# Task prose.bugfix-tally
...

### Packet B
...
```

**Hand the sets to a reviewer** (a person, or a subagent primed with the judgment prompt already in the file). The reviewer reads every packet in a set, then writes back one JSON object per line:

```json
{"set": 3, "ranking": ["B", "A"], "writing": {"A": 3, "B": 5}, "why": "A buries the fix in a wall of activity; B says what changed in one line."}
```

`ranking` lists every packet's letter, best writing first. `writing` scores each packet 1-5. `why` names the most important thing about the set and quotes the worst packet.

**Score it.** `rank-score` unblinds every line against rank-sets' key and reports per (label, model): mean score, times ranked first, times ranked last, and the sample size behind them.

```bash
/tmp/lab/evener-fluency rank-score --key /tmp/lab/rank-key.json --reviews /tmp/lab/reviewer-1.jsonl --detail
```

```
LABEL  MODEL                          MEAN  FIRST  LAST  N
v0     lunarouter/glm-5.3-vision      2.80  1      6     8
v1-A   lunarouter/glm-5.3-vision      4.10  6      1     8

Set 3 (model=lunarouter/glm-5.3-vision, task=prose.bugfix-tally): v1-A > v0
  why: A buries the fix in a wall of activity; B says what changed in one line.
```

`--reviews` is repeatable, so more than one reviewer's output can be scored together. `--detail` lists every set's unblinded ranking and its "why", which is where you go to see what actually drove a number. Eight sets per cell shows you a direction, not a significant result; treat a close mean as "run more sets," not as a tie-break.

## Reading the result

Three things tell you different parts of the story, and none of them alone is the answer:

- **prose-stats** is fast and free of judgment calls, and it is the first thing to check after a run: did the version you expected to write differently actually change on the counts it was meant to change? A version that shows no movement here probably will not show movement in the blind read either.
- **The blind read** is the actual judgment: whether the writing is good, in the way an editor means it. Read the `why` lines behind a surprising mean before trusting it; a single confusing packet can move a mean of 8.
- **Task pass rate** (in `prose-stats`' `TASKS ALL PASSED` column) tells you whether a prompt change broke behavior. A version that writes beautifully but stops passing tasks is not a version you want.

None of this is a significance test. Eight sets per cell, one blind read, is enough to see a direction worth acting on or a difference too small to bother with; it is not enough to defend a specific number against another close one.
