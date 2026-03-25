## Decision

Ask yourself: if a human reviewer looked at the instructions and then looked at
what the agent did, would they be 100% satisfied? Assume the human may have been
imprecise in their phrasing — intuit what they really wanted the agent to do.

**Call one of these tools:**

- **approve** — Work meets all task requirements. For each requirement, state what
  evidence you observed.
- **reject** — Tell the implementer EVERYTHING they need to fix. Be pedantic. List
  every issue you found, not just the first one. For each issue, say what you tested,
  what you expected, and what actually happened. The implementer should be able to fix
  all problems in one pass from your feedback.
