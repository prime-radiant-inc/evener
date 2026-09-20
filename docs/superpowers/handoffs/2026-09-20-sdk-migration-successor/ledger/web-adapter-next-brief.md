# Web adapter next-slice constraints

Prepare only after A4 transition extraction and package P10 API are qualified.
A5/A6 retained scope must replace real hub behavior; publishing another helper is not completion.

Source reread at A3 f84794a4 confirms production consumers depend on existing
stores/transcriptDisplay exports. Transcript.tsx and Session.tsx use Zustand useStore
with viewport/local/hub selectors; settings/transcript.tsx uses hub/drafts/local/loading/
errors/support and calls refreshHubDefaults and patchHubDefault from getState;
overflow harness uses setState plus setLocal/clearLocal. Preserve those API paths and
stable store identity. Existing Session/SessionChrome/settings test monkey-patches remain valid.

Keep browser local persistence and cross-tab handling as host behavior. Shared package
owns hub read/write lifecycle, fencing, defaults and previews. Effective transitions still
route through the web view registry. Read current stores/connection.ts for proven stable
snapshot/StoreApi semantics, but copy only what this adapter actually needs.

Retained A6 behavior is settled: superseded/fenced PATCH replies return current value,
support flap reloads, missed notifications trigger refresh, previews reconcile contradictions.
Keep package A8 checkpointed drafts and web A9 gating separate from this adapter slice.
Do not migrate native or expand import paths. No implementation assigned from this note yet.
