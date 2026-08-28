// actions.ts wraps the application mutations the rail's row menu drives:
// favorite/rename/archive/delete-project. REST-backed actions retain their
// handler-specific request helpers below; favorite and archive use the typed
// AppWire client. No optimistic UI: callers await the response's exact
// navigation targets before removing their overlay.
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { FavoriteSetResponse, NavigationMutation } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";

/** Wire shape of GET /api/pin-sections — { id, name, member_count }. */
export interface PinSectionSummary {
  id: string;
  name: string;
  member_count: number;
}

export interface NavigationMutationReceipt {
  navigation: NavigationMutation;
}
export type FavoriteMutationResponse = FavoriteSetResponse;

export interface ProjectDeleteResult {
  deleted: string[];
  skipped: { id: string; reason: string }[];
  navigation: NavigationMutation;
}

async function parseErrorBody(res: Response): Promise<string> {
  try {
    const data = (await res.json()) as { error?: string };
    if (typeof data.error === "string" && data.error !== "") return data.error;
  } catch {
    // non-JSON (or empty) error body: fall through to the status line
  }
  return `${res.status} ${res.statusText}`;
}

export class RailRequestError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "RailRequestError";
    this.status = status;
  }
}

export function isRailRequestStatus(error: unknown, status: number): boolean {
  return error instanceof RailRequestError && error.status === status;
}

async function requestJSON<T>(url: string, init: RequestInit): Promise<T> {
  const res = await fetch(url, { credentials: "same-origin", ...init });
  if (!res.ok) throw new RailRequestError(await parseErrorBody(res), res.status);
  return (await res.json()) as T;
}

// postJSON POSTs `body` as JSON and returns the parsed JSON response - except
// for a 204 No Content (handleAPIRename's success path), which resolves to
// undefined rather than attempting to parse an empty body as JSON.
async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new RailRequestError(await parseErrorBody(res), res.status);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/** Sets a project favorite through the typed hub AppWire method. */
export async function setFavorite(
  client: AppwireClientLike,
  kind: "project",
  id: string,
  favorited: boolean,
): Promise<FavoriteMutationResponse> {
  return client.request("evener/favorite/set", { kind, id, favorited });
}

export interface SessionPinAssignment {
  session_ref: string;
  section: PinSectionSummary;
}

export interface SessionPinMutationResponse {
  ok: true;
  changed: boolean;
  assignment: SessionPinAssignment;
  navigation: NavigationMutation;
}

export async function listPinSections(): Promise<PinSectionSummary[]> {
  return requestJSON<PinSectionSummary[]>("/api/pin-sections", {});
}

export async function assignSessionPin(
  ref: string,
  target: { section_id: string } | { section_name: string },
): Promise<SessionPinMutationResponse> {
  return requestJSON<SessionPinMutationResponse>("/api/session-pin", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ session_ref: ref, ...target }),
  });
}

export async function unpinSession(ref: string): Promise<NavigationMutationReceipt & { ok: true; changed: boolean }> {
  const response = await requestJSON<SessionPinMutationResponse>(`/api/session-pin?ref=${encodeURIComponent(ref)}`, {
    method: "DELETE",
  });
  return { ok: response.ok, changed: response.changed, navigation: response.navigation };
}

export async function renamePinSection(
  id: string,
  name: string,
): Promise<NavigationMutationReceipt & { section: PinSectionSummary }> {
  const response = await requestJSON<{
    ok: true;
    changed: boolean;
    section: PinSectionSummary;
    navigation: NavigationMutation;
  }>(`/api/pin-sections/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  return { section: response.section, navigation: response.navigation };
}

export async function deletePinSection(
  id: string,
): Promise<NavigationMutationReceipt & { ok: true; changed: boolean; member_count: number }> {
  return requestJSON(`/api/pin-sections/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** POST /api/sessions/{ref}/rename. Body: {name}. ref is URL-escaped - the
 * dispatcher (handleAPISession) url.PathUnescape()s the first path segment
 * after /api/sessions/, which is how a ref containing "/" or ":" survives
 * routing intact. */
export async function renameSession(ref: string, name: string): Promise<NavigationMutationReceipt> {
  return postJSON(`/api/sessions/${encodeURIComponent(ref)}/rename`, { name });
}

/** Sets an archive decision through evener/archive/set. workingDir is required
 * server-side for kind="project" (validated against identifier.ResolveProject)
 * and omitted for kind="session". */
export async function setArchived(
  kind: "session" | "project",
  id: string,
  archived: boolean,
  workingDir?: string,
): Promise<NavigationMutationReceipt> {
  const client: AppwireClientLike | null = connectionStore.getState().client;
  if (!client) {
    throw new Error("archive action: no client connected; call connectionStore.connect(client) first");
  }
  const params = { kind, id, archived, ...(workingDir === undefined ? {} : { workingDir }) };
  return client.request("evener/archive/set", params);
}

/** POST /api/project/delete. Body: {key, working_dir}. Destructive -
 * removes every session file under the project. Rejects (409, surfaced as
 * a thrown Error carrying the handler's message) when anything in the
 * project is still live. */
export async function deleteProject(key: string, workingDir: string): Promise<ProjectDeleteResult> {
  return postJSON<ProjectDeleteResult>("/api/project/delete", { key, working_dir: workingDir });
}

/** POST /api/sessions/{ref}/delete. No body - the ref in the URL is the only
 * input the handler reads. Destructive - removes one ended or crashed local
 * session's artifacts, decisions, and rendezvous records without touching
 * project siblings (cmd/evener-hub/web_api_session_delete.go). Same response
 * shape as deleteProject: a live or concurrently-reserved target resolves
 * with itself in `skipped` rather than rejecting - only a validation or
 * server error (400/500) rejects. */
export async function deleteSession(ref: string): Promise<ProjectDeleteResult> {
  return postJSON<ProjectDeleteResult>(`/api/sessions/${encodeURIComponent(ref)}/delete`, {});
}
