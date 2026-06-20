# Tool Fluency Experiment: combined watch guidance

For watch-driven tasks:

1. Start the observer and wait for readiness.
2. Create `job_watch` with operation fields at the top level and delivery fields under `send`.
3. Trigger the watched action after the watch is installed.
4. Treat the callback message as completion evidence.
5. Finish with the requested literal result token in the final result message.
