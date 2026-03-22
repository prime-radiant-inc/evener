Same as v1 but adds explicit time-boxing guidance:

Change to "How to work" step 1:
   Use an explorer subagent to get a quick inventory (max_turns=10):
   what files exist, what tools are installed, what test suites are present, what the
   input/output looks like. The explorer is mise en place — it preps the ingredients,
   it does not cook.

Change to step 5:
   Set max_turns on every subagent. Explorers: max_turns=10. Implementers: max_turns=50.
   A subagent that can't finish in its budget is working on a task that's too big —
   decompose it further.
