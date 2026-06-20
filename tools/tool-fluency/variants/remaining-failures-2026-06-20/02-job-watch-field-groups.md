# Tool Fluency Experiment: job_watch field groups

For `job_watch` create calls, use these field groups:

- Top-level: `operation`, `target`, `events`, `event_filter`
- Nested delivery: `send.to`, `send.message`, `send.include_excerpt`

Choose the smallest set that satisfies the task, then submit the create call.
