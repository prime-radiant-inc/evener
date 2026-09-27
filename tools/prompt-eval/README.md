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
go build -o /tmp/lab/evener-baseline ./cmd/evener
```

Run every task on several models, three times each:

```bash
/tmp/lab/evener-fluency matrix \
  --version baseline=/tmp/lab/evener-baseline \
  --models lunarouter/deepseek-4.1-flash,lunarouter/glm-5.3-vision \
  --probes-dir tools/prompt-eval/tasks \
  --repetitions 3 --timeout 25m --max-concurrent 4 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash \
  --out tools/prompt-eval/results/example
```

Count the prose each version produced:

```bash
/tmp/lab/evener-fluency prose-stats --results baseline=tools/prompt-eval/results/example/baseline
```

Make blind packets for a read against `rubric.md`. Keep the key somewhere the readers never look:

```bash
/tmp/lab/evener-fluency review-pack \
  --results baseline=tools/prompt-eval/results/example/baseline \
  --mask-root tools/prompt-eval/results/example \
  --packets /tmp/lab/review/packets --key /tmp/lab/review-key.json
```

Results land under `tools/prompt-eval/results/`, which git ignores.

## Adding a task

Write the manifest in `tasks/`, then run `go test ./tools/tool-fluency/cmd/evener-fluency/ -run TestPromptEvalTasks`. The test decodes every manifest strictly. When a task has a `reference` solution, the test proves its checks fail before the solution and pass after it. When a task asks for no change, the test proves its checks pass untouched. It also requires every Go file in a fixture to be gofmt-formatted, so an agent that formats the tree changes nothing it was not asked to.
