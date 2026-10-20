# Onboarding research and design review

## Audience and goal

Jesse wants a clear, simple, adult experience for developers new to Evener: run the binary, connect a provider in the web UI, and start useful work. The requested sequence is six independent simulated walkthroughs, workflow mockups, fresh-agent critiques over at most ten design iterations, implementation, and a screencast for Jesse before merge.

This is agent-assisted heuristic research, not a human focus group. No account credentials were read and no live provider requests were made during research.

## Six independent walkthroughs

| Perspective | Principal evidence | Interpretation |
| --- | --- | --- |
| Terminal-comfortable newcomer | `cmd/evener/main.go` calls Usage and exits 1 on an empty non-resume prompt; its usage leads with the noninteractive agent | Running the product name should provide a clear browser entry point |
| Developer bringing an API key | Getting-started sends users through Settings; Spawn's connector continues into a provider-filtered model chooser | Make the in-context connection route the primary first-run path |
| Developer bringing a chat subscription | Provider discovery lists names; billing and authentication explanation appears after selection | Explain API key versus Codex sign-in before the choice |
| Interrupted setup | Save precedes model-list checking, Cancel does not undo saved credentials, explicit retry paths exist | Preserve saved access and drafts, distinguish retry from replacement, label exits truthfully |
| Developer with an existing repository | The tutorial prioritizes a disposable Python fixture; existing-project guidance appears at the end | Give a short own-project route while retaining the verified tutorial |
| Keyboard user and cognitive-load review | Provider prerequisite follows the composer, a simultaneous missing-model notice appears, access is in Advanced options on desktop | Put the next required action first, remove redundant prerequisite noise, keep consequences visible |

Three scout reports deliberately returned source evidence without severity judgments. The priorities above are the parent reviewer's interpretation of that evidence, not six independent votes on a design. All six reviewed source; none performed a browser or live-provider test.

## Parent verification

Read `docs/getting-started.md`, `docs/connecting-a-provider.md`, the relevant subsystem map, and the current design-system guides. Independently inspected the empty-prompt branch in `cmd/evener/main.go` and `ProviderChoices` in `ProviderConnection.tsx`.

Rendered the current real Spawn component through the existing `spawnguard.html?onboarding=1` fixture, with its scripted AppWire client and a dead proxy destination rather than the live hub. At 1200×818 the browser showed:

- directory first, then the task composer with initial focus;
- a disabled Start and model selector before the provider prerequisite;
- Connect provider plus Retry provider check below the composer;
- a simultaneous no-default-model warning;
- plugin disclosure and Advanced options after these notices.

This validates rendered hierarchy for that fixture. It does not validate a clean installed hub, actual account setup, generation, first-run timings, or all provider rows. The fixture contains one provider and prepopulates a directory and plugins.

## Preserve

- Existing provider and credential ownership, host scoping, endpoint assertions, and credential secrecy.
- Model-list checks make no generation, quota, or model-capability promise.
- Provider connection never starts work or changes the global model default implicitly.
- Existing advanced/custom/cloud/local routes remain available.
- Explicit user selection of model, directory, access and task.
- Saved credentials survive check failures; a draft remains usable during in-app setup.
- Existing editorial visual system and widget APIs.
- Independent verification in the optional first-task tutorial.

## Boundaries requiring separate decisions

The friction guide records broader recovery work: unreadable credential-store startup, durable creation identity after uncertain Start, first-connection retry, and runtime credential refresh. These are not fixed by moving controls. Do not silently include or claim them as part of a visual onboarding change.

Jesse settled bare-command behavior: list all top-level commands and identify `hub` as the interactive UI entry point. Do not automatically start the hub or open a browser. The first mockup's shorter terminal guidance must be revised to reflect this.

No new widget or widget prop is authorized by this design. Any necessary widget API extension must be discussed before implementation.

## Design iteration ledger

### Iteration 1

Created `prototype.html` as a self-contained interactive workflow mockup. A fresh builder reviewer resolved an important research omission: `Welcome.tsx` already redirects a focused root page to New session when provider setup is missing and no session pane is open. The design now explicitly preserves that guarded behavior.

Two fresh reviewers evaluated the prototype independently: one exercised browser interaction and a 390×844 viewport, and one compared its contracts against current source. Material findings:

- Completing/dismissing the dialog lost focus to BODY, making the next Tab return to the preview toolbar.
- Blank Start incorrectly showed simulated active work instead of an empty session.
- Saved credentials after a failed check were treated as missing readiness, contrary to the existing configuration-readiness owner.
- A generic invented sandbox mode could not validate the promised effective-permission summary or advanced override precedence.
- A successful check still needs separate model-list loading, empty and failure handling.
- One fixture model tested navigation but provided no meaningful model-choice evaluation.

Browser scenarios that passed as simulations: API-key happy path, failed check/retry, saved dismissal, edited task preservation, configured bypass and 390px horizontal fit. These do not prove provider access, generation, real route restoration or comprehensive accessibility. The contract reviewer confirmed honest model-list-only claims, saved/cancel distinctions, qualified OAuth eligibility, and retained custom/remote boundaries.

### Iteration 2

Revised the prototype for Jesse's startup decision and the material findings above. The written design separates configuration from verification, describes catalogue recovery, uses real access modes and precedence, and requires meaningful focus restoration and honest blank-session behavior. Removed an invented unresolved-access launch restriction before publication.

Two fresh reviewers checked source contracts and browser interactions. Browser checks passed the earlier regressions: completion focused the task, Escape restored the model control, blank Start opened an idle composer, and failed/unsupported verification retained configuration and allowed explicit model choice. Edited task and directory survived. Independent catalogue loading/empty/error recovery, override precedence, unresolved access without a new launch gate, configured welcome, both themes and 390px horizontal fit passed as prototype interactions only.

Remaining material findings: healthy configured status dominated the task with a large repair block; catalogue recovery duplicated the same destination as Repair and Back; known permission modes besides Full access lacked consequences; existing provider/model escape affordances were not represented. The source review also flagged a stale diagram contradiction that had already been corrected by the parent during review: saved access after failed verification can continue explicitly to model choice.

Parent visually inspected the narrow ready-state capture and confirmed the oversized success block. Browser evidence: `/Users/jesse/Library/Caches/superpowers/browser/2026-10-18/session-1792303467746/010-eval.png`. These checks do not validate production widgets or live providers.

### Iteration 3

Refined healthy-state hierarchy, removed duplicate catalogue actions, explained each effective permission mode, and restored visible provider/model escape affordances as explicit prototype previews.

A fresh final reviewer found no material design issues. Browser checks exercised happy-path setup, failed/unsupported continuation, catalogue recovery, retained task/directory, model-completion and Escape focus, blank launch, configured welcome, permission overrides and unresolved states. Desktop and 390px layouts were inspected in both themes. The parent also inspected the final desktop screenshot. Evidence: `/Users/jesse/Library/Caches/superpowers/browser/2026-10-18/session-1792303717712/008-eval.png` and `016-eval.png`.

Stop after three iterations and present the design to Jesse. These are simulated workflow checks, not production implementation, live-provider tests or exhaustive accessibility validation. Implementation and the requested pre-merge screencast remain pending.

## Verification still needed

- Fresh-agent critique of the actual mockup and its alternative states.
- Jesse's approval of the workflow and consequential startup behavior.
- Test-first implementation and targeted frontend/Go checks.
- Real local producer/hub/client qualification with a scripted provider at the LLM boundary, not a mocked frontend end-to-end claim.
- Separately authorized live authentication/generation if needed for account-specific claims.
- Screencast of implemented flow, with private authorization material excluded, for review before merge.
