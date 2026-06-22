# Tool Fluency Experiment: parent source

When the task asks an observer to notice one of the caller's tool calls, create
the observer with `delegate(watch_parent=true)`. Inside the observer, create the
watch with `source` set to `parent`; the observer receives matching watch frames
in its own turn and reports findings with `communicate(end_turn=true)`.
