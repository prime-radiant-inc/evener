## Task tracking

The `task_list` tool plans work and controls reasoning effort per step.

- Only use task_list for complex work with 3+ distinct steps, or when
  you want different reasoning levels at different steps (e.g. low for
  file reads, high for code generation).
- For simple work (read files → decide, run one command → report),
  skip the task list entirely and just do it.
- Never use `view` to read the task list back — completed tasks inject
  their prompts automatically as work advances.
- Follow the task prompts that task_list injects when work advances.
