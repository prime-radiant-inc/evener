# Memory lab

The memory lab measures whether Evener's prompts make agents use memory and the session whiteboard. It checks five behaviors:
- **capture:** saving a lesson
- **recall:** a later session reading the lesson and acting on it
- **correction:** fixing a page that turned out to be wrong
- **scope choice:** personal, project or session memory
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
- `--work-root DIR` creates each trial workspace (`work`, and a stage's own `workspace` such as `work2`) at `DIR/<project>-<random>` instead of inside the results tree, where `<project>` is the last element of the fixture's `go.mod` module (`shop`, `textutil`), or `app`. The trial's `work/` is then a symlink to it, and `grade.json` records the real paths under `workdirs`. Every later use (the session's cwd, checks, `before` hooks, `show`, `ask`) takes the real path. Use it for capture-sensitive runs, for example `--work-root ~/Developer`. An agent whose cwd reads `.../results/memory-lab/runs/H1c/.../r5/work` infers a throwaway eval fixture and skips saving memory, and the real path removes that tell. The trial's state dir (`xdg/`) moves too, to `DIR/.state/<project>-<random>`, because memory tool results show its path. The dirs are left in place so you can inspect them; clean them up by hand. Without the flag, workspaces stay inside the trial dir. Fixture commits use the author `Sam Rivera <sam@shopco.dev>` either way.
- `--out` must be a fresh directory, relative or absolute. The command prints one line per stage as each trial finishes, then only the pass-rate table per version, scenario, stage and check. `report` prints the per-stage lines too.

## Reading results

```bash
./memory-lab report OUTDIR                       # one line per stage, then the pass-rate table
./memory-lab show OUTDIR/<version>/on/<scenario>/r1 A            # tool calls, memory files, whiteboard, final message
./memory-lab show --reasoning OUTDIR/.../r1 A    # also the model's reasoning summary, with tool calls marked
./memory-lab ask --bin bin/evener-try --model MODEL OUTDIR/.../r1 A \
  "Please don't change anything; just answer. You didn't save X to memory. Did you consider it, and what led you not to?"
```

`ask` resumes the stage's root session, by the id recorded in its `grade.json`, and asks it a question. For a stage that resumed an earlier one (for example `delegate-reads` stage B), that is the earlier stage's session. A stage that ran in another workspace needs `--workspace` (for example `--workspace work2` for `sed-quirk` stage B). `--effort` (default `high`) and `--timeout` (default 600 seconds) apply too. Use it whenever a trial does something you didn't want, and ask before you reword a prompt. In past rounds the answers named the actual cause:
- "I converted the constraint into an action, satisfied it, and checked it off."
- A skip rule read as a license to skip.
- A trigger read as a gate that a short task never trips.

Every trial keeps its full logs: `<stage>.events.ndjson`, `<stage>.stdout`, `<stage>.grade.json`, the memory files after each stage in `<stage>.memory/`, and the session state in `sessions/`.

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
| `long-work` | Longer work; the partner may pause and resume it. Working notes go to session memory | The cue is in the prompt. |
| `long-work-nocue` | The same work with no cue | Flash models write no session notes here. |
| `session-local` | A refactor constraint that applies only to this work | Information only: agents defensibly save it to project memory, since it has a named follow-up. |
| `delegate-reads` | The root records how this work is organized, then has a delegate do a sub-part with a one-sentence brief | B resumes A's session. B's check needs a delegate started during stage B to call `memory_read` or `memory_search` with scope `session`. It parses the delegates' transcripts as JSON, since a regex such as `memory_(read\|search)` also matches the tool list in every transcript. With the current prompt, A tends to put the work rule in project memory, so the delegates have no session note to read and the check grades `n`: it is a target for the session-memory prompting, not yet passing. |
| `recall-seeded` | Seeded project memory (a test-suite quirk). B reads it and acts | `on` and `off` arms. Recall from seeded memory already worked before the guidance work. |
| `correction-seeded` | A seeded page goes stale (an env var is renamed). B fixes the page | |
| `quirk` | A finds that `go test` silently skips without an env var | The agent usually fixes the root cause in the repository, which makes not saving the correct outcome. Kept as a caution. |
| `eval-port` | Stage A of the live memory eval fixture, run under the lab | A diagnostic for round caps. The task takes 7–8 rounds without any memory work. |
| `whiteboard` | A short task. The whiteboard should be set in the three-part shape, under 600 characters, with no file paths | Reads `agent_note` from the session meta. |

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
