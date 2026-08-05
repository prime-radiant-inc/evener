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

- A fix assignment does not authorize integration. Naming a base commit, target
  branch, or landing branch is context, not permission. Agents should not
  merge, push, rebase, delete branches, cherry-pick, or perform other
  integration actions unless explicitly requested in a separate instruction.
  Dispatch prompts should list those out-of-scope integration actions up front.

- Report authority correctly. Use "as requested" or "as instructed" only when a
  controller request exists and can be identified. Otherwise report what happened
  without false attribution, for example, "I fixed X and am ready for controller
  integration review."

A conventional-looking integration action plus false authority claims can evade
controller review: it looks like delegated, normal procedure, so the review step
may skip the authorization check even when code is otherwise correct.

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

The sharing also means **every vitest run, in any worktree, rewrites the
one real `node_modules/.vite` cache.** Anything that fingerprints or
diffs `node_modules` while a fleet is running will see those bytes move
and blame whatever it was guarding — `TestInstallHomeGeneratedHome`'s
install-mutation digest fired exactly this way during a 12-agent run and
cost a root-cause investigation before the digest learned to skip
`.vite`/`.vite-temp`. A guard over shared state must exclude the parts a
legitimate concurrent actor owns, or it is a coin-flip under fleet load.

**The Go build cache is large and grows fast.** One `go test -c` of the
biggest package adds roughly 1G from warm. With the cache on the boot
volume, a fleet filled the disk to zero **twice** — every agent died at
once, including tool calls, which write an output file before executing,
so nothing could run at all. On a machine that HAS a second volume with
room, move `GOCACHE` there, once per machine:

```
scripts/setup-gocache.sh /path/on/a/big/volume
```

There is no default target: a big volume is a per-machine fact, and the
script once defaulted to an external volume that was later retired, after
which every no-argument run chased the dead path. On a machine with only
its boot volume, leave `GOCACHE` at Go's default and watch the disk floor
instead. `go env -w` writes to a per-user file outside this git checkout,
so a fresh clone or a new machine does **not** inherit it — this is a
step to run, not a setting to commit.

The external volume itself can be unmounted. That must fail loudly, not
mysteriously: `scripts/disk-reclaim.sh --check` probes `GOCACHE` and
gives a specific "this is what an unmounted build-cache volume looks
like" diagnosis instead of a bare `go build` error naming neither
`GOCACHE` nor "unmounted".

It can also **stall while still mounted**, which is worse than being
gone: nothing fails, everything blocks. `mkdir` blocks, and so does every
go command that touches the cache — four go processes were once found
sleeping 12-23 hours that way, on a volume that answered `ls` instantly
by the time anyone looked, while a session watched five background
`go test` jobs die at their run timeout with zero output and no
explanation (kata r07s). So the probe is bounded: 10s by default,
`SERF_GOCACHE_PROBE_TIMEOUT` to change it, and the diagnosis names the
stall. A sleepy volume costs one failed check, not a fleet-wide mystery.
Do not route around it with `GOCACHE=/tmp/...`; that fills the volume the
external cache exists to spare (kata 98x9).

Disk exhaustion never announces itself otherwise. It surfaces as
`link: mapping output file failed`, `t.TempDir()` failures, and jobstore
open errors — four test failures were once root-caused to it after being
investigated as flakes.

**`make build` builds the runtime pair only — serf and serf-hub, not
serf-tui.** serf-tui has its own target. A live TUI check after `make
build` therefore runs a stale binary, which reproduces whatever the old
code did while every test of the new code passes. This produced a
"right side of the chip strip missing at 200 columns" mystery once, and
a full re-investigation of the already-landed fix a second time (kata
wqyx, both occurrences). Before any live TUI verification:
`go build -o serf-tui ./cmd/serf-tui/` and check the binary's mtime.

**Chrome is one shared instance.** With eight browser agents up, 41 tabs
across 13 ports accumulate and `new_tab` followed by an eval lands on
someone else's tab. Worse, `switch_tab` can land on a *backgrounded* tab
where `requestAnimationFrame` and `ResizeObserver` never fire — which
yields a confident wrong answer rather than an error. Assert
`location.port` inside **every** eval. That converts corruption into
failure, which is the right trade and is not a fix.

**`set_profile` does NOT give you a private browser — do not call it.**
It was previously documented here as the fix ("call `set_profile` with
your worktree name as the first browser action"). That guidance was
wrong and is actively harmful: `set_profile` is a single sticky value on
the shared MCP *server process*, not per-agent. Measured live (kata
8ecz): a controller that never called `set_profile` at all read back
profile `k7-turnvoice-verify` — one agent's worktree name, set hours
earlier — while 38 tabs from unrelated agents piled up inside that one
"isolated" profile and the true default profile held 2. Every agent that
follows the old advice silently redirects every *other* agent onto its
own profile in turn. It is worse than no isolation, because it looks like
isolation: each agent believes it is alone in a private browser while
sharing one with a dozen others. Real per-agent isolation needs a
per-call profile argument or one MCP server per agent — neither exists
today.

Until it does, the honest options are: serialise browser verification to
one agent at a time, or skip the browser and build a static-file harness
for the specific thing you need to check (one agent did this
successfully after losing four measurements to tab theft). The
`location.port` assertion above remains correct and is the only thing
that has been catching wrong-hub measurements.

**A click that reports success proves the dispatch, not the effect.**
Coordinate-based MCP clicks can be silently swallowed: the tool reports
"Clicked," no error, but the handler never runs and nothing crosses the
wire. Measured live (kata xkp2): 2 of 4 coordinate clicks on the same
enabled button were swallowed this way in one session, while
`element.click()` and the other 2 coordinate clicks on the identical
button worked normally. The kata that measured this was itself the
artifact — an agent's first-hand "clicked Spawn, nothing happened" was
filed as a product bug and cost a full investigation cycle (a correct,
thorough trace ruling out every code path that could produce silence)
before a live retest, with the wire captured, exposed the tool instead
of the product. Assert the click's *effect* — a captured wire call, a
DOM/state change, a toast — never the tool's report of the click.
Treat effect-absence as "retry the click," not as evidence about the
product.

**State whether you actually verified in a browser.** An agent that
cannot reliably hold a tab still reports its change as done, and
browser-verified work looks identical to unverified work in the ledger
unless the agent says so. End every completion report that touches a
user-facing surface with an explicit `Browser-verified: yes` or
`Browser-verified: no (reason)` line.

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

Classifying a worktree as removable needs **two** facts about its
branch: it has work of its own, and that work has landed. Only the
second is a question about ancestry. A freshly-created branch is
trivially an ancestor of what it was cut from, so an ancestor check
alone marks unstarted work as merged — that deleted six worktrees ninety
seconds after six agents were given them, and the dirty-check did not
save them, because a checkout nobody has written to yet is perfectly
clean.

The follow-up fix read the first fact as "the branch tip is still the
base's tip". That holds only at cut time: two merges landed on the base
between `worktree add` and the next run, five more fresh worktrees
stopped matching, and they went too. `scripts/disk-reclaim.sh` now takes
the first fact from the **branch's own reflog** — the commit it was
created at, and whether anyone has committed on it since — so a moving
base cannot change the answer. Nothing has to be done at creation time
for this to hold, so a worktree made by any means is covered; a branch
whose reflog is missing is kept.

**Ignored is not disposable.** git calls a checkout whose only content
is git-ignored clean, so `git worktree remove` without `--force` deleted
one holding an agent's `.superpowers/` SDD archive. The script keeps any
candidate holding ignored content it cannot name as regenerable
(`node_modules`, `.build/`, the built binaries, `.DS_Store`), and never
considers the main checkout at all.

Failure must always fall toward keeping. A worktree kept that could have
gone costs disk; the other direction costs a running agent its
workspace. `scripts/disk-reclaim-selftest.sh` carries a scenario per
hole — run it after touching the classifier.

**Expect `--worktrees` to free nothing, and do not treat that as a bug
in the classifier.** Every kata agent writes
`.superpowers/<kata>-report.md` into its worktree, and that file is
git-ignored work the script keeps by design. So a merged worktree is
normally *also* a kept one: measured at the disk floor on 2026-07-30, all
8 merged worktrees were held this way and the removable share was 0
bytes. The pressure this creates is the danger — the remedy on offer does
nothing, and the obvious next move is to loosen the classifier, which is
how the deletion incident already happened twice.

That zero is not a gap needing a new mechanism — kata worktrees have a
disposal path, and it is the CONTROLLER'S RETIREMENT PASS, not
`--worktrees`. After central gates go green the controller reads the
report, verifies the branch is clean and landed (both facts), and removes
worktree and branch by hand; the report goes with it, having been read.
Sixteen worktrees retired that way in one night with zero friction. The
report living inside the worktree is what makes the ratchet CORRECT for
everything else: a worktree nobody retired is abandoned work, and its
report is the only record of what that work was. Do not move reports out
of worktrees to "fix" the yield, and do not treat `--worktrees` as the
kata-cleanup tool — it is a background net for the other classes.

So the levers have to be honest about what they can do. `--check` is a
bare `df` on every test invocation and cannot measure, so it names the
tools that can rather than promising a yield. The report sizes
**removable, kept and unregistered separately**, because one total for
the worktrees directory reads as the amount `--worktrees` would free —
8.3G, when the answer was zero.

**Most of the reclaimable disk is not in registered worktrees.** Two
pockets are larger and neither is removed by any script here, because for
both of them git's own "is this safe" check is unavailable:

- `scripts/report-tmp-debris.sh` — per-session scratch under `/tmp`,
  measured at 10.0G across 120 entries on the same volume as the
  checkout. Scratch checkouts at ~270M each, stray per-session build
  caches, chrome profiles, dumps. A scratch checkout can hold a
  never-pushed experiment, so review each one; the bulk sweep needs
  authorization.
- `scripts/report-orphaned-worktrees.sh` — ~1.6G of pre-rename checkouts
  `git worktree list` has no record of (kata smw0).

**Do not `du` the build cache from the report.** It lives on an external
volume by design, where emptying it cannot move this checkout's floor,
and sizing it costs 32.6s of every run to answer a question nobody asked.
`df` of its volume is the fact that matters. Same rule as the levers:
measure what you can act on.

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

**Verify any mechanism you name.** A kata that says "add an entry to X"
sends the agent at X. Two katas in one night told fixers to add a
`SEMANTIC_USE_ALLOWLIST` entry so a stylesheet could use an attention
hue; that allowlist only matches paths shaped
`widgets/<name>/<name>.module.css`, so both entries would have been inert
and both tests would still have failed. The right mechanism was
`SEMANTIC_PATH_EXCEPTIONS`. Both agents traced the regex and worked
around the instruction — but only because they were told to push back.
Naming a wrong mechanism is worse than naming none, because a named one
stops the agent looking.

## Auditing a decision record

Reconstructing why a UI is the way it is, from mockups and commit
subjects, has its own failure modes. Two produced wrong entries in
`docs/web-ui/decisions.md` before adversarial review caught them:

**Check the shared block before crediting an alternative.** Mockups often
declare rules *held constant across all four* options — a "Held constant"
paragraph, or a rule stated once in the shared CSS above the A/B/C/D
overrides. Three such rules were filed as alternative D's contribution.
That reads exactly backwards: a held-constant rule is the *strongest*
kind of decision in the set, because every alternative agreed on it.

**"Not addressed at all" is a claim about history, and history is
checkable.** One entry said a feature was never built, two sentences
after citing the commit whose subject says it shipped. It had shipped,
into the pre-rewrite hub, and was lost when that hub was deleted —
a materially different story, and stronger evidence for the audit's own
thesis than what was written. `git log --oneline --all -- <path>` and
`git log -1 --format=%B <sha>` settle these in seconds.

Run the audit past two independent adversarial reviewers before trusting
it. Twelve findings came back on a document whose research was otherwise
sound; the two most valuable were places it contradicted its own cited
evidence.
