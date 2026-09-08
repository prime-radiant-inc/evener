# iOS v1 remaining work

This checklist narrows native-mobile delivery to Jesse's selected iOS-only v1 scope. Android evidence and implementation remain deferred and do not block this list. The source-backed workflow ledger is authoritative for requirement boundaries and evidence status: [native mobile acceptance](acceptance.md). The original scope and dependency order are in the [takeover handoff](../../superpowers/handoffs/2026-09-06-native-mobile-takeover.md).

## Dependency order

1. Freeze the intended source head and build both iPhone and iPad Release artifacts from that same head. Record source, bundle, binary and SDK hashes.
2. Run current deterministic checks and canonical repository gates. Keep unrelated generated Apple changes intact.
3. Re-run real-daemon workflows against isolated authenticated v4 hubs with a scripted provider at the LLM boundary. Capture direct API readback and persisted state before native claims.
4. Qualify the current Release artifact on iPhone and iPad: lifecycle, hubs, workflows, reader, accessibility, typography, gestures, rotation and failure recovery. iPhone and iPad journeys can run in parallel when they use separate profiles and fixtures; shared artifact installation, hub mutations and final evidence identity remain coordinator-owned gates. Repeat required cases after cold launch where persistence is part of the contract.
5. Complete physical-device, signing, install/update and performance checks, then make the final release decision from one identity-matched record.

Source and deterministic gates can run in parallel with independent SDK recipe work and isolated fixture preparation. Device journeys, shared hubs, artifact installation and final integration remain serialized. Up to three bounded Luna workers can own disjoint lanes; one coordinator must own artifact identity, shared-device lifecycle and final gates.

## Implemented but not yet qualified on the current iOS Release artifact

- **Conversation and reader:** item paging, fragment/live merge, reconnect, stable transcript identity, caller-owned cursor recovery, rich Markdown, images, reader anchors and native Latest boundary are implemented. Focused service/native tests pass and dated iPad recovery evidence exists, but the current artifact still needs the fresh hub-removal journey, rich streaming/image combinations, VoiceOver and scale/performance coverage. See [reader recovery](ipad-reader-recovery-evidence.md), [reader continuity](reader-continuity-evidence.md), and the reader row in [acceptance](acceptance.md).
  The saved-anchor cold-launch failure is fixed in `7944778e0`: approximate restoration now retries when the furthest measured row advances and resets its bounded failure budget only after genuine progress. Installed launch plus two independent cold launches restored saved m12 at the exact pre-cold endpoint. Broader final-release reader, accessibility, scale, performance and workflow coverage remains pending.
- **Hub lifecycle and profiles:** switching, reconnect, drafts, scoped removal and two-hub identity are implemented. Requalify credential rotation, uncertain writes/lost replies, process death, iPad completion and physical LAN/pairing against the final artifact.
- **Composition:** send, steer, queue, stop and uncertain-delivery guards are implemented. Requalify held-turn, reconnect/conflict and keyboard/a11y cases on the final artifact.
- **Creation and administration:** session creation, images, launch settings, provider credentials/OAuth recovery, plugins, marketplaces, organization, fork and deletion routes exist in source. Their dated evidence is feature evidence; each needs a current-head iOS workflow pass where the ledger marks it open.
- **Decisions and workflow surfaces:** goals, tasks, activity, approvals and questions have native controllers and deterministic coverage. Real daemon pause/resume, stale decisions, delegate/task lifecycle, reconnect and accessibility remain qualification work.

## Confirmed source or coverage gaps

- Goals/tasks/activity, plugins/marketplaces and hub settings are still marked partial in the acceptance ledger. Resolve any missing advertised operation against current generated contracts before treating their iOS journeys as complete.
- The SDK cookbook has 90 of 91 catalog methods. Method 91 is the reserved, intentionally unsupported method; it is not an omitted supported feature. The cookbook count is recipe presence, not complete method outcome acceptance, and local package publication is separate.
- SDK notification producer coverage is tracked separately: the current scoped series reports 22 of 36 producers pending coordinator review, with the queue subset now verified by root. Do not convert this into a claim that all 36 notifications, reconnect behavior or lifecycle outcomes are qualified. See [SDK notification evidence](sdk-notifications-evidence.md) and [SDK management evidence](sdk-management-evidence.md).
- No whole-SDK readiness conclusion follows from the 90/91 and 22/36 counts. Complete the catalog-derived success/failure/disconnect matrix for supported methods and notifications, using independently installed packages and real producers where required.

## External qualification inputs still required

- VoiceOver focus, rotor/custom-action discovery and activation; largest text, reduced motion, dark/light, landscape and hardware keyboard behavior.
- Representative large transcripts and live streaming/image reflow with measured scroll/input latency and memory/leak observations.
- Physical iPhone/iPad networking and pairing, including signed install, update/relaunch and credential/keychain behavior. Simulator screenshots do not close these gates.
- Final current-head iPhone and iPad Release artifact identity, installation receipts, cold-launch/restart evidence and preservation checks for unrelated Apple files.

## Exit record

Close this list only when every implemented workflow has a current artifact identity, deterministic checks are green, real-daemon readback is retained, required iPhone/iPad journeys are observed, and physical/accessibility/performance/signing evidence is attached. Historical receipts remain provenance but cannot substitute for the final source and artifact record.
