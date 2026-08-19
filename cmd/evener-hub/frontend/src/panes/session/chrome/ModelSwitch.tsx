// ModelSwitch: the mid-session model switch. The control itself - the label as
// the trigger, the shared catalog picker in a floating popover - is
// ModelSwitchTrigger, which the spawn pane renders too (issue #198); this binds
// it to a thread: which model is current, whether the wire allows a switch, and
// what a pick DOES.
//
// The launchable SET comes from threadsStore.listModels() (session-lifetime
// cached in the store, da1b43f85's own doc comment - a repeat call after the
// first is effectively free) since that's what's actually valid to switch THIS
// session to; mergeScopedCatalog enriches those bare provider/model pairs with
// the unscoped /api/models catalog's metadata, exactly the way ModelField.tsx's
// settings (unscoped) call site already does - so this reuses both the
// rendering AND the enrichment plumbing rather than duplicating either.
//
// reasoningEffortLevels/supportsReasoning need no special handling here:
// StatusRow already re-renders from the live ThreadModel on every store
// change, so once thread/model/changed lands (protocol/reducer.ts's own
// case, which updates the reasoning ladder alongside modelProvider/model),
// the existing ReasoningEffortControl picks up the new model's profile
// for free.
import { useCallback } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { type ModelCatalog, type ModelCatalogEntry, useToasts } from "../../../widgets";
import { fetchModelCatalog } from "../../../widgets/modelCatalog/catalogClient";
import { mergeScopedCatalog } from "../../../widgets/modelCatalog/scopedCatalog";
import { ModelSwitchTrigger } from "./ModelSwitchTrigger";
import { modelLabel } from "./statusFormat";

export interface ModelSwitchProps {
  sessionRef: string;
  model: ThreadModel;
}

export function ModelSwitch({ sessionRef, model }: ModelSwitchProps) {
  const toasts = useToasts();

  // changeModel is the ONLY gate, and it is the wire's own answer: the hub
  // advertises it for a cold exited thread as well as a live one (cmd/evener-hub/
  // app_threadread.go's pastEntryThread) and resumes the session behind
  // thread/model/set when it has to (app_model.go's setThreadModelWithResume),
  // so a user never has to know whether their session is "running" to pick its
  // next model. The turn-in-flight refusal stays where it belongs - the daemon
  // answers Conflict for a switch mid-turn (server/appwire_runtime.go's
  // handleAppThreadModelSet) and handlePick surfaces that as a toast. Gating on
  // a client-side turn predicate here made the client guess at an answer only
  // the daemon has, and it took the switch away from every cold session too.
  const disabled = !model.capabilities.changeModel;
  const currentModelLabel = modelLabel(model.modelProvider, model.model);

  const loadCatalog = useCallback(async (): Promise<ModelCatalog> => {
    const [scoped, enrichment] = await Promise.all([
      threadsStore.getState().listModels(),
      fetchModelCatalog().catch(() => null),
    ]);
    return mergeScopedCatalog(scoped.data, enrichment);
  }, []);

  async function handlePick(entry: ModelCatalogEntry): Promise<void> {
    try {
      await threadsStore.getState().setModel(sessionRef, entry.provider, entry.model);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change model", err));
    }
  }

  return (
    <ModelSwitchTrigger
      label={currentModelLabel}
      value={currentModelLabel}
      disabled={disabled}
      loadCatalog={loadCatalog}
      onPick={(entry) => void handlePick(entry)}
      data-testid="model-switch-trigger"
      valueTestId="model-switch-value"
    />
  );
}
