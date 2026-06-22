# Tool Fluency Experiment: job_watch field groups

For `job_watch` create calls, use these field groups:

- Required top-level fields: `operation`, `source`
- Optional trigger fields: `events`, `event_filter`, `output_match`, `progress_interval_ms`, `every`

Choose the smallest set that satisfies the task, then submit the create call.
