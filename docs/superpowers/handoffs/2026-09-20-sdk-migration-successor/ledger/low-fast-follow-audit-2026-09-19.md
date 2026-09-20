# Low fast-follow audit

Snapshot: **2026-09-19T23:13:22Z**. Source: live GitHub REST (pulls state=open, 50 repository-open PRs), targeted PR receipts for #2014, #1968, #2024, and #1982, and issue REST for the scoped open Low follow-ups. This report is coordinator-local and ignored.

The current accounting is 50 repository-open PRs and 27 handoff rows: 14 carried implementations, 11 parked/dependent implementations, and 2 reference/docs rows. Twenty-two takeover merges are recorded. The newest receipts are #2024 -> 7b23fd083416bb7944b652ed73116b4949da8a80 at 22:21:21Z and #1982 -> cbf6d80b07b8c83c0fbb6eec05b335f468b34fbe at 22:32:53Z. #2024's reviewed head is d521522b...; its direct reviewed parent is #2014's 9c789aeda.... #1982's reviewed head is 7144d78f.... The current open heads that matter here are #2005 755f6a1e... and #1940 96a2cc05....

All 15 issues below are still open in REST. Their descriptions carry the Low/follow-up findings even though the issue labels are empty. Under the current policy, these must become focused PRs or be explicitly withdrawn; leaving them as issue-only parking is not completion.

| issue | parent or dependency | focused follow-up | disposition |
| --- | --- | --- | --- |
| #2006 | **Merged** #1982 (7144d78f...; issue text names stale e6d550e...) | Grow active tool-call/result candidates in the provenance-cost regression fixture. | **Startable now** as an AppWire test-only fast follow-up. |
| #2026 | **Merged** #2024 (7b23fd08...; reviewed head d521522b...) | Assert decoded-equivalent records with different canonical raw shape reset identity/generation. | **Startable now** as a focused SDK/store test PR. |
| #1941 | **Merged** offline chain (#1904 and predecessors) | Recheck remaining fixture/read duplication and make the live-model nudge rejection observable. | **Startable after a landed-code reread**; keep separate from storage conformance. |
| #1948 | **Merged** offline storage replacement | Exercise CAS/insert, malformed/null storage, and canonical identity through rawStringDraftBackend. | **Startable now** as an independent native test-only PR. |
| #1946 | **Merged** ask/carrier chain (#1907/#1962/#1977) | Add a live/restore oracle for the existing kindless journal shape and only then make a reachable correction. | **Startable now** as an independent agent test follow-up. |
| #2015 | **Open** #2005, current head 755f6a1e... (issue text names stale 74ef4039...) | Add interrupted-salvage timeline label, explicit gap classification, and table coverage. | **Separate PR after #2005 settles or rebase against its final head.** |
| #2016 | **Open** #2005, current head 755f6a1e... (issue text names stale 74ef4039...) | Make the TUI fixture honest/consolidated at the existing kind-agnostic boundary. | **Separate PR after #2005 settles; do not widen transcript types.** |
| #2027 | **Open** #1940 at 96a2cc05... | Preserve AppliedUnavailable when post-apply list reconciliation fails. | **Focused server behavior PR after #1940's outcome contract lands.** |
| #1944 | **Open** server #1940 | Verify AppWire round trips for available arrays, empty arrays, and AppliedUnavailable. | **Independent server/protocol test PR after #1940.** |
| #1951 | **Open** server #1940 | Log the secondary reconciliation read failure while preserving the typed outcome. | **Independent server observability PR after #1940.** |
| #1953 | **Open** SDK #1954 | Validate every marketplace row before publishing an applied snapshot. | **Independent SDK validation PR after #1954's current contract settles.** |
| #1959 | **Open** web #1960; related native lifetime consumers remain open | Deliver late applied-removal outcomes across client/page lifetimes. | **Focused lifecycle test PR after consumer bases settle; keep separate from #1944/#1953.** |
| #1942 | **Open** reconnect stack #1922/#1952/#1955 | Consolidate retained-screen connection wiring only where lifecycle contracts match; preserve the modal visibility behavior. | **Native follow-up after the reconnect stack lands; not an issue-only disposition.** |
| #2008 | **Open** native runtime #1981 / local A2 | Retry NativeMutationRuntime.start after timer setup failure instead of latching started state. | **Independent narrow runtime PR; base on the current runtime head.** |
| #2020 | **No GitHub parent**; local recovery-status slice 4dd099f3... | Make exported recovery action derivation record/eligibility-aware with focused helper tests. | **Create a real focused PR from the current main-compatible slice; do not count local qualification as publication.** |

## Small independent groups

1. **Merged-parent fast follows (web-first):** #2006, #2026, #1948, and #1946 each have a bounded test or oracle contract and can be opened independently now. #1941 is also small but needs a fresh landed-code measurement first because its issue explicitly warns that earlier helper-removal assumptions are stale.
2. **Ask/carrier follow-ups:** #2015 and #2016 are two separate small PRs against the final #2005 head. They should not be combined: one changes native presentation classification, the other corrects TUI test intent.
3. **Marketplace server/protocol follow-ups:** #2027, #1944, and #1951 are separate focused PRs after #1940. #2027 is applied-outcome behavior; #1944 is wire coverage; #1951 is diagnostic coverage. They share the outcome contract but do not need a single implementation.
4. **Marketplace consumer follow-ups:** #1953 is SDK row validation; #1959 is page/client lifetime coverage. Keep both behind their current consumers (#1954/#1960 and the related native lifetime work), with no new cross-consumer framework.
5. **Native secondary:** #2008, #2020, and #1942 are independent native follow-ups with different seams (runtime startup, recovery action eligibility, and retained-screen connection UI). They remain secondary to the web-first recovery queue but should each become a real PR if retained.

The current order is therefore: land/qualify web-first parents and open the merged-parent fast follows; prepare #2005/#1940 dependent PRs against final heads; then turn the native secondary issues into focused PRs. No code, branch/ref, PR, issue, CI, or hosted artifact was changed by this audit.
