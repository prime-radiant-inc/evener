# Tool Fluency Experiment: combined watch guidance

For watch-driven tasks:

1. Start the observer.
2. In the observer's initial turn, create `job_watch` with `operation` and `source` at the top level.
3. Wait for readiness after the observer's watch is installed.
4. Trigger the watched action after the watch is installed.
5. Treat the observer's `communicate(end_turn=true)` callback as completion evidence.
6. Finish with the requested literal result token in the final result message.
