# Provider connection design study

Design exploration for Jesse, 2026-09-10. Production behavior is unchanged.
Source baseline: `2664cc881d128d8b0c4a0af96683126e70d13cd7`.

## Brief

A first-time user spent about 20 minutes trying to complete the full provider form and would have abandoned setup. The cited document describes configuration internals rather than a usable setup journey. Explore six genuinely different interaction patterns, get independent agent critiques, revise, and present options before implementation.

## Design invariants

- Choose a recognizable provider first. Show a short curated group, then searchable all-providers and local/custom paths. The shortlist is a design hypothesis, not measured popularity.
- For a standard API-key provider, the only required entry is the key. Give an explicit key-creation link and explain API billing near that link.
- For OpenAI, distinguish ChatGPT sign-in from API-key access. Do not imply a Claude or Gemini subscription can be used as API credentials.
- Use existing implicit provider instances for standard setup. Additional named connections and endpoint overrides belong to an explicit advanced/custom route.
- Hide optional technical fields. Required cloud project, region, resource, endpoint, or credential inputs must remain visible in their relevant provider-specific route.
- One continuous flow owns save, check, repair, and return to the originating task. Saving credentials alone does not mean a connection works.
- Explain that the existing check requests the model list and does not generate a response. A generation test would be separate new behavior. Keep saved-but-unchecked, authorization failure, endpoint failure, and unsupported-check states distinct.
- Never collect real credentials in a mockup. Outcomes are explicitly simulated and nothing is persisted or sent to a provider.

## Visual plan

Keep Evener's existing dark neutral surfaces and blue selection/focus language from `cmd/evener-hub/frontend/src/styles/tokens.css`: page #17181A, pane #1C1D1F, card #232427, edge #3A3C40, text #F2F3F4, accent #3D9AFF. UI uses the existing Inter/system-sans stack; small configuration examples use a system monospace stack. The gallery can use a larger title scale, while product screens stay restrained.

The signature is deliberate absence: a whole provider connection reduced to one explained input. Space makes requiredness obvious. Provider logos are simple textual marks, not external assets. No decorative gradients or unrelated rebrand.

## Six revised directions

| Direction | Interaction difference | Best context | Cost or risk |
|---|---|---|---|
| A. Quick connect | Compact provider picker replaces itself with one credential step | Universal default from settings and launch | A screen transition hides the directory |
| B. Inline cards | Chosen provider expands in the settings page | Add another provider | Page grows and can become busy |
| C. Guided setup | Full-page journey with a persistent progress rail | First-run onboarding | Too much ceremony for repeat setup |
| D. Model first | Choose the desired model family, then connect an explicit provider | Empty model picker | Model availability and routing can be ambiguous |
| E. Access first | Start from ChatGPT sign-in, an API key, or a local/company endpoint | Users who know what access they have | Asks about authentication before provider identity |
| F. Provider directory | Searchable master list with a stable detail panel | Settings, frequent provider changes | More on-screen structure for a beginner |

All six share the same credential and outcome design so comparison tests navigation rather than six different levels of form quality. Each has a distinct initial screen and a complete illustrative key path. OAuth, cloud, local/custom, advanced, and failure states are inspectable. These are design prototypes, not an Evener implementation.

## Panel review

Three independent reviewers examined the initial designs through first-run comprehension, authentication truth, and accessibility lenses. Their rankings and every finding’s disposition are in [reviews.md](reviews.md). The original designs are preserved in commit `5ccfbd8ce`.

**Recommendation:** compare A, Quick connect, as the shared flow with C, Guided setup, for first run. Two panelists favored A; the first-run panelist favored C. All six alternatives remain available. Agent critique is heuristic review, not observed usability research.

## Open the study

From the repository root:

```sh
python3 -m http.server 43127 --bind 127.0.0.1 --directory docs/design/provider-connection-study
```

Open <http://127.0.0.1:43127/>. The gallery links to every initial screen and credential step. Select a simulated outcome in the toolbar, choose a provider, and use **Fill demo details**. Never enter real credentials. Closing or resetting the prototype discards its in-memory draft; it does not use browser storage or provider APIs.

- [Research and source audit](research.md)
- [Panel findings and revisions](reviews.md)
- [Proposed user instructions](instructions.md)
- [Screenshots and verification report](screenshots/verification.json)
- [Independent panel recheck: 41/41 passed](screenshots/panel-verification.json)

## Reproduce design verification

Use an installed Playwright package and Google Chrome. Dependencies need not be added to Evener. `--playwright` names the installed package’s `package.json`; `CHROME_PATH` can select another Chrome executable.

```sh
node docs/design/provider-connection-study/verify.mjs --help
node docs/design/provider-connection-study/verify.mjs \
  --url http://127.0.0.1:43127/ \
  --output docs/design/provider-connection-study/screenshots \
  --playwright "$PLAYWRIGHT_PACKAGE"
```

The verifier exercises the rendered six desktop/mobile prototypes, captures their screens, and writes `verification.json`. It includes panel regression checks and rejects unexpected external requests or browser errors. The initial failure runs established the defects before repair. Existing registry filtering was independently checked with:

```sh
go test ./llm/registry -run 'TestInstances_(ImplicitFromEnv|PseudoProviderNeedsBaseURL)$' -count=1 -v
```

These are design-artifact checks. Production source, dependencies, and integration are unchanged; production gates and real authentication are outside this study. The full-editor handoff lists retained existing capabilities rather than rebuilding the editor. Actual credential resolution, persistence, OAuth, and model catalog integration would require separately approved implementation work.
