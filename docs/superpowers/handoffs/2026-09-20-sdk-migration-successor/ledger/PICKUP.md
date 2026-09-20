# Mobile SDK queue pickup

Live snapshot: **2026-09-19T23:13:22Z UTC**. Coordinator: /Users/jesse/.codex/worktrees/mobile-sdk-1934-pickup/evener, branch codex/mobile-sdk-1934-pickup.

This ignored pickup was refreshed from live GitHub REST and targeted merge receipts. Product code, refs, PRs, issues, CI, and hosted artifacts were left unchanged; no hosted artifact was republished. PICKUP.md remains Markdown.

## Current merge and accounting

- **Twenty-two takeover merges** are recorded. Recent receipts: #2014 -> 9c789aeda8ce8399554f6cf59cad78317546e060, #1968 -> 4413c18eef00ea4dcfab1b8e78cc32e9618e38dd, #2024 -> 7b23fd083416bb7944b652ed73116b4949da8a80 at 22:21:21Z, and #1982 -> cbf6d80b07b8c83c0fbb6eec05b335f468b34fbe at 22:32:53Z. #2024's reviewed head is d521522b... with #2014's 9c789aeda... as its direct reviewed parent; #1982's reviewed head is 7144d78f....
- Current REST accounting is **50 repository-open PRs** and **27 handoff rows**: **14 carried implementations**, **11 parked/dependent implementations**, and **2 reference/docs rows**. The carried set no longer includes merged #1982 or #2024. The full current JSON is in project-pr-accounting-current.json.
- The active web-first parents are #2005 at 755f6a1ecd... and #1940 at 96a2cc059c...; #1952 remains 709a65ca.... Native remains secondary at #1981 c8828ce2... and #1983 de951b7a....

## Web-first queue

| lane | current state | next dependency |
| --- | --- | --- |
| History / settings recovery | #1982 and #2024 merged; #1968 and #2014 merged | Open fast follows #2006 (history provenance coverage) and #2026 (raw identity coverage). Preserve the private 1982 Medium plan separately. |
| Ask / carrier | #2005 open at 755f6a1e... | Qualify the current head, then open separate #2015 timeline and #2016 TUI test follow-ups against the final head. |
| Marketplace server | #1940 open at 96a2cc05... | Qualify the outcome contract, then split #2027 behavior, #1944 wire coverage, and #1951 diagnostics into focused PRs. |
| Marketplace SDK / consumers | #1954/#1960 and related consumer rows open | #1953 row validation and #1959 lifecycle coverage remain separate dependent fast follows. |
| Reconnect | #1922/#1952/#1955 open | Keep #1942 as a focused native follow-up after the stack settles. |
| Offline | predecessor chain merged through #1904 and successors | #1941 and #1948 are distinct offline Low fast follows; remeasure #1941 before editing. |
| Native secondary | #1981/#1983 open; local A2 and recovery panel work held | Turn #2008 and #2020 into actual focused PRs if retained; do not count local qualification as publication. |

## Low policy follow-ups

The scoped REST inventory has 15 open follow-up issues: #2006, #2008, #2015, #2016, #2020, #2026, #2027, plus earlier #1941, #1942, #1944, #1946, #1948, #1951, #1953, and #1959. Issue-only parking is no longer sufficient. The independent merged-parent group is #2006/#2026/#1948/#1946, with #1941 requiring a landed-code reread. The unmerged-parent groups are #2015/#2016 on #2005, #2027/#1944/#1951 on #1940, and #1953/#1959 on the marketplace consumer bases. Native secondary work is #2008/#2020/#1942.

## Held and unopened work

- Native Host B, durable rows, recovery, and TestFlight/device acceptance remain held behind the web-first queue.
- D6 Stack A pieces 11–13 remain unopened; publish surface, local store, cross-tab sync, transitions, and hub half are still not started.
- Public coverage/export and metadata remain held behind the private history findings. P10a is locally qualified but blocked on its P7/chain; P10b/c are not started.
- Reconnect corrections target existing PRs. Outbox A2 remains locally qualified for parking only; Host B and the web edge remain held.

Sources and exact issue dependency map are in low-fast-follow-audit-2026-09-19.md. Current local artifacts were not published.
