# Agent Model Selection Correctness Design

Date: 2026-07-15
Status: Approved
Tracker: #20
Program order: Project 4 of 6; implement after the budget, transcript/API-log,
and job-supervision projects.

## Purpose

The agent that creates or resumes a delegate may choose the model family. Serf's
job is to make that choice work or reject it before creating partial child state.
Serf must not impose a routing policy or silently substitute a different model.

This design completes and verifies the existing delegate model-selection path.
It does not replace the broader, already-specced user-facing mid-session model
switching work.

## Decisions

### Agent discretion

The parent agent may request any provider/model reference available through the
session's configured model catalog. Serf does not prefer a family, implement an
escalation ladder, or route by task risk.

Omitting the delegate model retains the documented parent-model inheritance.
Providing a model is an explicit selection and must never silently collapse back
to inheritance.

### Validate before durable creation

Serf validates an explicit delegate model before minting or persisting any:

- child session;
- delegate handle or job;
- managed worktree;
- transcript or metadata file;
- notification/watch state.

Validation resolves:

- configured provider instance;
- canonical requested model;
- catalog visibility/availability;
- required tool-calling capability for an agent delegate;
- requested reasoning-effort support or the existing documented clamp behavior.

Validation may use the configured/static catalog and the provider's normal model
enumeration path. It must not issue a paid completion as a probe.

If availability enumeration cannot complete, explicit selection fails with a
typed, actionable `model_availability_unverified` error. It does not create the
child optimistically and fail on its first real turn.

### Cross-family operation

Cross-provider and cross-family delegate choices are supported when the resolved
model has the required capabilities. Provider-specific tools, request projection,
reasoning settings, and history handling are constructed from the selected child
profile rather than copied blindly from the parent.

The child receives one coherent provider/model for its entire initial turn.
Configured fallbacks may change an individual provider attempt through the
existing fallback mechanism, but are not silent child-profile substitution.

### Explicit fallbacks only

Only fallbacks present in effective Serf configuration may run. Every fallback:

- is recorded as a distinct API attempt in the same logical attempt group;
- exposes requested and actual provider/model metadata;
- preserves the terminal error when all configured options fail.

Serf does not invent a same-family or cross-family fallback based on model name,
price, latency, or perceived task difficulty.

### Durable provenance

The existing delegate restore/job metadata remains authoritative and must retain:

- raw requested model reference;
- resolved provider/profile ID;
- resolved requested model;
- actual provider/model per produced turn and API attempt;
- reasoning effort;
- configured fallback outcome when used.

Restore uses the frozen resolved child profile. If that profile is no longer
configured or available, resume fails clearly without selecting a replacement.

### Mid-session switching

The existing mid-session model-switching design remains the contract:

- validated switching at turn boundaries;
- no mid-tool-loop user/agent switch;
- persisted model provenance;
- no partial mutation when validation fails.

This project may fix defects required for delegate-selected models to interoperate
with that path, but must not redesign its UI or protocol.

## Errors

Use typed errors that distinguish:

- unknown provider instance;
- unknown or unavailable model;
- availability could not be verified;
- missing required capability;
- invalid reasoning effort;
- restored model no longer available.

Errors include configured alternatives where safe and useful. They do not expose
credentials or create durable child residue.

## Testing

Use fake catalogs/providers and real delegate creation plumbing.

Cover:

- omitted model inherits the current parent model;
- explicit same-family and cross-family models resolve correctly;
- unknown instance/model and catalog failure create no child/job/worktree/files;
- a model without tool-calling capability is rejected;
- explicit model is not silently replaced by the parent model;
- configured fallback attempts are recorded with requested/actual provenance;
- restore preserves the frozen model or fails clearly when unavailable;
- parent and child provider-specific tool/request configuration do not leak
  across profiles;
- existing turn-boundary switching tests remain green.

Add one opt-in live cross-family delegate scenario only if it exercises provider
behavior that scripted catalogs cannot. Provider credentials alone must not run
it.

## Scope Lock

This spec does not:

- add a model-family preference or automatic escalation policy;
- modify Superpowers prompts, skills, or plans;
- probe availability with paid completions;
- silently fail open when explicit availability cannot be checked;
- add compatibility behavior for invalid historical model references;
- redesign the Web/TUI model picker or the existing switching protocol.
