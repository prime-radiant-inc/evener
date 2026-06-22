# Tool Fluency Experiment: combined watch guidance

For watch-driven tasks:

1. Start the observer and wait for readiness.
2. In the observer, create `job_watch` with `operation` and `source` at the top level.
3. Trigger the watched action after the watch is installed.
4. Treat the observer's `communicate(end_turn=true)` callback as completion evidence.
5. Finish with the requested literal result token in the final result message.
