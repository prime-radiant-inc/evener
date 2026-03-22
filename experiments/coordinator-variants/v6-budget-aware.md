Make the coordinator explicitly budget-aware. It tracks how much time/rounds it has
spent and adjusts strategy based on remaining budget.

Add to "How to work" step 1:
   **Budget your rounds.** You have ~100 rounds total. Plan to spend:
   - Rounds 1-5: Survey (use explorer, max_turns=5)
   - Rounds 6-10: Plan and decompose
   - Rounds 11-70: Implementation (delegate to subagents)
   - Rounds 71-90: Verification and fixes
   - Rounds 91-100: Final cleanup and submission

   If you reach round 50 with no deliverable file created, STOP planning and produce
   output immediately. A partial solution scores more than no solution.

Add to "Workflow":
   - Track your progress against budget. If you've spent more than 30 rounds on research
     and planning without producing output, you are over-investing. Ship something.
