// ModelSwitch: the mid-session model switch. The control itself - the label as
// the trigger, the shared catalog picker in a floating popover - is
// ModelSwitchTrigger, which the spawn pane renders too (issue #198); this binds
// it to a thread: which model is current, whether the wire allows a switch, and
// what a pick DOES.
//
// The launchable SET and its metadata come from threadsStore.listModels()
// (session-lifetime cached in the store, so a repeat call after the first is
// effectively free) since that's what's actually valid to switch THIS session
// to. The hub's typed model/list response is already the shared catalog.
//
// reasoningEffortLevels/supportsReasoning need no special handling here:
// StatusRow already re-renders from the live ThreadModel on every store
// change, so once thread/model/changed lands (appwire-client/typescript/
// reducer.ts's own case, which updates the reasoning ladder alongside
// modelProvider/model), the existing ReasoningEffortControl picks up the new
// model's profile for free.

import type { ThreadModel } from "@evener/appwire-client";
import { sessionActionError } from "@evener/appwire-client";
import { useCallback, useEffect, useState } from "react";
import { onConnectionNotification } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import { type ModelCatalog, type ModelCatalogEntry, useToasts } from "../../../widgets";
import { modelListToCatalog } from "../../../widgets/modelCatalog/catalogClient";
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
  // next model. A pick made while a turn runs is accepted and queued by the
  // session for the turn boundary (agent/session.go's SetModel queue path), so
  // the next turn - typically the message being typed - runs on it. A genuinely
  // refused pick (unknown model, unresolvable ref) still surfaces as a toast
  // from handlePick. Gating on a client-side turn predicate here made the client
  // guess at an answer only the daemon has, and it took the switch away from
  // every cold session too.
  const disabled = !model.capabilities.changeModel;
  const currentModelLabel = modelLabel(model.modelProvider, model.model);
  const [acceptedModel, setAcceptedModel] = useState<{
    sessionRef: string;
    label: string;
    previousLabel: string;
  } | null>(null);
  const displayedModelLabel =
    acceptedModel?.sessionRef === sessionRef && acceptedModel.previousLabel === currentModelLabel
      ? acceptedModel.label
      : currentModelLabel;

  // The accepted label bridges the successful RPC and the authoritative
  // thread/model/changed notification. Any authoritative change settles it,
  // including a switch made from another client.
  useEffect(() => {
    if (
      acceptedModel !== null &&
      (acceptedModel.sessionRef !== sessionRef || acceptedModel.previousLabel !== currentModelLabel)
    ) {
      setAcceptedModel(null);
    }
  }, [acceptedModel, currentModelLabel, sessionRef]);

  const loadCatalog = useCallback(async (refresh?: boolean): Promise<ModelCatalog> => {
    return modelListToCatalog(await threadsStore.getState().listModels(refresh));
  }, []);

  // The hub announces a refreshed model list on evener/auth/updated (it serves
  // a stale list at once and refreshes it behind the request), so an open
  // picker re-reads it in place rather than showing the old list until it is
  // reopened. A queued switch can still fail history validation at the turn
  // boundary; its sourced warning restores the authoritative label.
  const [modelsRevision, setModelsRevision] = useState(0);
  useEffect(
    () =>
      onConnectionNotification((n) => {
        if (n.method === "evener/auth/updated") setModelsRevision((revision) => revision + 1);
        if (n.method === "warning" && n.params.ref === sessionRef && n.params.source === "model_switch") {
          setAcceptedModel(null);
        }
      }),
    [sessionRef],
  );

  async function handlePick(entry: ModelCatalogEntry): Promise<void> {
    try {
      await threadsStore.getState().setModel(sessionRef, entry.provider, entry.model);
      setAcceptedModel({
        sessionRef,
        label: modelLabel(entry.provider, entry.model),
        previousLabel: currentModelLabel,
      });
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't change model", err));
    }
  }

  return (
    <ModelSwitchTrigger
      label={displayedModelLabel}
      value={displayedModelLabel}
      disabled={disabled}
      loadCatalog={loadCatalog}
      refreshKey={modelsRevision}
      onPick={(entry) => void handlePick(entry)}
      data-testid="model-switch-trigger"
      valueTestId="model-switch-value"
    />
  );
}
