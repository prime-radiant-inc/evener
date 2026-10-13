# File-link MVP release plan

> **For agentic workers:** Use superpowers:subagent-driven-development for independent verification and the required simplify-code reviewers. This records Jesse's approved reduced shipping scope.

**Goal:** Ship clickable filenames in assistant messages through the existing web document pane and native Reader.

**Architecture:** Keep the committed owning-session references, current-worktree routing, parser and recovering reader implementation. Use a clean release worktree so unaccepted workspace navigation and scroll drafts remain separate.

**Tech Stack:** Go, AppWire, React, React Native, TypeScript, Vitest, Chrome and the installed native Markdown parser.

**Spec:** `docs/superpowers/specs/2026-10-02-worktree-file-links-design.md`, with Jesse's October 6 approval of the reduced MVP superseding its exhaustive release qualification gate. The original design and evidence remain historical records.

## Shipping boundaries

- Recognize plain paths, inline code and Markdown file links in assistant messages.
- Preserve existing exclusions for user messages, logs and parent delegate reports.
- Open the owning session's current worktree, including controller-relayed remote sessions.
- Keep typed errors, missing-file recovery and the existing Reload controls.
- Verify source, draft and position preservation in direct file-opening journeys.
- Preserve unaccepted drafts in `wip/worktree-file-links`; do not import them as a batch.
- Defer exhaustive workspace-history/layout, Vite generation/import-effects and native dependency audits.
- Report physical native touch and live SSH qualification as unperformed.
- Do not deploy or restart the hub.

## Release steps

- [ ] Verify the committed candidate in `wip/file-links-mvp`, including the native harness copies.
  - `make test-web`
  - `go test -p 4 ./cmd/evener-hub ./cmd/evener-hub/internal/fspaths -run '^(TestDocumentWorktree_|TestDoc|TestSessionDocument|TestSessionImage|TestResolve|TestUnder)' -count=1 -timeout 10m -v`
  - `go test -p 4 ./cmd/evener-hub -run '^TestDocumentFileLinks(Browser|FixtureCreate.*)$' -count=1 -timeout 10m -args -document-file-links-browser`
  - `node --test cmd/evener-hub/frontend/scripts/documentfilelinksguard/startup-regression.mjs`
  - From `mobile-native`: `npx tsx --tsconfig tsconfig.json scripts/check-markdown-file-links.mts`, relevant native tests, typecheck and script resolution checks.
- [ ] Merge the discovered current base without dropping either side's behavior. Re-run affected checks after conflicts are resolved.
- [ ] Run all four simplify-code reviewers on the whole release diff: Reuse, Simplification, Efficiency and Altitude. Apply only behavior-preserving findings and re-run checks.
- [ ] Commit the release-owned harnesses and documentation with normal hooks.
- [ ] Create the PR in `prime-radiant-inc/evener`, then shepherd one push, CI run and current-head review per round.
- [ ] Merge only after every required check is green, the review is current and only low or conclusively refuted findings remain. Existing repository authorization permits admin squash merge to satisfy the approval barrier only.

## Evidence limits

Passing existing tests does not establish every scenario from the original broad qualification plan. Record direct behavior evidence and gaps in the PR. Keep historical failed runs and preserved drafts intact.
