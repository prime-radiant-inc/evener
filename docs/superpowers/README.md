# Superpowers working records

Specs, plans, reviews, and evidence produced while building features. These are
**process records**: they document how a change was planned and verified, not
what the product does. Treat them as history.

For current behavior use `docs/` and `docs/product/`. For the design law of the
web UI, use [`docs/web-ui/design-system.md`](../web-ui/design-system.md).

## What is here

| Path | Contents |
| --- | --- |
| `specs/` | Feature specs, including agreed designs and their review rounds. |
| `plans/` | Task-by-task implementation plans, with checkboxes and evidence. |
| `research/` | Reading and comparison studies that informed a design. |
| `reviews/` | Independent review records. |
| `handoffs/` | Context written for a successor session or engineer. |
| `notes/` | Loose working notes. |
| `proofs/` | Captured evidence for a specific claim or gate. |
| `mockups/` | Static mockups and reference images. |

## Status rules

- A plan is not a promise. A checked box means a step was done in that effort,
  not that the behavior still exists.
- A spec marked **Proposed** describes an intention. A spec marked **Approved**
  records a decision; the code and the current guides show whether it shipped.
- Where a plan and a guide disagree, the guide is right.
- Do not rewrite an old record to match today's behavior. Write the current rule
  into its owning guide and, if the record is worth keeping, leave it as it was.

Links inside these records point at paths and line numbers from when they were
written and are deliberately excluded from `make docs-check`.
