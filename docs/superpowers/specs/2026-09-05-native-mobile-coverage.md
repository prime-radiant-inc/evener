# Native mobile coverage

Jesse's objective is an intuitive, reliable iOS and Android app with shared code,
multiple Evener hubs, and mobile access to Evener functionality. A working shell
is an iteration milestone. It is not feature completeness. Voice with barge-in
and control of other sessions is outside v1.

## Current evidence and remaining workflows

The protocol inventory comes from
cmd/evener-hub/frontend/src/protocol/types.gen.ts (MethodTypes). A method's
existence is not UI coverage. Each workflow needs a usable native surface and
behavioral verification, including errors, authorization, and hub isolation.

| Workflow | Native status | Work still required |
| --- | --- | --- |
| Hubs and connections | Named profiles, secure bearer credentials, switch, reconnect | Edit profiles, pairing, physical network and auth recovery checks |
| Browse conversations | First 50 distinct sessions, refresh, native navigation | Pagination, search, archived sessions, project organization, favorites and pin sections |
| Read conversation | Shared canonical projection, older history, expandable activity | Rich Markdown/code, attachments, long-transcript performance and accessible disclosure |
| Compose | Text send, stop, uncertainty recovery while mounted | Persistent hub-scoped drafts, attachments, command selection, queue and steering |
| Create session | Native project/harness/model/prompt flow; playground creation verified on both platforms | Real Evener E2E, large catalogs, physical keyboard/accessibility coverage |
| Manage session | Not exposed | Rename, fork, resume, clear, compact, shutdown; honor capabilities and identity |
| Model and launch settings | Not exposed | Model, vision model, reasoning, launch layers, schema validation and repository trust |
| Goals and work | Not exposed | Goals, tasks, background jobs/output, subagent previews and navigation |
| Approvals | Not exposed | Sandbox escalation resolution and any other user decision surfaces |
| Hub navigation | Not exposed | Favorite/archive, project/session removal, pin assign/unpin, section rename/delete |
| Provider authentication | Not exposed | Status, API keys, login/logout, device authorization, auth tests |
| Instances | Not exposed | List/create/edit/remove/default |
| Plugins and marketplaces | Not exposed | Browse/preview/install/upgrade/enable/disable/remove, source management, auto-upgrade |
| Hub preferences | Not exposed | Overview, transcript display settings, upgrade |
| Native distribution | Expo Go verified | Standalone iOS/Android builds, signing/distribution, physical devices |

## Verification boundaries

- The first slice was manually exercised in Expo Go on iOS26.5 and Android35.
  Production checks read the local hub. Scripted playground mutations prove the
  native network/UI path, not real Evener daemon or provider execution.
- Automated native tests exercise profile storage, the shared client handshake,
  draft recovery, and shared store/service integration over a loopback socket.
- Production session listing can take about 28 seconds and sometimes exceed the
  existing 30-second request limit. Reducing page size did not resolve this. A
  responsive app needs an investigation of the actual hub path before claiming
  performance readiness.
- Release readiness requires standalone builds, deterministic tests through real
  Evener plumbing with a scripted provider boundary, and manual native E2E of the
  completed workflows. Include Android back behavior, iOS gestures, keyboard,
  rotation, font scaling, screen readers, background/foreground, failed requests,
  reconnect and switching hubs with overlapping session identifiers.

Update this inventory with exact evidence as iterations land. Do not mark a
workflow complete merely because an RPC wrapper or generic settings form exists.
