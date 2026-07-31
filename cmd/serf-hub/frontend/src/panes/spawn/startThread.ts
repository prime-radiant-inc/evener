// startThread is the thread/start seam: it starts a real session over appwire
// "thread/start" (NOT the REST /api/spawn shim) and returns the created
// thread's ref. T1 defines the signature + a minimal working body (bare
// prompt + cwd -> real session); T2 fills the rest (branch/access-mode ->
// launchOverrides, the schema engine, sticky defaults).
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { InputItem, LaunchConfigLayer, ThreadStartParams } from "../../protocol/types.gen";
import { translateAttachmentMarkers } from "../../stores/attachmentMarkers";
import type { InputAttachment } from "../../stores/threads";
import { mergeAccessModeSandbox } from "./accessMode";

export interface SpawnRequest {
  cwd: string; // required (ThreadStartParams.cwd)
  prompt: string; // RAW, untrimmed - floor §1.12 (bar the marker translation startThread applies)
  attachments?: InputAttachment[];
  harness?: string;
  modelProvider?: string; // serf-model harness: "<provider>/<model>" split -> provider half
  model?: string; // model id (bare id for a non-serf harness - floor §1.4)
  reasoningEffort?: string; // wire camelCase - floor §1.11
  // branch is DISPLAY-ONLY (floor §1.7): the branch chip shows the resolved
  // HEAD ref, but the wire has nowhere to carry it - appwire ThreadStartParams
  // and LaunchConfigLayer both lack a branch field, and the legacy REST
  // /api/spawn dropped req.Branch on the floor too (web_spawn.go:135-144, never
  // threaded into ThreadStartParams). Accepted here so callers can pass the
  // form value uniformly; startThread never sends it.
  branch?: string;
  accessMode?: string; // merged -> launchOverrides.sandbox unless schema set it (floor §1.8)
  launchOverrides?: LaunchConfigLayer;
}

export interface SpawnResult {
  ref: string;
}

// buildInput mirrors threadsStore's own turn-input assembly (threads.ts:
// 304-312; unexported there, mirrored locally per PIN-C): an optional leading
// RAW text item (kept only when non-empty after trim, but sent UNTRIMMED -
// floor §1.12), then one image item per attachment (image-only submits are
// valid). Its caller applies the one transformation the wire text gets, the
// "[image N]" marker translation - shared with the store's own submit path
// rather than mirrored, since a mirror of THAT would silently drift.
function buildInput(text: string, attachments?: InputAttachment[]): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
  for (const att of attachments ?? []) {
    input.push({ type: "image", mediaType: att.mediaType, data: att.data, name: att.name });
  }
  return input;
}

// The ref is thread.serf.ref VERBATIM - the qualified "<source>:<threadId>"
// form (e.g. "local:abc123"), NOT the legacy server's "local:"-stripped bare
// id (floor §1.14 / spawn.js:404-417 describes that legacy routing). The SPA
// routes and reads every session by this qualified ref: thread/read resolves
// it through appwire.ParseRef, which REQUIRES the ":" separator (appwire/
// refs.go; cmd/serf-hub/internal/appsource/registry.go SourceForRef), so a
// stripped bare id is rejected outright. Every other shipped session-open path
// uses the same verbatim ref (Rail.tsx:177 node.session.ref;
// SessionActionsMenu.tsx resp.thread.serf.ref for fork children). Stripping
// here would open a dead-on-arrival session pane.
export async function startThread(client: AppwireClientLike, req: SpawnRequest): Promise<SpawnResult> {
  const prompt = translateAttachmentMarkers(req.prompt, req.attachments);
  const params: ThreadStartParams = { cwd: req.cwd, input: buildInput(prompt, req.attachments) };
  if (req.harness) params.harness = req.harness;
  if (req.modelProvider) params.modelProvider = req.modelProvider;
  if (req.model) params.model = req.model;
  if (req.reasoningEffort) params.reasoningEffort = req.reasoningEffort;
  const launchOverrides = mergeAccessModeSandbox(req.launchOverrides, req.accessMode ?? "");
  if (launchOverrides) params.launchOverrides = launchOverrides;
  const resp = await client.request("thread/start", params);
  return { ref: resp.thread.serf.ref };
}
