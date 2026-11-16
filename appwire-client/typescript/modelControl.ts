import type { ThreadModel } from "./model";
import { sameJsonValue } from "./plainObject";
import type { ModelControlAuthority, ModelControlSnapshot } from "./types.gen";

export type ModelControlCarrier =
  | { kind: "read"; version: number; snapshot?: ModelControlSnapshot }
  | { kind: "response" | "notification"; snapshot: ModelControlSnapshot };

export type ModelControlDecision = "applied" | "duplicate" | "ignored" | "invalidate";

export interface ModelControlRequestFence {
  authority: ModelControlAuthority;
  clientGeneration: number;
  readGeneration: number;
}

function sameAuthority(left: ModelControlAuthority, right: ModelControlAuthority): boolean {
  return sameModelControlAuthority(left, right);
}

export function sameModelControlAuthority(
  left: ModelControlAuthority | undefined,
  right: ModelControlAuthority | undefined,
): boolean {
  return (
    left !== undefined &&
    right !== undefined &&
    left.instanceId === right.instanceId &&
    left.bootGeneration === right.bootGeneration
  );
}

export function projectModelControlSnapshot(model: ThreadModel, snapshot: ModelControlSnapshot): ThreadModel {
  return {
    ...model,
    modelControlVersion: 1,
    modelControl: snapshot,
    modelProvider: snapshot.active.modelProvider,
    model: snapshot.active.model,
    reasoningEffortLevels: [...(snapshot.active.reasoningEffortLevels ?? [])],
    supportsReasoning: snapshot.active.supportsReasoning,
  };
}

function invalidateSnapshot(model: ThreadModel): ThreadModel {
  const { modelControl: _modelControl, ...withoutSnapshot } = model;
  return withoutSnapshot;
}

export function reduceModelControl(
  model: ThreadModel,
  carrier: ModelControlCarrier,
): { model: ThreadModel; decision: ModelControlDecision } {
  if (carrier.kind === "read") {
    if (carrier.version !== 1) {
      const { modelControlVersion: _version, modelControl: _snapshot, ...legacy } = model;
      return { model: legacy, decision: "applied" };
    }
    if (!carrier.snapshot) {
      return { model: invalidateSnapshot({ ...model, modelControlVersion: 1 }), decision: "invalidate" };
    }
    if (model.modelControlVersion !== 1 || !model.modelControl) {
      return { model: projectModelControlSnapshot(model, carrier.snapshot), decision: "applied" };
    }
    if (!sameAuthority(model.modelControl.authority, carrier.snapshot.authority)) {
      return { model: projectModelControlSnapshot(model, carrier.snapshot), decision: "applied" };
    }
    return reduceRevision(model, model.modelControl, carrier.snapshot);
  }

  if (model.modelControlVersion !== 1 || !model.modelControl) {
    return { model, decision: "ignored" };
  }
  const held = model.modelControl;
  const incoming = carrier.snapshot;
  if (!sameAuthority(held.authority, incoming.authority)) {
    return { model: invalidateSnapshot(model), decision: "invalidate" };
  }
  return reduceRevision(model, held, incoming);
}

function reduceRevision(
  model: ThreadModel,
  held: ModelControlSnapshot,
  incoming: ModelControlSnapshot,
): { model: ThreadModel; decision: ModelControlDecision } {
  if (incoming.revision < held.revision) return { model, decision: "ignored" };
  if (incoming.revision === held.revision) {
    return sameJsonValue(held, incoming)
      ? { model, decision: "duplicate" }
      : { model: invalidateSnapshot(model), decision: "invalidate" };
  }
  return { model: projectModelControlSnapshot(model, incoming), decision: "applied" };
}

export function modelControlAuthority(model: ThreadModel): ModelControlAuthority | undefined {
  return model.modelControlVersion === 1 ? model.modelControl?.authority : undefined;
}

export function modelControlDisplay(model: ThreadModel): { modelProvider: string; model: string } {
  if (model.modelControlVersion === 1 && model.modelControl) {
    const desired = model.modelControl.desired;
    if (desired?.lifecycle === "pending" || desired?.lifecycle === "applied") {
      return { modelProvider: desired.modelProvider, model: desired.model };
    }
    return {
      modelProvider: model.modelControl.active.modelProvider,
      model: model.modelControl.active.model,
    };
  }
  return { modelProvider: model.modelProvider, model: model.model };
}
