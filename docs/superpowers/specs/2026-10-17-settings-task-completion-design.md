# Settings task-completion design

## Intent

Make existing settings easier to find, understand and finish without another visual redesign. The independent user-panel review is archived in `.impeccable/critique/2026-10-17T21-01-49Z__frontend-src-panes-settings-settings-tsx-55f22dec.md`. Jesse chose confirmation before leaving an edited form and then delegated remaining choices while offline. This document records Bot's implementation choices, not additional user approvals.

## Outcomes

1. Search finds visible preference controls, including phone density, desktop notifications and desktop/mobile conversation layout. Keep page-level results and current navigation groups; do not create a new search product.
2. First-plugin empty states offer direct actions into Browse or Sources. Adding a compatible source offers browsing it. Explain catalog compatibility with existing documentation. Never install, enable, add a source or trust a repository automatically.
3. Show the scope before conversation controls, and distinguish project local overrides from session defaults in actions. Show actual inheritance excluding the current local layer; do not relabel an effective local value as inherited.
4. Same-context read failures and rejected writes preserve user input. Unaffected plugin controls remain usable, with targeted retries for failed reads. Concurrent source refreshes update untouched forms; edited forms require an explicit conflict resolution. An uncertain write must be reconciled, never retried automatically.
5. Before an intentional host/project/section switch or Settings close would discard an edited form, offer Stay or Discard changes. Stay preserves the original route/context and draft; Discard performs only the originally requested navigation. No persistent/per-context draft cache. Automatic host registration replacement must never reassign a draft or mutation to another registration. Browser refresh/close may use the browser's native unsaved-work protection.
6. Notification controls distinguish saved preference from permission availability, show blocked/unsupported/request-failure status and recovery guidance, and never repeatedly prompt after a resolved permission choice.
7. Correct primary button foreground/background contrast for normal text in existing themes without adding widget APIs. Keep the current visual language and test shared consumers. Shorten overly long settings explanations where meaning can be preserved.

## Boundaries

Use existing widget APIs, tokens, route/store ownership and request fences. No new widget or widget prop. Preserve browser-local versus hub-default persistence, selected-directory repository discovery, reviewed-hash trust, launch precedence, explicit-empty semantics, host registration identity, independent Desktop/Mobile defaults and browser overrides. No live-hub restart or real preference mutations. No new recommended marketplace or new dependency.

Dirty-form protection covers explicit draft editors in Settings, including launch/session/project configuration, source editing/addition, directory/MCP/credential/agent instructions forms where leaving discards unsaved input, and pending shortcut capture. Autosaved controls do not become dirty forms. Integrate with existing navigation rather than a global application draft framework. Use one narrow reusable registration/confirmation boundary if no existing guard exists. A draft remains editable through temporary errors, but write authority must still follow fresh capability/host state.

## Evidence and acceptance

Regressions must fail before implementation and assert routing, retained input, requests, permissions and state rather than copy. Test read failure, definite write rejection, uncertain outcome/reconciliation, same-context reconnect, concurrent external edit, deliberate switch and registration replacement separately. Search tests assert destinations for user queries, not literal rendered copy. First-plugin tests traverse real components with the fake transport boundary and verify no implicit mutation.

Run targeted tests per task, TypeScript and Biome, then full `make test-web` and browser guards serially. One batched desktop/mobile visual review plus at most one confirmation pass. Independent task reviews and a whole-branch review follow. Keep the branch committed and report any remaining issue explicitly. Publishing/merging follows the repository's standing PR rules only after verification; never deploy the live hub as a side effect.
