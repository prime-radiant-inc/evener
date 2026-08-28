// actions.ts wraps the application mutations the rail's row menu drives. The
// typed AppWire mutations await exact navigation targets before removing an
// overlay; the remaining REST-backed pin actions retain their request helpers.
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type {
  FavoriteSetResponse,
  NavigationMutation,
  ProjectDeleteResponse,
  SessionDeleteResponse,
} from "../../protocol/types.gen";
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

export type ProjectDeleteResult = ProjectDeleteResponse;

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

/** Deletes every removable session in a path-validated local project through
 * evener/project/delete. A live session at entry rejects the whole request as
 * an AppWire conflict; concurrent resumes are returned in skipped. */
export async function deleteProject(key: string, workingDir: string): Promise<ProjectDeleteResult> {
  const client: AppwireClientLike | null = connectionStore.getState().client;
  if (!client) {
    throw new Error("project delete action: no client connected; call connectionStore.connect(client) first");
  }
  return client.request("evener/project/delete", { key, workingDir });
}

/** Deletes one ended or crashed local session through the typed hub method.
 * Live or concurrently reserved targets resolve in `skipped`; validation and
 * server failures reject through AppWire. */
export async function deleteSession(client: AppwireClientLike, ref: string): Promise<SessionDeleteResponse> {
  return client.request("evener/session/delete", { ref });
}
