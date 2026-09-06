# Existing-session model switching

Extend the conversation service with a model catalog read scoped to the successfully bound thread’s source and cwd. Scope is cleared with the existing open/close epoch; reject catalog completions from obsolete bindings. Keep model mutation capability checks in the shared service.

The composer exposes the current model and reasoning as compact, independently tappable footer controls. The model control opens a searchable native modal; reasoning opens a short choice sheet. Session management no longer owns either setting. Show the current model label, provider identity for each result, loading/empty/failure states and catalog diagnostics. Preserve the draft and block sending while a settings action is pending. Choosing a model row stages the choice; an explicit Use model action applies it. Session-owned controls serialize model changes with rename/compact/reasoning/stop, validate the chosen provider/model against the loaded catalog, refresh the authoritative session afterward and never automatically replay a failed mutation. A new binding clears the catalog and selection.

Reuse native typography, radio controls and platform modal behavior. A virtualized list avoids laying out an entire provider catalog. Keep work on model choice separate from vision-model selection, which remains tracked in coverage.

Verify scoped requests, unavailable/stale catalogs, wrong provider/model pairs, duplicate mutations and failed responses at the real shared service boundary. Run native/shared tests and TypeScript, build both Release apps and manually switch models through the isolated real daemon on both simulators. Record exact verification limits and screenshots.
