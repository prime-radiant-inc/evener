# Observer and sidecar use-case research

Date: 2026-06-18
Branch: `wip/job-control-handle-split-impl`
Status: research only; no API decision is approved here.

This note records the observer/sidecar use-case inventory gathered from four
independent systems-design lenses, plus one follow-up use case that was missing
from the first pass: memory/context injection. The goal is to preserve the raw
research shape before reducing it to the smallest Serf API that can make these
cases easy for agents without adding unnecessary primitives.

Current Serf baseline: observer sidecars are a composition of ordinary
`delegate`, `job_watch`, and `delegate_send` behavior. The recent live Kimi
scenario showed the key gap: caller event frames can say that a `communicate`
event happened, but they do not include the communicated content. That makes
content-triggered observer behavior impossible and can create noisy feedback
loops when the observer's own notifications become watched caller events.

## Headline findings

1. Useful observers need content-bearing frames. Metadata-only frames are enough
   for notification, but not for review, escalation, policy, memory, or
   coordination.
2. Observation, reporting, alerting, and injection are separate concepts. The
   default should be observe/report, not steer.
3. Most use-case policy belongs in the sidecar. The platform should enforce
   identity, provenance, redaction, ordering, permissions, loop controls, and
   delivery semantics.
4. A new observer primitive is not obviously needed. The research points toward
   better watch frames plus cleaner route semantics.
5. The hardest safety issue is invisible mutation: sidecars that change the main
   agent's context, route, or authority without a visible, source-linked event.

## Product and workflow lens

### 1. Compliance gatekeeper

- User story: a regulated team wants a sidecar to catch policy violations before
  sensitive actions reach production, customers, or external systems.
- Desired behavior: stay silent during normal work, flag suspected violations,
  and optionally request approval before risky actions execute.
- Frame needs: proposed action content, target system, actor/session ID, tool
  name, tool inputs, policy context, redaction labels, timestamp, correlation ID.
- Edge cases: overblocking, missing violations because content was absent,
  stale policy, false positives during debugging, ambiguous approver ownership.
- Anti-goals: not a generic reasoning critic and not a replacement for target
  system authorization.
- API pressure: pre-action frames need complete structured payloads and a clear
  report/escalate/intervene permission split.

### 2. Long-running job concierge

- User story: a user starts a multi-hour job and wants useful progress updates
  without reading raw logs.
- Desired behavior: summarize milestones, blockers, retries, and meaningful
  state changes in a side thread or operator notification.
- Frame needs: job goal, current step, recent messages, tool calls/results,
  errors, elapsed time, retry count, state transitions.
- Edge cases: mistaking busy logs for progress, repeated summaries, vague
  summaries from thin frames, non-actionable interruptions.
- Anti-goals: not a live transcript mirror or heartbeat spammer.
- API pressure: frames should distinguish milestone, heartbeat, error, retry,
  and completion events; side-thread output should be the default.

### 3. Human approval broker

- User story: operations teams want agents to prepare risky changes while humans
  approve only the decision points that matter.
- Desired behavior: package the exact decision, notify the right approver, and
  relay the result back only through explicit permission.
- Frame needs: proposed action, diff/preview, blast radius, rollback plan,
  target environment, deadline, alternatives, exact approval question.
- Edge cases: vague approval prompts, stale approvals, state changes while
  waiting, wrong approver route.
- Anti-goals: not a broad chat participant and not allowed to reinterpret an
  approval into a different action.
- API pressure: approval frames need immutable payloads, expiry, approver
  identity, and narrow response routing.

### 4. Multi-agent dependency coordinator

- User story: a lead agent delegates work to several subagents and needs a
  sidecar that spots dependency conflicts without micromanaging workers.
- Desired behavior: detect blocking, invalidating, or overlapping work and tell
  the lead only when coordination is needed.
- Frame needs: parent job ID, child session ID, assigned scope, resource claims,
  status, outputs, blockers, dependency declarations, handoff artifacts.
- Edge cases: feedback loops between workers, false conflicts, stale dependency
  maps, low-value lead interruptions.
- Anti-goals: not a scheduler and not omniscient shared memory.
- API pressure: frames need parent/child correlation and scoped visibility; the
  observer should report to the lead but not steer workers by default.

### 5. Incident response scribe

- User story: during an outage, responders want decisions, timeline, symptoms,
  mitigations, and open questions captured automatically.
- Desired behavior: maintain a structured incident log and escalate only on
  missing ownership, contradictions, or unresolved critical questions.
- Frame needs: messages, commands, alerts, severity, affected services,
  timestamps, tool outputs, decisions, owners, hypotheses, mitigations.
- Edge cases: sensitive data capture, speculation recorded as fact, responder
  interruption, broken chronology under parallel work.
- Anti-goals: not incident commander and not root-cause oracle.
- API pressure: observers need append-only timeline outputs, source attribution,
  sensitivity labels, and fact/hypothesis/decision/action classifications.

### 6. Customer support escalation watcher

- User story: support teams want urgent, emotional, legal, or high-value tickets
  escalated before AI-assisted handling causes damage.
- Desired behavior: monitor ticket context and drafted replies, stay silent for
  routine cases, and produce a concise human handoff when escalation criteria
  match.
- Frame needs: customer text, account tier, sentiment/risk signals, agent draft,
  cited policies, ticket history, SLA, channel, proposed next action.
- Edge cases: escalation spam, missed sarcasm or legal threats, private data in
  notifications, unnecessary tone changes.
- Anti-goals: not a second support bot and not a generic sentiment dashboard.
- API pressure: frames must include actual customer/draft content and route
  escalation by risk category.

### 7. Quality auditor for deliverables

- User story: a professional services team wants deliverables inspected before
  client delivery.
- Desired behavior: review outputs against acceptance criteria, source material,
  and client constraints; report privately or request revision if allowed.
- Frame needs: final artifact, task brief, acceptance criteria, source
  references, assumptions, validation checks, client constraints, deadline.
- Edge cases: subjective style complaints, duplicate authorship, harmless
  deviations flagged, missing source material.
- Anti-goals: not a full second author and not a blocker for low-risk drafts
  unless configured.
- API pressure: artifact snapshot frames need provenance, criteria, and
  severity-ranked findings.

### 8. Cost and quota sentinel

- User story: a platform team wants to prevent runaway jobs from burning budget,
  quota, or vendor limits.
- Desired behavior: track usage and warn or escalate before thresholds are hit.
- Frame needs: token usage, tool-call counts, API usage, elapsed time, retry
  loops, model/tool choices, job budget, team quota, baseline.
- Edge cases: stopping legitimate expensive work, noisy near-threshold warnings,
  planned bursts, unactionable cost alerts.
- Anti-goals: not a product analytics system and not a substitute for hard
  platform quotas.
- API pressure: observers need normalized usage frames, budget metadata,
  loop/retry signals, and limited intervention permissions.

### 9. Security secrets monitor

- User story: security teams want accidental exposure of credentials, PII, or
  restricted data detected while agents read files, logs, and chat content.
- Desired behavior: inspect content, log redacted findings, notify responsible
  humans, and block outbound propagation only if explicitly configured.
- Frame needs: snippets or hashes, source location, destination/action, data
  labels, redaction status, tool inputs/outputs, session/user identity.
- Edge cases: observer becoming another leak, false positives on fixtures,
  encoded secrets, legitimate security work blocked.
- Anti-goals: not a full DLP product and not allowed to store raw secrets
  unnecessarily.
- API pressure: content access must carry redaction controls, sensitivity
  metadata, retention limits, and safe finding output.

### 10. Test failure triage observer

- User story: development teams want an observer to spot thrashing on tests or
  symptom-fixing instead of root-cause debugging.
- Desired behavior: watch test runs, diffs, errors, repair attempts, and repeated
  failure patterns; emit concise diagnosis when thresholds match.
- Frame needs: test command, failing output, changed files, diff summary, retry
  count, error signatures, agent rationale, previous attempts.
- Edge cases: interrupting debugging too early, overfitting to command output,
  missing source context, encouraging superficial fixes.
- Anti-goals: not a pair programmer commenting on every edit and not a linter.
- API pressure: frames need grouped failure-attempt state, diff/test
  correlation, and retry thresholds.

### 11. Knowledge capture and runbook builder

- User story: teams want valuable operational knowledge captured without making
  the main agent document everything live.
- Desired behavior: watch successful workflows, decisions, commands, errors, and
  recovery steps; draft a runbook for human review.
- Frame needs: goal, environment, commands, outputs, decisions, errors,
  resolution, assumptions, versions, final state.
- Edge cases: one-off hacks becoming canonical docs, environment leaks, premature
  documentation before success, bloated notes.
- Anti-goals: not automatic official-doc publishing and not transcript
  formatting.
- API pressure: support candidate-knowledge outputs, source-linked summaries,
  and human review before promotion.

### 12. SLA and workflow handoff monitor

- User story: workflow owners want agent-driven processes to avoid stalls between
  systems, teams, and required handoffs.
- Desired behavior: track workflow state, detect missing handoffs or expired
  SLAs, and summarize what waits on whom.
- Frame needs: workflow ID, state, expected next step, owner, due time, blocking
  dependency, last meaningful event, handoff payload, external IDs.
- Edge cases: confusing wait states with stalls, wrong owner routing, duplicate
  workflow-system alerts, injecting pressure into the wrong session.
- Anti-goals: not a BPM engine and not a universal notification router.
- API pressure: frames need workflow state, ownership, deadlines, idempotency
  keys, dedupe, and read-only defaults.

## Reliability, safety, and incident lens

### 13. Destructive action gate

- User story: a production agent is about to delete data, rotate credentials,
  terminate instances, rewrite history, or apply irreversible changes.
- Desired behavior: alert or block only when a destructive action is proposed
  without required approval, dry-run evidence, or rollback notes.
- Frame needs: tool name, full arguments, environment, resource IDs, command
  text, approval state, actor/session ID, timestamp, policy labels.
- Edge cases: redaction hides target, retries duplicate alerts, stale approval,
  benign cleanup looks destructive.
- Anti-goals: no approval inference from vague summaries and no unpermitted
  alternative injection.
- API pressure: structured tool-call frames, severity, dedupe, cooldown, and
  explicit `can_interrupt`.

### 14. Secret and PII exposure watcher

- User story: agents handling logs, tickets, screenshots, or database rows may
  expose credentials or private content.
- Desired behavior: detect sensitive content in prompts, tool outputs, generated
  files, and outbound messages; minimally log redacted findings.
- Frame needs: actual text, source/destination channel, classification,
  org/user boundary, tool output body, attachments, outbound preview.
- Edge cases: over-redaction prevents detection, test strings cause noise,
  split tokens evade matching, observer logs leak content.
- Anti-goals: do not store raw secrets, rewrite content silently, or scan
  unrelated sessions.
- API pressure: configurable redaction views and safe logging of hashes, labels,
  and locations.

### 15. Runaway loop and budget watchdog

- User story: an agent repeats failed commands, re-reads files, asks equivalent
  questions, or creates self-feedback.
- Desired behavior: track repeated patterns, cost growth, unchanged outputs, and
  causal chains; alert only after thresholded evidence.
- Frame needs: event type, normalized action fingerprint, parent event ID, tool
  status, cost counters, retry count, injection source, session causal chain.
- Edge cases: legitimate long searches, semantic repetitions missed, observer
  feedback creating another loop.
- Anti-goals: not policing every repeat, not terminating without authority, not
  frequent progress nags.
- API pressure: causal IDs, observer-origin markers, rate limits, max injections,
  dedupe keys, and observe-only mode.

### 16. Production incident drift monitor

- User story: during an incident, agents may chase speculative fixes, lose the
  timeline, or diverge from the incident commander's plan.
- Desired behavior: flag missing owner/timeline/severity, untested assumptions,
  unsafe production actions, or plan drift.
- Frame needs: incident ID, severity, objective, command outputs, hypotheses,
  mitigation status, owner, timestamps, communication destination, authority.
- Edge cases: fast-changing context, stale frames, conflicting commanders,
  warning fatigue.
- Anti-goals: not incident commander, not generating status updates unless
  authorized, not blocking urgent mitigations from partial context.
- API pressure: freshness timestamps and a human-alert route separate from agent
  injection.

### 17. Stale context and assumption detector

- User story: agents rely on old docs, old test output, cached plans, or previous
  assumptions after state changed.
- Desired behavior: compare claims against source freshness and newer conflicting
  evidence; flag stale decisions.
- Frame needs: claim text, referenced artifact, artifact version/time, newer
  event IDs, command execution time, repo SHA, external data retrieval time.
- Edge cases: unknown freshness, newer evidence not necessarily better, clock
  skew, noisy alerts.
- Anti-goals: not forcing every fact to be rechecked and not silently replacing
  context.
- API pressure: source provenance/version metadata and evidence-linked findings.

### 18. Authority boundary monitor

- User story: a side agent or worker starts deciding outside its mandate,
  touching unauthorized files, contacting users, or changing scope.
- Desired behavior: enforce declared boundaries such as observe-only, read-only,
  edit-allowed, external-send-allowed, and prod-allowed.
- Frame needs: role, granted capabilities, task scope, tool args, target path or
  system, external recipient, permission decision, delegation chain.
- Edge cases: ambiguous scope, shell scripts hiding writes, broad tool surfaces,
  unclear inherited permissions.
- Anti-goals: no inferred permissions and no conflating read access with write
  authority.
- API pressure: machine-readable capabilities on frames and a single escalation
  path for violations.

### 19. Deployment and rollback sentinel

- User story: an agent deploys a service, migration, feature flag, or config and
  must watch rollout health without overreacting.
- Desired behavior: track deploy phases, health checks, metrics, migration
  output, and rollback criteria.
- Frame needs: target, version/SHA, environment, phase, check results, metric
  snippets, rollback plan, threshold policy, time window.
- Edge cases: metric lag, redacted service names, canary-check spam, destructive
  rollback.
- Anti-goals: do not run rollback, invent thresholds, or treat one failed probe
  as an incident without policy.
- API pressure: structured phase/check data and rate-limited state transitions.

### 20. Supply chain and unsafe command monitor

- User story: agents install packages, curl scripts, run build tools, or execute
  generated commands that may compromise the host.
- Desired behavior: flag unpinned installs, remote scripts, suspicious shell
  expansions, privilege escalation, unexpected endpoints, and untrusted command
  provenance.
- Frame needs: full command, working directory, environment, URL, package
  version, lockfile diff, command source, stdout/stderr, exit code.
- Edge cases: legitimate bootstrap scripts, hidden transitive package changes,
  split command frames, redacted URLs.
- Anti-goals: not banning all network access, not rewriting commands silently,
  not regex-only detection.
- API pressure: exact command/provenance frames and pre-execution warning points.

### 21. Conflicting agent action detector

- User story: multiple agents may duplicate, overwrite, or contradict work on
  the same repository, incident, branch, calendar, or infrastructure target.
- Desired behavior: alert when two sessions plan incompatible writes, duplicate
  messages, competing deployments, or divergent incident actions.
- Frame needs: session ID, target resource, operation intent, ownership/lock
  state, branch/SHA, paths, external recipient, planned execution time.
- Edge cases: read-only work should not alert, broad globs overmatch, ownership
  expiry, stale conflict alerts.
- Anti-goals: not global scheduling, not serializing harmless work, not leaking
  sensitive content across agents.
- API pressure: structured resource keys, scoped visibility, TTLs, and minimal
  conflict disclosures.

### 22. Human escalation quality gate

- User story: agents decide whether to interrupt humans during incidents,
  compliance risk, security events, or high-cost operations.
- Desired behavior: validate severity, evidence, affected system, recommended
  decision, deadline, and consequence of ignoring; suppress duplicates.
- Frame needs: proposed alert, evidence frame IDs, severity, target channel,
  dedupe key, previous alerts, time sensitivity, allowed communication mode.
- Edge cases: alert storms, stale alerts after mitigation, unclear ownership,
  private data in alert text, route outage.
- Anti-goals: no speculative paging, no hiding critical alerts solely due to
  rate limits, no raw secrets.
- API pressure: alert output needs severity, dedupe, cooldown, redaction,
  escalation policy, and delivery status.

### 23. Forensic audit and evidence preservation

- User story: after a failed deployment or bad agent action, responders need a
  reliable timeline without unnecessary sensitive content.
- Desired behavior: record tamper-evident summaries, hashes, decisions, tool
  calls, approvals, and redacted excerpts.
- Frame needs: event ID, parent ID, timestamp, actor, tool call/result, content
  hash, redaction label, approval metadata, side effects, session config.
- Edge cases: privacy violations from full capture, summary omissions, clock
  skew, observer downtime gaps.
- Anti-goals: not full transcript mirroring and not agent-editable audit trails.
- API pressure: append-only logging, redaction-aware hashes, retention classes,
  and observer health/gap markers.

### 24. Recovery handoff and liveness observer

- User story: a long-running agent stalls, crashes, loses tool access, or exceeds
  a time budget during work that needs continuity.
- Desired behavior: detect liveness failure, capture objective, last safe state,
  unresolved decisions, blocked tools, and escalation target.
- Frame needs: heartbeats, current plan, last successful action, pending tool
  call, error state, elapsed time, owner, checkpoint, artifacts touched.
- Edge cases: slow tools look dead, partial outputs mislead, stale checkpoints
  restart unsafe work, repeated handoffs spam humans.
- Anti-goals: no unauthorized resume/fork and no claiming completion from
  partial evidence.
- API pressure: lifecycle/heartbeat frames, checkpoint summaries, observer
  health, freshness, and confidence fields.

## Human collaboration, UX, and attention lens

### 25. Interruption gatekeeper

- User story: a human supervising long-running agents wants only
  decision-worthy interruptions.
- Desired behavior: watch blockers, irreversible actions, ambiguity, cost growth,
  or confidence drops; notify only when a human decision matters.
- Frame needs: goal, plan, recent tool calls/outputs, proposed next action, risk,
  confidence, elapsed time, retry count, injection permission.
- Edge cases: over-alerting, under-alerting, unverifiable alerts, repeated
  observer loops.
- Anti-goals: no step narration, harmless second-guessing, or replacing the main
  agent's judgment.
- API pressure: explicit notification thresholds, silent defaults, and enough
  content to justify interruption.

### 26. Drift watcher

- User story: a user wants the system to notice when the agent drifts from
  detailed instructions before trust or time is wasted.
- Desired behavior: compare work to original request, constraints, prohibitions,
  and later corrections; flag meaningful divergence.
- Frame needs: original prompt, active instructions, plan, tool outputs, user
  corrections, touched resources, intervention policy.
- Edge cases: valid detours, stale instructions after user changes direction,
  insufficient content to distinguish drift.
- Anti-goals: no style policing, no blocking exploration, no agent debates.
- API pressure: stable instruction snapshots plus event content.

### 27. Review shadow

- User story: a senior teammate wants an independent quiet reviewer without
  interrupting execution.
- Desired behavior: accumulate side-thread review notes and emit a final review
  grounded in observed actions and artifacts.
- Frame needs: diffs, commands, test results, error outputs, rationale,
  unresolved TODOs, files touched, severity.
- Edge cases: reviewing incomplete intermediate work as final, duplicate issues,
  missing context.
- Anti-goals: no real-time heckling, style nitpicks, or speculative blocking.
- API pressure: side-thread accumulation, final-review mode, and ordered events.

### 28. Human-readable progress brief

- User story: a busy operator wants concise status without reading logs.
- Desired behavior: periodically summarize what changed, what remains, risks,
  confidence, and next checkpoint.
- Frame needs: task goal, milestone state, recent actions, meaningful outputs,
  failures/retries, artifacts, remaining work, confidence, last summary time.
- Edge cases: vague summaries, noisy cadence, optimism bias, stale summaries.
- Anti-goals: no chatty narration, marketing tone, or audit-log replacement.
- API pressure: cadence controls and "since last summary" boundaries.

### 29. Stuckness detector

- User story: a human wants to know when an agent is looping, thrashing, or
  repeatedly failing.
- Desired behavior: detect repeated commands, errors, plan churn, tool failures,
  non-progress, or workaround behavior; suggest the smallest useful
  intervention.
- Frame needs: action sequence, command/tool inputs, outputs/errors, retry count,
  plan revisions, elapsed time, artifacts, success criteria.
- Edge cases: systematic work looks repetitive, long builds look stuck,
  premature interruption, thin output hides root cause.
- Anti-goals: no penalty for verification loops and no guessing from metadata.
- API pressure: repetition/progress metadata plus actual outputs before high
  confidence claims.

### 30. Accountability trail builder

- User story: a team lead wants a trustworthy account of what an agent did and
  why.
- Desired behavior: record decisions, approvals, side effects, assumptions, and
  evidence without joining the main workflow.
- Frame needs: instructions, reasoning summaries, tool calls/outputs, approvals,
  external writes, diffs, timestamps, actor IDs, confidence.
- Edge cases: overbroad sensitive logging, over-compression, inferred rationale
  mistaken as fact.
- Anti-goals: no surveillance theater, transcript duplication, or unsupported
  compliance claims.
- API pressure: provenance, actor identity, content excerpts, and redaction
  controls.

### 31. Team handoff packager

- User story: someone handing off an agent-run task wants a compact state package
  for a teammate entering midstream.
- Desired behavior: prepare status, constraints, completed work, changed
  artifacts, failed paths, risks, pending decisions, and safe next action.
- Frame needs: brief, milestones, artifacts, decisions, discarded approaches,
  blockers, working state, links/IDs, permissions.
- Edge cases: omitted rationale, too much noise, stale state after continued
  work, privacy exposure.
- Anti-goals: no generic summaries, raw transcript dumps, or false certainty.
- API pressure: observer-maintained state snapshots and explicit handoff boundary
  event IDs.

### 32. Consent and approval sentinel

- User story: a human wants a second process to watch for actions that need
  consent before they happen.
- Desired behavior: detect irreversible, external, expensive, privacy-sensitive,
  or user-visible actions and surface exact approval context.
- Frame needs: proposed action, target, reversibility, visibility, cost,
  sensitive data, prior permission, capability, timing.
- Edge cases: observer sees action too late, ambiguous prior consent, approval
  fatigue, inadequate prompt detail.
- Anti-goals: no blanket confirmations or observer approval on behalf of user.
- API pressure: pre-action events, action intent payloads, and intervention
  permission separate from commentary.

### 33. Coaching sidecar

- User story: a user improving agent or team practice wants coaching that does
  not derail the active task.
- Desired behavior: maintain private notes on planning, testing, communication,
  interruption hygiene, and missed collaboration moments; summarize later or
  nudge live only when allowed.
- Frame needs: plans, instructions, tool sequences, verification events, messages,
  errors, recoveries, outcome markers, commentary permission.
- Edge cases: subjective or patronizing advice, process over outcome, live
  distraction, vague evidence-free coaching.
- Anti-goals: no personality critique, micro-feedback, or one-methodology rule.
- API pressure: private commentary, delayed reports, optional live-nudge route.

### 34. Multi-agent attention router

- User story: one person overseeing several agents wants the right item at the
  right time instead of many noisy streams.
- Desired behavior: rank updates by urgency, reversibility, blocker state,
  decision need, and deadline; group related events and suppress duplicates.
- Frame needs: session IDs, goals, status, blockers, alerts, deadlines, risk,
  summaries, approvals, ownership, notification history.
- Edge cases: loud sessions starving quiet critical work, bad dedupe, stale
  cross-session state, unexplained priority.
- Anti-goals: not a universal notification bus or opaque priority scorer.
- API pressure: consistent status fields and notification history while keeping
  routing inspectable.

### 35. Stakeholder translator

- User story: non-implementing stakeholders want updates in their language
  without raw implementation detail.
- Desired behavior: translate activity into impact, risk, decisions, confidence,
  and validation evidence for product/support/ops/leadership audiences.
- Frame needs: objective, user impact, artifacts changed, decisions, risks,
  blockers, validation evidence, source links, audience.
- Edge cases: oversimplification, noisy stakeholder channels, fact mutation,
  sensitive details leaked.
- Anti-goals: no PR spin, executive theater, or replacement for technical review.
- API pressure: audience metadata, source-linked summaries, redaction/scope
  controls.

### 36. Feedback loop governor

- User story: an orchestration designer wants to prevent observer-agent chatter
  from becoming a runaway loop.
- Desired behavior: detect repeated corrections, recursive summaries,
  conflicting advice, and unbounded escalation; recommend mute, batch, side-thread
  only, or escalation.
- Frame needs: source/target, route, injection permission, message content,
  correlation IDs, reply chains, observer identities, suppression state.
- Edge cases: necessary debate mistaken for noise, hidden side threads, governor
  becoming noisy, contentless frames missing semantic repetition.
- Anti-goals: no ban on observer communication and no hidden interventions.
- API pressure: explicit routing, correlation IDs, content hashes/summaries, and
  simple mute/batch/side-thread/escalate controls.

## Platform, protocol, and API lens

### 37. Safety policy watcher

- User story: teams running agents over repos, tickets, and docs want policy
  issues flagged before harm.
- Desired behavior: inspect prompts, tool calls/results, and proposed outputs;
  emit structured violations and intervene only if allowed.
- Frame needs: event type, timestamp, sequence, actor, session/job ID, content,
  tool name/args, result summary, route destination, permission scope.
- Edge cases: redaction hides risk, late frames cannot prevent action, false
  positives, observer output leaks content.
- Anti-goals: not a policy engine baked into every agent and no silent rewrite of
  user intent.
- API pressure: content-bearing frames, redaction metadata, explicit permissions,
  and a narrow intervention primitive.

### 38. Cost and token budget watcher

- User story: organizations let agents spawn subagents and long-running searches
  and need budget control.
- Desired behavior: track token deltas, models, spawned sessions, tools, retries,
  and wall-clock time; warn at thresholds.
- Frame needs: session/parent IDs, sequence, model, token counts, tool cost,
  retry count, spawned job ID, elapsed time, budget, cumulative usage.
- Edge cases: late usage frames, inconsistent subagent reporting, replay
  double-counting, streaming increments.
- Anti-goals: not billing and not global resource scheduling.
- API pressure: monotonic event IDs, cumulative counters or idempotency keys,
  parent-child identity, replay markers.

### 39. Code review sidecar

- User story: a main agent implements while a sidecar catches risky edits early.
- Desired behavior: observe edits, commands, tests, and summaries; append notes to
  its own thread and emit structured report when needed.
- Frame needs: patch hunks, paths, command invocations, exit codes, stdout/stderr,
  tests, rationale, git status, final draft.
- Edge cases: huge diffs, generated files, lacking repo context, over-interrupting.
- Anti-goals: not a second implementer and not mandatory for trivial changes.
- API pressure: event filters by type/path, payload size controls, durable notes,
  report mode separate from injection.

### 40. User intent drift detector

- User story: user asks for a targeted fix and wants broad refactoring caught
  before it grows.
- Desired behavior: compare initial task, plans, actions, and edits against
  stated scope; warn on expansion or skipped constraints.
- Frame needs: original request, constraints, plan updates, edit summaries,
  command history, assumptions, status, actor, injection permission.
- Edge cases: legitimate discovered scope expansion, wording overfit, missing
  initial context.
- Anti-goals: not a semantic judge for all design choices.
- API pressure: immutable task snapshot and constraint bundle at observer setup.

### 41. Progress digest observer

- User story: users stepping away from long jobs want concise progress updates.
- Desired behavior: emit structured digests: changed work, remaining work,
  blockers, and input needed.
- Frame needs: plan state, task status, timestamps, command summaries, errors,
  file edit summaries, phase, blocked/retry state, input requests.
- Edge cases: noise, vague summaries, replay resending old updates, parallel job
  ordering.
- Anti-goals: not telemetry storage or a chatty second assistant.
- API pressure: filters, debounce/rate limits, sequence numbers, replay cursor,
  structured report channel.

### 42. Blocker and dependency watcher

- User story: in multi-agent runs, a coordinator needs dependency readiness and
  blocker signals.
- Desired behavior: observe job transitions, relationships, waits, failures, and
  completion reports; emit dependency status.
- Frame needs: job ID, parent ID, dependency IDs, event type, status transition,
  wait reason, command/test result, artifact refs, completion criteria.
- Edge cases: inconsistent blockers, partial completion, dependency cycles,
  out-of-order frames.
- Anti-goals: not a workflow engine and not responsible for all resolution.
- API pressure: job identity, parent-child links, status enums, ordering, and
  structured reports.

### 43. Audit trail recorder

- User story: regulated teams need to know why production configuration changed,
  who authorized it, which tools ran, and what evidence existed.
- Desired behavior: append significant events with content, permissions,
  approvals, tool calls, outputs, and replay/export support.
- Frame needs: immutable event ID, timestamp, actor/session, approvals, tool args,
  results, diffs, permission grants, redaction markers, replay cursor, hash.
- Edge cases: raw retention risk, partial traces, replay/live confusion, clock
  skew.
- Anti-goals: not unlimited transcript retention or analytics warehouse.
- API pressure: durability options, retention policy, redaction provenance,
  sequence IDs, replay boundaries.

### 44. Privacy boundary observer

- User story: agents using Slack, GitHub, web search, or email need checks before
  private content crosses a trust boundary.
- Desired behavior: inspect outbound calls/messages, classify destination trust,
  compare payload to privacy labels, and report or request approval.
- Frame needs: tool destination, org/workspace, payload, source refs, privacy
  labels, redaction state, approval state, actor, policy.
- Edge cases: derived sensitive facts, over-redaction, obscured destinations,
  report leaks.
- Anti-goals: not universal DLP and not inventing labels after the fact.
- API pressure: source/destination metadata, content labels, pre-send frames, and
  a "hold for approval" intervention.

### 45. Evaluation trace collector

- User story: platform teams need comparable traces for benchmark scoring without
  affecting behavior.
- Desired behavior: passively record task inputs, tools, artifacts, final answers,
  timings, and failures; never inject.
- Frame needs: task ID, run ID, model version, prompt, tool calls/results,
  artifact refs, final output, timestamps, token usage, error type, replay/live.
- Edge cases: observer visibility perturbing behavior, high trace volume,
  deterministic ordering, privacy limits.
- Anti-goals: not online coaching and not improving benchmark runs live.
- API pressure: passive read-only permission, normalized schemas, artifact refs,
  ordering, and invisible-to-target option.

### 46. Human escalation monitor

- User story: support agents need human escalation for anger, legal risk, or lack
  of authority.
- Desired behavior: observe ticket content, drafts, uncertainty, and tool failures;
  emit handoff packet only on criteria match.
- Frame needs: incoming/customer messages, drafts, uncertainty markers, policy
  tags, tool errors, account/ticket metadata, prior escalation state, route.
- Edge cases: missed tone, alert spam, private data in packets, agent continues
  after escalation.
- Anti-goals: not assignment system and not sentiment-only classification.
- API pressure: route-specific permissions, dedupe keys, privacy-aware reports,
  optional request-pause.

### 47. Loop and thrash detector

- User story: agents retry same failing test, regenerate same patch, or ask
  equivalent questions.
- Desired behavior: identify repeated failed actions with no new evidence and
  report stuck pattern plus last distinct progress.
- Frame needs: command fingerprint/result, exit code, stderr summary, patch
  fingerprint, plan version, retry count, timestamps, token/cost deltas.
- Edge cases: legitimate retries, hashes missing semantics, truncated output
  hiding differences, premature intervention.
- Anti-goals: not a generic timeout and not a retry ban.
- API pressure: compact fingerprints plus excerpts, retry metadata, rate-limited
  stuck reports, opt-in narrow intervention.

### 48. Artifact freshness observer

- User story: agents produce docs, patches, screenshots, and logs; final answers
  should reference current, accessible artifacts from the right run.
- Desired behavior: track artifact lifecycle and final-answer references; report
  stale links, missing files, failed screenshots, or unsupported claims.
- Frame needs: artifact ID/path/URI, producer session, creation/modification time,
  content hash, generating command, final text, reference target, availability.
- Edge cases: moves/regeneration under same name, claims without artifacts,
  expiring links, expensive full-content observation.
- Anti-goals: not document management and not semantic verification of every
  sentence.
- API pressure: artifact lifecycle frames, stable IDs, final-response frames,
  hashes, and metadata-only filters.

## Missing use case added after review

### 49. Memory and context injection sidecar

- User story: a user wants a sidecar to notice durable decisions, constraints,
  facts, warnings, or project memories and feed relevant reminders back into the
  main session when they matter.
- Desired behavior: observe activity, propose or emit scoped context notes, and
  optionally inject a visible, source-linked reminder into the main session at a
  safe boundary. The specific sidecar policy decides whether the note is a
  gentle reminder, a hard warning, a project memory, a stale-fact correction, or
  nothing.
- Frame needs: observed message/tool/artifact content, source event IDs, original
  instruction snapshots, current task scope, memory type, freshness, confidence,
  invalidation/TTL, target route, injection permission.
- Edge cases: stale facts, hidden prompt mutation, prompt-injection persistence,
  memory overriding user/system/developer instructions, repeated reminders,
  unclear source of truth, project-global memories leaking across tasks.
- Anti-goals: the platform should not silently mutate the main agent's hidden
  context, decide memory policy for all sidecars, or make remembered content more
  authoritative than current instructions.
- API pressure: memory sidecars are possible if the platform provides visible
  routed messages, source references, provenance, scoping, rate limits, and
  permissioned injection. The platform does not need a special memory primitive.

## Canonical use-case families

The 49 cards reduce to a smaller set of recurring families:

1. Summarize: progress digest, handoff, stakeholder translation, runbook capture.
2. Review: code review, quality auditor, artifact freshness, final answer checks.
3. Gate: destructive action, approval, consent, privacy boundary, compliance.
4. Detect risk: secrets, unsafe commands, stale context, drift, incident drift.
5. Detect non-progress: stuckness, loops, budget runaway, liveness failures.
6. Coordinate: dependencies, conflicts, multi-agent attention, SLA/handoff.
7. Preserve evidence: audit, incident scribe, evaluation trace, accountability.
8. Inject context: memory/context sidecar, coaching nudges, direct alerts.

Those are use-case policies. They do not require one platform primitive per
family.

## API design synthesis

### The platform substrate

The smallest useful substrate appears to be:

1. `delegate`: create a durable sidecar actor.
2. `job_watch`: subscribe to typed events or output from a concrete job or the
   caller session and deliver content-bearing frames to a target sidecar.
3. `communicate`: allow a sidecar to publish a report, note, alert, or context
   message to its own thread, and possibly to the caller when explicitly routed
   and permitted.
4. `delegate_send`: owner-to-delegate messaging and resume by `delegate_id`.

The research does not justify a separate `observer_create`, `observer_comment`,
`observer_memory`, `observer_alert`, or per-use-case tool. A sidecar is just a
delegate plus watch delivery plus a reporting route.

### Watch frame contract

Every delivered frame should have an envelope:

- `frame_id` or `delivery_id`
- `watch_id`
- `observed_event_id`
- `event_kind`
- `timestamp`
- monotonic sequence number within the observed stream
- `live` versus `replay`
- target identity: `target`, `job_id` when applicable, `session_id`
- actor identity: session, delegate, tool call, and parent/child relation where
  applicable
- origin marker: user, assistant, tool, observer, system, replay
- routing/cause chain so observer-originated effects can be excluded or deduped
- redaction state and content-retention class

The payload should be a typed union, not prose-only:

- message payload: role/source, visible text, optional structured output
- tool call payload: tool name, arguments, destination, working directory,
  command text, permission/capability metadata
- tool result payload: status, stdout/stderr excerpts, exit code, output refs
- job lifecycle payload: status transition, reason, owner, parent job, transcript
  ref
- artifact payload: path/URI/ID, hash, producer, timestamps, availability
- diff payload: paths, hunks or refs, generated/noisy marker
- usage payload: model, token deltas, cumulative counters, cost/budget metadata
- approval/action-intent payload: proposed action, reversibility, blast radius,
  deadline, prior approval state

If a frame cannot include full content, it should still include a bounded
excerpt, a content hash, a redaction reason, and an artifact or transcript
reference when the observer is allowed to read more.

### Permissions and routes

Sidecar policy belongs to the sidecar, but the platform should make authority
plain and enforceable:

- `observe`: what streams and payload classes the sidecar may receive.
- `read_more`: whether a sidecar may read referenced job output/transcripts.
- `report_self`: write to its own sidecar thread.
- `alert_caller`: send a visible alert/report to the watched or parent session.
- `inject_caller`: send a visible advisory/context message that the main agent is
  expected to consider.
- `request_pause` or equivalent: ask the owner/human to pause or approve, if a
  future control path exists.

These are capabilities on route/configuration, not necessarily separate tools.
The important distinction is that reporting to the sidecar thread, alerting a
human/caller, and injecting into the main agent are not the same authority.

### Route shape hypothesis

The clearest split is:

- `delegate_send(to=<delegate_id>, ...)`: the owner controls a delegate
  conversation. It should not be the generic way for a sidecar to inject into its
  caller.
- `communicate(...)`: the sidecar reports. If cross-thread delivery is allowed,
  add route semantics as a parameter rather than a new primitive.

Possible research-level shape:

```text
communicate(
  message: "...",
  route: "self" | "caller",
  kind: "note" | "report" | "alert" | "context",
  references: [frame_id, artifact_id, job_id],
  dedupe_key: "...",
)
```

This is not an implementation decision. It records the direction suggested by
the use cases: caller-directed observer output should be visible, source-linked,
typed, dedupable, and permission-checked. It should not masquerade as ordinary
owner-to-delegate control.

### Loop controls

Any observer route that can affect the main session needs built-in loop
controls:

- observer-origin frames are marked and excluded by default from the same watch;
- max deliveries per watch and max injections per window;
- dedupe keys and cooldowns;
- latest-frame-wins while an observer is busy;
- safe-boundary delivery only;
- caller-visible diagnostic on hard delivery failure;
- replay markers so rehydration does not re-inject old advice;
- explicit clear/mute behavior by `watch_id`.

These controls are necessary for snide-commentary, memory/context injection,
progress digest, and escalation cases alike. They are not a special-case fix for
one scenario.

## What this means for "simplest API"

The simplest API is not the smallest number of fields. It is the fewest concepts
with enough structure that agents do not have to guess.

Recommended direction for the next design pass:

1. Do not add an observer-specific primitive yet.
2. Make watch frames content-bearing, typed, ordered, redacted, and
   source-linked.
3. Keep sidecars as delegates.
4. Treat sidecar output as `communicate` reports with explicit route/kind
   semantics, not as generic delegate control.
5. Restrict `delegate_send` to owner-to-delegate messaging by `delegate_id`, or
   at least stop using its caller alias as the preferred observer path.
6. Encode observer authority in route/config capability, not in sidecar prompts
   alone.
7. Keep memory/context injection as a sidecar policy enabled by visible
   source-linked routed messages, not as hidden prompt mutation.

Open questions for the spec:

1. Should `communicate(route="caller")` be added, or should caller reporting use
   a differently named parameter on existing `communicate` to avoid implying
   arbitrary cross-session messaging?
2. Is `request_pause` needed now, or should observers only alert and leave
   pausing to the user/main session?
3. What is the minimal content payload for caller `communicate` event frames:
   exact message text, bounded excerpt, structured output, or a read grant?
4. Should event filters support content predicates server-side, or should
   observers always receive frames and filter themselves?
5. What redaction policy should be applied before a watch frame is delivered to a
   sidecar with a different session identity?
