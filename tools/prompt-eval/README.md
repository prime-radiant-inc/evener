# Prompt evaluation

These tasks measure how a system prompt shapes the way agents write and behave. They exist for the system prompt rewrite (`docs/superpowers/specs/2026-09-26-system-prompt-prose-rewrite-design.md`) and for later prompt work.

The tasks run on the tool-fluency runner. Each task is a manifest with a fixture, a prompt written the way a colleague would ask, and checks that judge the outcome of the work. No check reads the system prompt; the prose tools count what the agents wrote.

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

## Running

Build the runner and one evener binary for each prompt version:

```bash
go build -o /tmp/lab/evener-fluency ./tools/tool-fluency/cmd/evener-fluency
go build -o /tmp/lab/evener-v0 ./cmd/evener
```

Run every task on several models, three times each. Each prompt version gets a label with a digit in it, such as `v0` for the baseline and `v1-A` for a draft, because `review-pack` masks the label wherever it appears:

```bash
/tmp/lab/evener-fluency matrix \
  --version v0=/tmp/lab/evener-v0 \
  --models lunarouter/deepseek-4.1-flash,lunarouter/glm-5.3-vision \
  --probes-dir tools/prompt-eval/tasks \
  --repetitions 3 --timeout 25m --max-concurrent 4 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash \
  --out $PWD/tools/prompt-eval/results/example
```

A run refuses an `--out` that already holds results, so give each run its own directory, or add new version labels to an existing one.

Count the prose each version produced:

```bash
/tmp/lab/evener-fluency prose-stats --results v0=$PWD/tools/prompt-eval/results/example/v0
```

Make blind packets for a read against `rubric.md`. Keep the key somewhere the readers never look:

```bash
/tmp/lab/evener-fluency review-pack \
  --results v0=$PWD/tools/prompt-eval/results/example/v0 \
  --mask-root $PWD/tools/prompt-eval/results/example \
  --packets /tmp/lab/review/packets --key /tmp/lab/review-key.json
```

Results land under `tools/prompt-eval/results/`, which git ignores. Each fixture still runs as its own project: the runner turns Go workspaces off and stops git from looking above the fixture, for the task checks and for the agent's commands. Use absolute paths for `--out`, since each result records where its sessions live.

## Adding a task

Write the manifest in `tasks/`, then run `go test ./tools/tool-fluency/cmd/evener-fluency/ -run TestPromptEvalTasks`. The test decodes every manifest strictly. When a task has a `reference` solution, the test proves its checks fail before the solution and pass after it. When a task asks for no change, the test proves its checks pass untouched. It also requires every Go file in a fixture to be gofmt-formatted, so an agent that formats the tree changes nothing it was not asked to.
