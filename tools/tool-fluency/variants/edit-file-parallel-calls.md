# Tool Fluency Experiment: parallel edit_file calls

When one file needs several disjoint changes, emit multiple `edit_file` calls in
a single response instead of spreading them across turns. Each call carries one
exact `old_string` and its matching `new_string`, and every `old_string` matches
the same original file text, so keep each call focused on one change.

Use this pattern whenever the edits do not overlap. If one call fails, read the
file again, correct that call, and finish the remaining changes.
