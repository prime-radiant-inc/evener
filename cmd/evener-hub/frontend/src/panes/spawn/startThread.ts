// startThread is the canonical launch seam: it starts a real session over
// AppWire "thread/start" and returns the created thread's ref.
import type { AppwireClientLike, ComposerMention, LaunchConfigLayer, ThreadStartParams } from "@evener/appwire-client";
import {
  buildComposerInput,
  errorKind,
  friendlyLaunchErrorMessage,
  isSocketDroppedError,
  mutationErrorData,
  RequestTimeoutError,
  WireError,
} from "@evener/appwire-client";
import type { InputAttachment } from "../../stores/threads";
import { mergeAccessModeSandbox } from "./accessMode";

export interface SpawnRequest {
  cwd: string; // required (ThreadStartParams.cwd)
  prompt: string; // RAW, untrimmed - floor §1.12 (bar the marker translation startThread applies)
  attachments?: InputAttachment[];
  skillNames?: readonly string[];
  commandNames?: readonly string[];
  mentions?: readonly ComposerMention[];
  harness?: string;
  modelProvider?: string; // evener-model harness: "<provider>/<model>" split -> provider half
  model?: string; // model id (bare id for a non-evener harness - floor §1.4)
  reasoningEffort?: string; // wire camelCase - floor §1.11
  // branch is DISPLAY-ONLY (floor §1.7): the branch chip shows the resolved
  // HEAD ref, but typed AppWire launch params have nowhere to carry it.
  // Accepted here so callers can pass the form value uniformly; startThread
  // never sends it.
  branch?: string;
  accessMode?: string; // merged -> launchOverrides.sandbox unless schema set it (floor §1.8)
  launchOverrides?: LaunchConfigLayer;
  // Explicit launch source id (Component 06a). "local" is the default and is
  // OMITTED from the wire so a local spawn stays byte-identical to before the
  // host picker existed; any other value names the host/source to target.
  source?: string;
  // Start the session as a Manager. A start-time property carried by
  // ThreadStartParams.manager; false is OMITTED from the wire so an ordinary
  // launch stays byte-identical to before this control existed.
  manager?: boolean;
  clientMutationId?: string;
}

export interface SpawnResult {
  ref: string;
}

function threadStartMeta(req: SpawnRequest): Omit<ThreadStartParams, "input" | "clientMutationId"> {
  const meta: Omit<ThreadStartParams, "input" | "clientMutationId"> = { cwd: req.cwd };
  if (req.harness) meta.harness = req.harness;
  if (req.modelProvider) meta.modelProvider = req.modelProvider;
  if (req.model) meta.model = req.model;
  if (req.reasoningEffort) meta.reasoningEffort = req.reasoningEffort;
  const launchOverrides = mergeAccessModeSandbox(req.launchOverrides, req.accessMode ?? "");
  if (launchOverrides) meta.launchOverrides = launchOverrides;
  if (req.source && req.source !== "local") meta.source = req.source;
  if (req.manager) meta.manager = true;
  return meta;
}

function hashAttachmentData(data: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 5381;
  for (let i = 0; i < data.length; i += 1) {
    const code = data.charCodeAt(i);
    h1 = Math.imul(h1 ^ code, 0x01000193);
    h2 = (Math.imul(h2, 33) ^ code) | 0;
  }
  return `${data.length}:${(h1 >>> 0).toString(16)}-${(h2 >>> 0).toString(16)}`;
}

// Lightweight fingerprint covering every field sent on thread/start without
// calling buildComposerInput twice or retaining base64 image payloads in state.
export function spawnRequestFingerprint(req: SpawnRequest): string {
  return JSON.stringify({
    ...threadStartMeta(req),
    prompt: req.prompt,
    attachments: req.attachments?.length
      ? req.attachments.map((item) => ({
          marker: item.marker,
          mediaType: item.mediaType,
          ...(item.name !== undefined ? { name: item.name } : {}),
          data: hashAttachmentData(item.data),
        }))
      : undefined,
    skillNames: req.skillNames?.length ? req.skillNames : undefined,
    commandNames: req.commandNames?.length ? req.commandNames : undefined,
    mentions: req.mentions?.length ? req.mentions : undefined,
  });
}

// buildComposerInput assembles the optional RAW text item and image items,
// translating composer attachment markers at the wire boundary.
export function buildThreadStartParams(req: SpawnRequest): ThreadStartParams {
  return {
    ...threadStartMeta(req),
    input: buildComposerInput(req.prompt, req.attachments, req.skillNames, req.commandNames, req.mentions),
  };
}

// The ref is thread.evener.ref VERBATIM - the qualified "<source>:<threadId>"
// form (e.g. "local:abc123"), NOT the legacy server's "local:"-stripped bare
// id (floor §1.14 describes that legacy routing). The SPA
// routes and reads every session by this qualified ref: thread/read resolves
// it through appwire.ParseRef, which REQUIRES the ":" separator (appwire/
// refs.go; cmd/evener-hub/internal/appsource/registry.go SourceForRef), so a
// stripped bare id is rejected outright. Every other shipped session-open path
// uses the same verbatim ref (Rail.tsx:177 node.session.ref;
// SessionActionsMenu.tsx resp.thread.evener.ref for fork children). Stripping
// here would open a dead-on-arrival session pane.
export async function startThread(client: AppwireClientLike, req: SpawnRequest): Promise<SpawnResult> {
  const params = buildThreadStartParams(req);
  if (req.clientMutationId) params.clientMutationId = req.clientMutationId;
  const resp = await client.request("thread/start", params);
  return { ref: resp.thread.evener.ref };
}

export type StartFailureKind =
  | "failed"
  | "uncertain-timeout"
  | "uncertain-disconnected"
  | "uncertain-partial"
  | "uncertain";

export interface StartFailureContext {
  startDispatched: boolean;
  retryablePendingStart: boolean;
}

export interface StartFailureOutcome {
  kind: StartFailureKind;
  clearPendingStart: boolean;
  retryable: boolean;
}

// Separates confirmed pre-spawn refusals (and pre-dispatch failures) from
// post-dispatch failures whose outcome is uncertain because the hub detaches
// thread/start after admission and may still have created the session.
export function classifyStartFailure(err: unknown, context: StartFailureContext): StartFailureOutcome {
  if (!context.startDispatched) {
    return { kind: "failed", clearPendingStart: false, retryable: false };
  }
  const data = mutationErrorData(err);
  if (data?.mutationOutcome === "notAccepted" || data?.retryDisposition === "blocked") {
    return { kind: "failed", clearPendingStart: true, retryable: false };
  }
  if (errorKind(err) === "hub-unreachable") {
    return { kind: "failed", clearPendingStart: false, retryable: false };
  }
  if (err instanceof RequestTimeoutError) {
    return { kind: "uncertain-timeout", clearPendingStart: false, retryable: context.retryablePendingStart };
  }
  if (isSocketDroppedError(err)) {
    return { kind: "uncertain-disconnected", clearPendingStart: false, retryable: context.retryablePendingStart };
  }
  if (!context.retryablePendingStart) {
    return { kind: "failed", clearPendingStart: false, retryable: false };
  }
  return {
    kind: err instanceof WireError ? "uncertain-partial" : "uncertain",
    clearPendingStart: false,
    retryable: true,
  };
}

const RETRY_CHECK_GUIDANCE = "Press Start again to check and open it without creating a duplicate.";
const MANUAL_CHECK_GUIDANCE = "Check the session list before starting again.";

export function formatStartFailureToast(outcome: StartFailureOutcome, err: unknown): string {
  const guidance = outcome.retryable ? RETRY_CHECK_GUIDANCE : MANUAL_CHECK_GUIDANCE;
  switch (outcome.kind) {
    case "failed":
      return `Start failed: ${friendlyLaunchErrorMessage(err)}`;
    case "uncertain-timeout":
      return `Session start timed out, but it may still have started. ${guidance}`;
    case "uncertain-disconnected":
      return `Connection lost while starting the session; it may still have started. ${guidance}`;
    case "uncertain-partial": {
      const detail = friendlyLaunchErrorMessage(err);
      const separator = /[.!?]$/.test(detail) ? " " : ". ";
      return `Start did not finish: ${detail}${separator}Press Start again to retry without creating a duplicate.`;
    }
    case "uncertain":
      return `Could not confirm whether the session started. ${RETRY_CHECK_GUIDANCE}`;
  }
}
