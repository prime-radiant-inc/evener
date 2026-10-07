# Memory lab

The memory lab measures whether Evener's prompts make agents use memory and the session whiteboard. It checks five behaviors:
- **capture:** saving a lesson
- **recall:** a later session reading the lesson and acting on it
- **correction:** fixing a page that turned out to be wrong
- **scope choice:** personal or project memory
- **whiteboard upkeep**

Use it before and after you change memory guidance, memory tool descriptions, the Finishing table, or the whiteboard prompting.

The one-session prompt-eval runner (`tools/tool-fluency/cmd/evener-fluency`) gives every run a fresh directory. That doesn't work for memory, because project memory is keyed by checkout path and capture-then-recall needs several sessions sharing one memory root. The lab instead runs each scenario as a sequence of `evener` sessions ("stages") in one checkout. Each trial gets its own `XDG_STATE_HOME`, so trials never share memory. The lab grades every stage from:
- the event stream
- the workspace
- the memory files
- the session whiteboard

## Setup

The script is Python 3.11 or later, using the standard library only. From the repository root:

```bash
cd tools/prompt-eval/memory-lab
mkdir -p bin
go build -o bin/projid ./projid          # computes a checkout's project memory id, for seeded scenarios
go build -o bin/evener-base ../../../cmd/evener    # build each version you want to compare
```

To compare prompt versions, build one `evener` binary per commit, for example `bin/evener-base` from `main` and `bin/evener-try` from your branch. `bin/` is git-ignored.

The lab runs real models with your configured provider credentials, so it is never part of `make test`. Write results under `tools/prompt-eval/results/`, which is git-ignored.

## Running

```bash
./memory-lab run \
  --version base=bin/evener-base --version try=bin/evener-try \
  --model lunarouter/deepseek-4.1-flash-background \
  --scenarios scenarios/feedback,scenarios/freeze,scenarios/long-work \
  --reps 6 --jobs 10 --out $PWD/../results/memory-lab/my-run
```

- `--reps`: 3 repetitions are enough to spot a broken scenario. Plan on 6–10 to compare two prompts. With these weak models, a single prompt has swung from 0/3 to 3/3 between runs with nothing changed.
- `--jobs`: lunarouter allows 10 concurrent requests on a model's regular pool and 30 on its `-background` pool. Keep the total across parallel runs under that.
- `--max-rounds` (default 40) caps tool rounds per stage; 0 means no cap. If the cap is tight, every stage stops on the cap and the results measure the cap, not memory.
- `--effort` (default `high`) sets the reasoning effort. `--timeout` (default 1200) is seconds per stage.
- An infrastructure failure prints `ERROR` on the stage it hit and on every later stage. That covers failed workspace setup, a failing or timed-out `before` hook, and a session that times out or exits nonzero, except evener's exit 1 on the round cap, which grades as a `CAPPED` stage. Those stages fail every check in the table, the failed session's events and memory snapshot stay on disk, and `run` exits nonzero once all trials finish. A check that times out or errors fails on its own, with the reason in `show`.
- `--work-root DIR` creates each trial workspace (`work`, and a stage's own `workspace` such as `work2`) at `DIR/<project>-<random>` instead of inside the results tree, where `<project>` is the last element of the fixture's `go.mod` module (`shop`, `textutil`), or `app`. The trial's `work/` is then a symlink to it, and every later use (the session's cwd, checks, `before` hooks, `ask`) resolves the symlink to the real path. Use it for capture-sensitive runs, for example `--work-root ~/Developer`. An agent whose cwd reads `.../results/memory-lab/runs/H1c/.../r5/work` infers a throwaway eval fixture and skips saving memory, and the real path removes that tell. The trial's state dir (`xdg/`) moves too, to `DIR/.state/<project>-<random>`, because memory tool results show its path. The dirs are left in place so you can inspect them; clean them up by hand. Without the flag, workspaces stay inside the trial dir.
- `--out` must be a fresh directory, relative or absolute. The command prints one line per stage as each trial finishes, then only the pass-rate table per version, scenario, stage and check. `report` prints the per-stage lines too.

## Reading results

```bash
./memory-lab report OUTDIR                       # one line per stage, then the pass-rate table
./memory-lab show OUTDIR/<version>/on/<scenario>/r1 A            # tool calls, memory files, whiteboard, final message
./memory-lab show --reasoning OUTDIR/.../r1 A    # also the model's reasoning summary, with tool calls marked
./memory-lab ask --bin bin/evener-try --model MODEL OUTDIR/.../r1 A \
  "Please don't change anything; just answer. You didn't save X to memory. Did you consider it, and what led you not to?"
```

`ask` resumes the stage's root session, by the id recorded in its `grade.json`, and asks it a question. For a stage that resumed an earlier one, that is the earlier stage's session. A stage that ran in another workspace needs `--workspace` (for example `--workspace work2` for `sed-quirk` stage B). `--effort` (default `high`) and `--timeout` (default 600 seconds) apply too. Use it whenever a trial does something you didn't want, and ask before you reword a prompt. In past rounds the answers named the actual cause:
- "I converted the constraint into an action, satisfied it, and checked it off."
- A skip rule read as a license to skip.
- A trigger read as a gate that a short task never trips.

Every trial keeps its full logs: `<stage>.events.ndjson`, `<stage>.stdout`, `<stage>.grade.json`, the memory files after each stage in `<stage>.memory/`, and the session state in `sessions/`.

`bookkeeping RUNDIR [STAGE]` counts each trial's root-session bookkeeping by surface: ledger writes (a file write, an `apply_patch` whose headers name `progress.md`, or a shell redirect, `tee` or in-place edit aimed at it), memory writes by scope, whiteboard updates, task-list status updates and task-list notes (one call can count as both), plus read-backs. It prints one line per trial and a mean per version and scenario. Use it to measure duplicated progress tracking, for example on `sdd-plan`.

Shell commands are classified by a heuristic, not a shell parser. It splits a command into statements (lines, `;`, `&&`, `||`, `|`), tokenizes each with `shlex`, and skips leading `VAR=value` settings and the wrappers `sudo`, `env`, `timeout`, `nice`, `nohup`, `command`, `exec` and `time`. The covered forms are:
Options for `grep`, `rg`, `sed` and `perl` are parsed getopt-style: short clusters (`-Ef FILE`, `-neTask`, `-pe EXPR`), attached and separate values, long `--name=value`, and `--` ending the options.
- writes: redirects (`>`, `>>`, `2>`, `&>`, `>&`), `tee`, and the files `sed -i`/`--in-place` or `perl -i` edits. Their `-e` expressions are skipped, and a `sed -f` script or a perl script file (the first operand when there's no `-e`) counts as read.
- reads: `<` input redirects (not heredocs or `<<<` here-strings), and the file arguments of `cat`, `head`, `tail`, `less`, `more`, `awk`, `wc`, `diff`, quiet `sed` (`-n` alone or in a cluster, `--quiet`, `--silent`), `grep` and `rg`. For `grep` and `rg` the pattern operand is not a file, including a pattern given with `-e`/`--regexp` (separate or attached); a `-f`/`--file` pattern file is read.

Any other form counts as ordinary work. In the lab runs so far, shell commands account for about one in nine progress-file writes; the rest go through file tools. `test_bookkeeping.py` holds the classifier's behavior cases (`python3 -B test_bookkeeping.py`).

## Scenarios

Every scenario is a directory holding `scenario.json` and `fixture/` (a small Go module). Several reuse another scenario's fixture instead: `"fixture_from": "feedback"` in `scenario.json` starts the trial from `scenarios/feedback/fixture`.

| Scenario | Measures | Notes |
|---|---|---|
| `feedback` | A: partner repeats a preference ("run go vet, I don't want to say this again"). B: a fresh session follows it | The persistence cue is in the partner's words. |
| `report-format` | Partner asks "from now on, end with Commands run:". B follows it | A saves it even without guidance. B shows whether recall works. |
| `migration` | Partner mentions in passing that the project is moving off `oldlog`. B adds no new `oldlog` call | A project fact with no persistence cue. |
| `freeze` | Partner says the exported API is frozen until 2.0. B is asked to break it | A held-out project-fact scenario. B should push back. |
| `cents` | Partner states a decision (money is integer cents). B formats prices | Checks for real `float32`/`float64` use, not comments. B tends to pass without memory too. |
| `sed-quirk` | A hits macOS BSD `sed -i` while bumping a version. B, in a different project, writes an in-place script | Personal scope across projects. Needs BSD `sed` (macOS): on GNU `sed` the stage A `before` hook fails the trial as infrastructure, since there is no quirk to hit. |
| `long-work` | Longer work; the partner may pause and resume it | The cue is in the prompt. Progress belongs in the task list; read it with `bookkeeping`. |
| `long-work-nocue` | The same work with no cue | Read it with `bookkeeping`, as for `long-work`. |
| `session-local` | A refactor constraint that applies only to this work | Information only: it records whether the constraint lands in project memory. |
| `recall-seeded` | Seeded project memory (a test-suite quirk). B reads it and acts | `on` and `off` arms. Recall from seeded memory already worked before the guidance work. |
| `correction-seeded` | A seeded page goes stale (an env var is renamed). B fixes the page | |
| `quirk` | A finds that `go test` silently skips without an env var | The agent usually fixes the root cause in the repository, which makes not saving the correct outcome. Kept as a caution. |
| `eval-port` | Stage A of the live memory eval fixture, run under the lab | A diagnostic for round caps. The task takes 7–8 rounds without any memory work. |
| `whiteboard` | A short task. The whiteboard should be set in the three-part shape, under 600 characters, with no file paths | Reads `agent_note` from the session meta. |
| `long-project` | Four sessions of one cleanup, each asked for commit SHAs and progress. A states a doc-comment team tag, B an error-wrapping convention, C changes the team tag; D adds a function and must apply B and C | The page-hygiene scenario that discriminates most: without the hygiene guidance, project memory grows a progress ledger with SHAs. Durable-memory noise checks run after every stage. |
| `many-facts` | A is told four facts in passing (API freeze, commit prefix, int cents, run `go vet`). B must apply them | Capture is at the ceiling at neutral paths; run it with `--work-root`. |
| `progress-notes` | A long job with a keep-notes cue and a durable decision. Progress goes in the task list, the decision in project memory, and no SHAs, paths, test counts or status reach durable memory | |
| `progress-log` | Three commits plus a team-tag rule told in passing. B applies the rule; durable memory stays free of run details | At the ceiling: a short task doesn't provoke logging. |
| `fact-changes` | A saves a dollar receipt format, B switches to euros, C must follow euros; no page still states dollars as current | C reads the format from the code too, so it is at the ceiling. |
| `stale-status` | A leaves a rename half done; the partner finishes it before B. B must not report or keep the rename as unfinished | |
| `polluted-seed` | Seeded project memory: a long, dated progress log with SHAs and worker ids, and a status-only index line, with one durable decision (use `log/slog`) buried inside. B adds a log line | Checks that B uses slog and leaves the decision on a short page with no SHAs (a fresh page, or the log rewritten in place). |
| `clean-seed` | The same decision seeded as a clean one-fact page | The control for `polluted-seed`. |
| `sdd-plan` | A four-task superpowers plan run with the real subagent-driven-development skill (copied into the fixture's `.agents/skills`). Measures bookkeeping: run `bookkeeping` on the results | The skill keeps its own ledger, so progress belongs there with task-list statuses only. Its checks look at every branch, because the skill works on a worktree branch and leaves the merge to the partner: a held-out test of all four helpers (coupon bounds, empty cart, unknown SKU) must pass on some branch. |
| `plan-noskill` | The same plan with no skill and a may-stop-you cue. Progress should go in the task list | The same held-out behavior check as `sdd-plan`. |

## Scenario format

The header of `memory-lab` documents every field. In short:
- each stage has a prompt
- a scenario can set `fixture_from` to start from a sibling scenario's `fixture/`
- stages can carry `before`, `seed_project_memory`, `fixture` with `workspace` (another project; `run` refuses a `fixture` without a `workspace` other than `work`) and `resume` (continue an earlier stage's session)
- checks come in these types:
  - `checks`: shell commands
  - `trace`: tool-call regexes
  - `memory`: regexes over memory files, optionally per scope or `absent`
  - `final`: a regex over the last message
  - `transcripts`: a regex over every transcript, delegates included
  - `delegate_calls`: a tool call with a given name and arguments, parsed from the transcripts of delegates created during this stage
  - `whiteboard`: shape and length

Run `./memory-lab check` after editing a scenario. It loads every scenario under `scenarios/` (or the dirs you name) the way `run` does, confirms each loads as itself and that its fixture and seed dirs exist, prints one OK or ERROR line per scenario, and exits nonzero on any error. It runs no model.

Write a check that a reasonable outcome can actually fail. Before you trust a scenario, confirm that the baseline prompt doesn't already pass it, and that its regexes don't match comments or prose. The `cents` float check originally failed on comments that said "no floats".

For work the prompt asks for, add a held-out test: a `checks` command that writes a temporary `zz_heldout_test.go`, runs `go test -run Heldout`, deletes the file and exits with the test's status. "tests pass" alone passes on whatever tests the agent wrote, including none.
