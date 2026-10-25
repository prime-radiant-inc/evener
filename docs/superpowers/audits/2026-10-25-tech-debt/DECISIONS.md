# Decisions on the cleanup roadmap

Jesse's answers to the questions in [ROADMAP.md](ROADMAP.md) ("Decisions that are Jesse's"). Where a decision changes a roadmap entry, the entry has been edited to match.

## Q1. Version skew and state migration

Jesse's words: "we don't want to fuck over users who dont upgrade all at once or frequently. but the hub should always be as new as or newer than the daemons." On the two boundaries where versions can skew either way: "phone and hub tolerate remote hub being older."

Rules that follow:

| Boundary | Must tolerate | May drop |
|---|---|---|
| Hub and its daemons | Hub works with any older daemon | Daemon-side fallbacks for an older hub (the hub is always the same version or newer) |
| Phone and hub | Phone works with an older hub | Hub-side fallbacks for an older phone app |
| Local hub and remote hub over SSH | Local hub works with an older remote hub | Remote-side fallbacks for a newer local hub |
| On-disk state | Migrations stay, because users skip versions | Nothing on a time window alone |

How it applies to the gated compat entries:
- The "older hub" fallbacks (W1-59, x-compat-legacy-1): sort each by direction. A fallback in phone code or local-hub code for an older hub stays. A fallback in daemon code for an older hub goes.
- State migrations stay. This covers plugin-store migrations (W1-61), the serf-era and MEMORY.md migrations (W1-62), the legacy transcript projection and checkpoint formats (W1-63), and persisted client-state eras (W1-64). The allowed work there is deduplication and simplification that keeps every migration path working.
- Compat code with no production producer at all (dead in every direction) is T1 dead code, not compat, and can be deleted.

## Q2. Coverage floor

Drop it as a protected gate. Prune the coverage-wave and hub `cov_*` files after one before/after coverage report from CI, and keep the real behavior tests. Remove the matching rows in `scripts/fuzz/fuzz-targets.txt`, `scripts/coverage/fuzzcov-ignore.txt`, `scripts/coverage/coverage-floors.txt` and `testing-budget.json` in the same PR as the files. Unblocks W1-54.

## Q4. Shared durable writer

Home it in a new package in the `execsupport` module (`auth` takes the dependency). It fsyncs the file and its directory by default. Rebuildable caches use a variant named for skipping sync. Measure hot callers before switching them (see GAPS.md section 3, item 1). Unblocks W1-01..W1-03.

## Q10(a). Design mockup harnesses

They are final. Delete them (W0-05).

## Q12. AGENTS.md contributor rules

Approved with the roadmap's five-rule wording, plus the typo and path fixes (W0-01).

## Q17. Slash-command ambiguity

When two plugins define the same bare command, report it as ambiguous with a sentinel error, as skills already do, instead of picking one at random (W0-18).

## Still open

Q3, Q5, Q6, Q7, Q8, Q9, Q10(b–d), Q11, Q13, Q14, Q15, Q16, Q18, Q19, Q20, plus the `afero` question in GAPS.md (G7).
