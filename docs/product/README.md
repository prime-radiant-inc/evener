# Evener product guide

Evener helps people accomplish work with a coding agent. Its runtime, tools,
hub, and clients form one product: the person should be able to express an
intent, follow the work, steer it, and use the result without managing Evener's
internal machinery.

These are living references. Update the relevant page when behavior or ownership
changes; keep implementation history in Git and pull requests.

- [Product principles](principles.md): the experience changes should produce.
- [Subsystem map](subsystems.md): responsibilities, sources of truth, entry
  points, dependencies, and recovery ownership across the product.
- [Session activity](session-activity.md): scoped delegate, job and watch reads,
  navigation boundaries, pagination and shared recovery ownership.
- [Friction punchlist](friction.md): open, evidence-backed gaps, with unresolved
  proposals, agreed outcomes and deferred work identified separately. Discuss
  unresolved choices individually; an agreed outcome still needs implementation
  and verification before it describes current behavior.
- [Code architecture](../architecture.md): module and process structure.
- [Web design system](../web-ui/design-system.md): shared visual and interaction
  contracts. [Native development](../../mobile-native/README.md) describes the
  phone client's implementation and qualification.
- [Developing Evener](../developing-evener/README.md): how to build and verify a
  change.

## Keeping the guide useful

The principles express product intent. The map describes the implementation.
The punchlist records gaps between them. Keep those meanings distinct: adding a
case does not silently change a runtime contract, and documenting existing
behavior does not endorse it.

When changing a subsystem, follow the map to its dependent surfaces and update
its row and linked contract in the same change. When discussing a punchlist case,
record the chosen behavior beside the evidence. Once implemented and verified,
put the durable rule in the owning guide and remove the resolved case from the
open punchlist. Git retains its history. Do not renumber surviving case IDs.

Use stable filenames, present-tense explanations, source links, and named
functions or types. Do not turn these pages into release notes, dated audit
diaries, PR progress reports, or duplicate implementation plans. Recheck source
references when touching a case; a historical decision document cannot establish
what the current product does.
