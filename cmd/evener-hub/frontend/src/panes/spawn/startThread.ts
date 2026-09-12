// startThread is the canonical launch seam: it starts a real session over
// AppWire "thread/start" and returns the created thread's ref. T1 defines the
// signature + a minimal working body (bare
// prompt + cwd -> real session); T2 fills the rest (branch/access-mode ->
// launchOverrides, the schema engine, sticky defaults).
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { LaunchConfigLayer, ThreadStartParams } from "../../protocol/types.gen";
import { buildComposerInput } from "../../stores/composerInput";
import type { InputAttachment } from "../../stores/threads";
import { mergeAccessModeSandbox } from "./accessMode";

export interface SpawnRequest {
  cwd: string; // required (ThreadStartParams.cwd)
  prompt: string; // RAW, untrimmed - floor §1.12 (bar the marker translation startThread applies)
  attachments?: InputAttachment[];
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
}

export interface SpawnResult {
  ref: string;
}

// buildComposerInput assembles the optional RAW text item and image items,
// translating composer attachment markers at the wire boundary.
// The ref is thread.evener.ref VERBATIM - the qualified "<source>:<threadId>"
// form (e.g. "local:abc123"), NOT the legacy server's "local:"-stripped bare
// id (floor §1.14 / spawn.js:404-417 describes that legacy routing). The SPA
// routes and reads every session by this qualified ref: thread/read resolves
// it through appwire.ParseRef, which REQUIRES the ":" separator (appwire/
// refs.go; cmd/evener-hub/internal/appsource/registry.go SourceForRef), so a
// stripped bare id is rejected outright. Every other shipped session-open path
// uses the same verbatim ref (Rail.tsx:177 node.session.ref;
// SessionActionsMenu.tsx resp.thread.evener.ref for fork children). Stripping
// here would open a dead-on-arrival session pane.
export async function startThread(client: AppwireClientLike, req: SpawnRequest): Promise<SpawnResult> {
  const params: ThreadStartParams = { cwd: req.cwd, input: buildComposerInput(req.prompt, req.attachments) };
  if (req.harness) params.harness = req.harness;
  if (req.modelProvider) params.modelProvider = req.modelProvider;
  if (req.model) params.model = req.model;
  if (req.reasoningEffort) params.reasoningEffort = req.reasoningEffort;
  const launchOverrides = mergeAccessModeSandbox(req.launchOverrides, req.accessMode ?? "");
  if (launchOverrides) params.launchOverrides = launchOverrides;
  const resp = await client.request("thread/start", params);
  return { ref: resp.thread.evener.ref };
}
