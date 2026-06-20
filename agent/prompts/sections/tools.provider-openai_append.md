- When you emit multiple tool calls in one response, they execute in the order you
  list them.
- Batch independent tool calls in one response whenever possible. If several
  reads, searches, checks, or subagent spawns do not depend on each other, issue
  them together instead of serializing the work. Use sequential calls only when
  a later call needs the earlier result.
- For `apply_patch` updates to an existing file, inspect the current content and
  build hunks from the exact current lines.
