# Issue Triage

How open GitHub issues are categorized, labeled, and ranked for Evener.
This is the process the triager follows, not an aspirational standard —
read it as the operating procedure that produced the current label set and
priority assignments.

## Scope

Every open issue in `prime-radiant-inc/evener` gets at least one category
label and one type label. The goal is a backlog you can filter by label and
see a coherent work queue, not a wall of untagged reports. Triage is done in
batches against the full open set, not one-at-a-time as issues arrive.

## Tooling

The `gh` CLI is the only required tool:

```
gh issue list --repo prime-radiant-inc/evener --state open --limit 200 \
  --json number,title,labels,createdAt,body
```

Export the full set to JSON, read the bodies, and apply labels with:

```
gh issue edit <number> --add-label "label1,label2"
```

Labels are additive. Never remove a label during triage unless it is
demonstrably wrong — someone may have applied it for a reason the triager
doesn't see.

## Label vocabulary

### Type labels

| Label | Meaning |
|-------|---------|
| `bug` | Something is wrong — code produces an incorrect result, hangs, crashes, or violates a stated contract. |
| `enhancement` | A new capability or improvement that is not fixing a defect. |
| `documentation` | Docs-only change. |

Every issue gets exactly one type label. If a report is both a bug and a
feature request, it's a bug — the defect is the thing that shipped wrong.

### Category labels

Category labels say *where in the system* the issue lives. An issue can
carry several. These are the categories in use:

| Label | Scope |
|-------|-------|
| `drain` | Shutdown, drain, and one-shot run termination lifecycle. How a run that has finished actually exits. |
| `provider` | Provider API calls, rate limiting, retries, and network fetching (`web_fetch`, vision side-channels). |
| `agent-runtime` | Agent runtime behavior: tool execution, resource reporting, sandboxing, file confinement. |
| `prompting` | Agent prompting and policy — the system prompt, doctrine sections, and behavioral rules. |
| `turn-model` | Turn lifecycle and control-mutation semantics: Start, Stop, Steer, Queue, drain-as-steer. |
| `delegate` | Delegate and subagent lifecycle: spawning, disposal, attention, agent-type scoping. |
| `webui` | The evener-hub web UI. |
| `testing` | Test coverage, oracles, and test infrastructure. |
| `flake` | Load-sensitive or intermittent test failure. |
| `dev-tooling` | CI, fuzz infrastructure, coverage tooling, build scripts, and GitHub Actions. |
| `cleanup` | Dead code, doc drift, tidying. |

`flake` is always paired with `testing`. A flake is a testing issue first
and a flake second.

### Priority labels

Priority is assigned after categorization, to the subset the triager
recommends acting on. Most issues carry no priority label — that is
intentional. Priority is a recommendation, not a property of the issue.

| Label | Meaning |
|-------|---------|
| `P1` | Implement first. High impact on real runs, causes work loss or hangs, and the fix is bounded. |
| `P2` | High value, not as urgent or not as cheap. |
| `P3` | Deferred by ruling. |
| `P4` | Tripwire — implement when a specific trigger condition is met. |

### Lifecycle labels

| Label | Meaning |
|-------|---------|
| `parked` | Deliberately parked by ruling. Do not pick up. The issue is workable when the ruling changes. |
| `needs-review` | Work was attempted; awaiting review or a product ruling. |
| `eval-only` | The issue is shaped by eval/benchmark behavior and has questionable value for general users. See below. |

## The eval-only test

Not every issue observed in an eval run is an eval-only issue. Many
behaviors that fail in evals also fail for real users — a run that doesn't
exit, a delegate that deletes files it shouldn't, a provider call with no
timeout. Those are real bugs; the eval just made them visible.

An issue is `eval-only` when the proposed fix serves the eval shape, not
the general user. The signature:

- The doctrine or feature is specific to a task domain (ML benchmark
  training, competition scoring) and has no relevance to general agent
  work.
- The fix adds task-domain-specific rules to the general system prompt,
  increasing noise for every other task.
- The issue's own evidence is that the general prompt regressed a
  different task class after the doctrine landed.

When an issue is `eval-only`, label it and explain why in the triage
report. Do not close it — the underlying observation may be real. But do
not implement the eval-shaped fix in the general prompt.

## How to triage

1. **Export the full open set.** Pull every open issue with bodies. Read
   them all before labeling any. You cannot categorize an issue in
   isolation when the same defect is filed three times under three titles.

2. **Read the body, not just the title.** Titles are written for
   discoverability, not classification. The category comes from the
   mechanism described in the body, and the type comes from whether the
   mechanism is a defect or a gap.

3. **Assign type first, then category.** Is this a bug or an enhancement?
   Then: where in the system does it live? Apply labels with `gh issue
   edit --add-label`.

4. **Group by category.** After labeling, group issues into clusters. The
   clusters reveal cross-cutting failures: one drain bug is a bug; eight
   drain bugs is a subsystem that needs attention.

5. **Rank within clusters.** Within each cluster, which issues should be
   done first? The criteria, in order:
   - **Work loss.** Does the bug destroy or strand completed work? These
     come first.
   - **Hangs.** Does the bug make a run not terminate? These come next.
   - **Cross-cutting.** Does fixing this issue prevent a class of
     failures, not just one instance?
   - **Cost.** Is the fix bounded and verifiable, or open-ended?

6. **Pick the top five.** Across all clusters, name the five issues to
   implement first. Label them `P1`. The number five is deliberate: it is
   small enough to be a real plan and large enough to cover the
   cross-cutting fixes.

7. **Flag the bad ideas.** Identify issues that are `eval-only` or that
   propose over-engineered fixes. Label them and explain the reasoning.
   Do not close them unilaterally — surface them for a ruling.

8. **Write the report.** The triage output is a single report: the full
   categorized list, the top five with rationale, and the bad ideas with
   rationale. The report is the deliverable; the labels are the durable
   artifact.

## What triage is not

Triage does not diagnose or fix. The triager reads, categorizes, labels,
and ranks. If the triager finds a root cause while reading, that goes in
the issue body as a comment, not in the triage report as a fix. Triage is
intake; fixes are separate work with their own branches and tests.

Triage does not set priority on every issue. Most issues carry no
priority label. Priority is assigned to the recommended subset, not the
whole backlog — labeling everything `P2` makes priority meaningless.

Triage does not close issues. Even bad ideas stay open until a ruling.
The triager's job is to make the backlog navigable, not to prune it.
