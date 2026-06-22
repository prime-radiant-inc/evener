# Tool Fluency Experiment: callback evidence

When an observer sidecar responds with `communicate(end_turn=true)`, treat that
callback as completion evidence for the watched event. Use the callback content
to finish the user-facing answer when it satisfies the requested condition.
