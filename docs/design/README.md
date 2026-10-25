# Design records

Historical and working design material. These are **records, not references**:
each one describes the design at the moment it was written, and many describe
behavior that has since shipped differently or not at all. For current behavior,
use `docs/` (the user guides) and `docs/product/` (ownership and gaps).

The date in a filename is when the record was written. Where a document opens
with a status or a "historical design record" note, that note wins.

## What is here

| Path | Contents |
| --- | --- |
| `plans/` | Implementation plans and task breakdowns for past work. |
| `mobile/` | iPhone redesign material, checkpoints, and build/distribution notes. `mobile/ios-build-distribution.md` is still the living build procedure and is linked from the native guide. `mobile/acceptance.md` and `mobile/ios-v1-remaining.md` carry current native qualification work. |
| `onboarding/` | First-run onboarding prototype, research, and verification record. |
| Root `*.md` | Standalone design and experiment records: prompt composition, hub diagnostics, compaction comparisons, fuzzing toolkit, SDK migration inventory. |

## Using a record

- A record can explain **why** a decision was made. It cannot establish what the
  product does now.
- When a record and a current guide disagree, the guide is right. If the record
  is the only place a rule is written down and the rule still holds, move the
  rule into its owning guide rather than linking readers here.
- Do not add rollout logs, PR progress, or review threads. Git and the pull
  request keep those.
- New records go in the dated `YYYY-MM-DD-name.md` form. Prefer a directory per
  effort when an effort grows past a couple of documents.
- Screenshots and other evidence images go on the pull request. Commit an
  image only when a record links to it.

Links inside these records point at paths and line numbers from when they were
written and are deliberately excluded from `make docs-check`.
