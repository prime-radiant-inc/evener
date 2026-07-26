# Running a fleet of agents on this repo

A dozen agents working the same repository at once is normal here. Most
of what goes wrong is not in their code — it is two agents sharing
something neither knew was shared. This is the collected list, all of it
learned by losing work to it.

## The controller runs the test suites, not the agents

Agents write tests, verify the specific ones they wrote, and hand back a
**mutation list**: for each test, the exact one-line source change that
must make it fail, and which of those they actually ran. The controller
runs the suites centrally.

Two reasons, both measured. A parallel fleet each running the full suite
drove load to 358 with 68 concurrent vitest processes. And the first
centrally-gated batch after adopting this had four failures the authoring
agents had never run — one a real bug.

A test whose mutation was never executed is a test nobody has shown can
fail. Ask for that column and read it.

## Verify what an agent reports

Agents are usually right and occasionally confidently wrong. The
expensive failures share a shape: the report is coherent, the reasoning
is sound, and one premise is false.

- An agent that says "this was a pre-existing failure" is often
  describing a regression it introduced. Check against the base.
- An agent that finishes by saying its tree is "in the right final
  state" may still be holding debug edits. One had replaced
  `import.meta.env.DEV` with a literal `true` at both dev-route guards,
  which would have shipped the dev gallery into a production bundle.
- An LLM reviewer once spontaneously **confabulated a prompt-injection
  attack** during adversarial testing. No injection had occurred, proven
  from the raw transcript. Describing the suspected pattern to later
  agents then produced two more "confirmations." Check the primary
  source before believing anything surprising, and never re-describe a
  specific suspected attack to downstream agents.

Read the diff, not just the summary.

## Everything shared, and how it bites

**`refs/stash` is shared across every worktree.** `git stash` in one
worktree can be popped into another. Never stash; make a targeted edit
instead.

**`node_modules` is one real install, symlinked from each worktree.**
`npm ci` deletes the symlink's target, emptying it for every worktree at
once. Never run it. And never `git add` a directory containing that
symlink: a committed `node_modules` symlink pointing at its own absolute
path got onto eighteen branches, and git then re-materialised it over the
real install on **every merge**. It looked like sabotage from another
agent twice before the cause was found. `.gitignore` does not help —
ignore rules only govern untracked files, so once staged it went quiet.

**The Go build cache is large and grows fast.** One `go test -c` of the
biggest package adds roughly 1G from warm. With the cache on the boot
volume, a fleet filled the disk to zero and every agent died at once —
including tool calls, which write an output file before executing, so
nothing could run at all. Keep `GOCACHE` on a volume with room:

```
go env -w GOCACHE=/path/on/a/big/volume
```

Disk exhaustion never announces itself. It surfaces as
`link: mapping output file failed`, `t.TempDir()` failures, and jobstore
open errors — four test failures were once root-caused to it after being
investigated as flakes.

**Chrome is one shared instance.** With eight browser agents up, 41 tabs
across 13 ports accumulate and `new_tab` followed by an eval lands on
someone else's tab. Worse, `switch_tab` can land on a *backgrounded* tab
where `requestAnimationFrame` and `ResizeObserver` never fire — which
yields a confident wrong answer rather than an error. Assert
`location.port` inside **every** eval. That converts corruption into
failure, which is the right trade and is not a fix; call `set_profile`
with the worktree name as the first browser action, since it refuses once
Chrome is running.

**Ports and scratch paths must derive from something unique.** Handing
out ports in prose put two agents on the same one, and a hub answering on
the expected port passes every check an agent makes — so the measurement
is silently of the wrong thing. Bind `127.0.0.1:0` and read back the
kernel-assigned port. Derive scratch paths from `mktemp -d`, never from a
wave-wide prefix; two agents deriving from a shared prefix overwrote each
other's binaries.

## Worktrees

Create them from the **local** branch under test. `isolation: "worktree"`
branches from `origin`, which silently discards stacked unpushed work.

Never run two write-capable review agents in the same worktree; they
collide on revert-and-restore experiments. Verify a worktree is at the
approved commit before merging it.

Merge with `git merge --ff-only` and run `git worktree remove` from the
main checkout, never from inside the worktree, and confirm the target
branch actually advanced.

Classifying a worktree as removable needs **two** facts: it has commits
of its own, and they have landed. A freshly-created branch is trivially
an ancestor of what it was cut from, so an ancestor check alone marks
unstarted work as merged — that deleted five worktrees ninety seconds
after five agents were given them. The dirty-check did not save them
either: a checkout nobody has written to yet is perfectly clean. Failure
must always fall toward keeping.

## Model tiering

Use the cheapest model that does the job. Decompose for tier: many small
sessions beat one large one for mechanical work. Runners are mid-tier,
gates can be small, coordinators and reviewers want the strong model.
Never put an inherited frontier model on procedural work.

Note that a restricted tool list may be a prerequisite: an agent
inheriting a large tool surface can exceed a small model's context before
it reads a word of its prompt. Agent definitions live in
`.claude/agents/*.md` and are read at session start, so a new one needs a
restart before it resolves.

## Writing the kata, and being wrong in it

A kata written from an editor diagnostic or a quick read is frequently
wrong in its specifics, and the agent working it will believe you. Three
in one night needed correction: a "dead code" list where two of five
symbols were live, a bug scoped to resume that also fires on fresh
sessions, and a claim about which of four sites shared a cause.

State the problem and the evidence. **Do not state the solution** — and
this matters twice over when a persona panel or a reviewer is involved,
because naming options in the prompt produces unanimous agreement with
whatever you named. A panel asked "hairline or extra space?" returned
five unanimous votes for a hairline; the token in question measures
1.26:1 against its background in dark mode and would have been invisible.
That result was discarded and the question asked again without the
options, which produced a different and better answer.

When an agent corrects a kata's premise, record the correction on the
kata. That is the most valuable thing it produces.
