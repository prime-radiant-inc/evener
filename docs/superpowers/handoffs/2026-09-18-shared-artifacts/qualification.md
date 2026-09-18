# Shared artifacts qualification — 2026-09-18

Base: `0a53ef91f517c8d323a2381a493233b51ab59e22` (`origin/main` when fetched).
New worktree: `/Users/jesse/.codex/worktrees/shared-artifacts/evener`.
Branch: `codex/shared-artifacts-qualification`. Checkout was clean at creation;
unrelated private-journal files in the original checkout were left untouched.

**G0 dependency experiments passed. Production acceptance A01–A22 is not yet
established.** The next G0 step is freezing executable schemas/domain vectors,
then G1's durable store. Raw execution remains disabled pending security and
lifecycle qualification. See [contract.md](contract.md) for normalized tool/results,
durable invocation identity, namespace control and exact source integration seams.

| Qualification | Actual result |
| --- | --- |
| Runtime | Go 1.27.0; Node 26.0.0; npm 11.12.1; macOS 26.5.2, Darwin arm64 |
| Go dependencies | Workspace MCP SDK v1.6.1, SQLite v1.50.1; root-isolated MCP is still v1.3.0 and needs explicit alignment |
| Browser package family | MCP-UI 7.1.1; ext-apps 1.2.0; JS SDK 1.27.1; Zod 3.25.76; installed and executed together in scratch |
| Protocols | Core MCP 2025-11-25; MCP Apps 2026-01-26; real SDK codecs exercised |
| Stateless HTTP | Real authenticated loopback client/server; JSON replies, metadata/resource round trips, domain errors and forbidden requests checked |
| Restricted host | Real SDK calls denied through owned null-client AppBridge and MCP-UI AppFrame; ordinary AppRenderer's default display handler is unsuitable |
| Input result codec | `{isError:true}` rejects; `{success:false}` does not express failure |
| Browser/origin | Installed Chrome 153.0.8010.36; separate `artifact-sandbox.localhost` proxy, no synthetic Hub cookie at sandbox hostname |
| Executing document | Opaque `srcdoc` frame, scripts-only sandbox, measured CSP denials on actual executing child; fixed parent policy permits the nested frame |
| Runner bridge | One-time window/nonce/generation handshake plus document-bound MessagePort; pre-load replacement attack denied while load revocation was held by a barrier |
| Handoff | ZIP found and extracted in ignored scratch; original hashes verified; all 22 domain descriptions and five script syntax checks passed |
| Baseline | `make test` exit 0: root, agent, llm, auth, envvars, invariant, identifier and web all passed |

All C01–C08 are **adopted** as explicit implementation decisions, with C05's
supported numeric domain clarified. C05 keeps raw number-token fingerprinting
(`1` differs from `1.0`), duplicate-key rejection and durable terminal conflicts.
State numbers must be finite binary64 values, with safe integers and no nonzero
underflow; exact decimal/arbitrary-precision values use strings. Token-sensitive
retry identity does not imply preservation of numeric spelling on a new explicit
browser save. This requires a narrow raw argument/execution path
for bundled tools before ordinary map decoding/repair. It does not change other
tools' numeric semantics. See the decision table in the contract for each rule.

The most consequential integration findings are:

- Existing MCP management eagerly connects and discards structured result/UI
  metadata. Add a lazy fixed catalog and an explicit host envelope through both
  live and persisted projections, separate from model text.
- Agent restore repairs orphaned tool calls; it does not replay a saved artifact
  mutation. Persist exact invocation bytes before network dispatch and reconcile
  them before ordinary orphan repair. Existing `AppendDurable` returning nil is
  not proof that fsync succeeded.
- Existing `threads.send()` resolves at local outbox commit. `ui/message` must
  await the normal daemon input receipt, retaining the same client mutation ID.
- Existing daemon auth and Hub-to-daemon traffic do not provide a daemon-role
  reverse broker. Add private bootstrap, verified lineage, narrowed grants, and
  qualified restart rebootstrap; browser credentials cannot substitute for it.
- Keep namespace provision/delete authority in private Hub/service control;
  durable association precedes ensure, deletion embargo precedes cleanup, pending
  tombstones precede fresh grants after restart.

Experiments live under `.superpowers/shared-artifacts-qualification/` in this
worktree. Backend command: `go run backend/sdk_fixture.go` from that directory.
Browser commands from `browser/`: `npm ci`, `node codecs.mjs`,
`node default-handlers.mjs`, `node qualify.mjs`; host TypeScript compilation also
passed with bundler resolution. Reports are `backend-audit.md` and
`browser-report.md`; exact pins and machine-readable evidence are retained there.
These harnesses are dependency experiments and do not replace production tests.

Remaining qualifications include service/agent process counts, durable SQLite
fault tests, real reference-app interaction and recovery, proxy/viewer navigation
revocation, remote HTTPS sandbox provisioning, browser families beyond Chrome,
and the full existing repository gates. No live-model trial has run. A Chrome
policy pass does not establish universal no-egress or CPU/memory containment.
