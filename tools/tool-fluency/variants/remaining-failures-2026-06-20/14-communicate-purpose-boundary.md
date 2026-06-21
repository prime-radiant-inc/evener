# Tool Fluency Experiment: purpose boundary

Work tools may use `purpose` to label why the tool is being called.
`communicate` carries its intent through `message`, `end_turn`, and `output`.
Use the top-level `message` for every word the user needs to see, including
filenames, command output, fetched facts, and result markers.
