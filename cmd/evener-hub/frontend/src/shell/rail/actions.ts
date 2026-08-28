// actions.ts wraps the application mutations the rail's row menu drives.
// Navigation pinning, favorite, archive, project deletion, and session
// deletion use the typed AppWire client. No optimistic UI: callers await the
// response's exact navigation targets before removing their overlay.

import { WireError } from "../../protocol/errors";
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type {
  FavoriteSetResponse,
  NavigationMutation,
  PinSectionDeleteResponse,
  PinSectionRenameResponse,
  ProjectDeleteResponse,
  SessionDeleteResponse,
  SessionPinAssignResponse,
  SessionPinUnpinResponse,
} from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import type { NavigationPinSectionSummary } from "../../stores/navigation/selectors";

export type PinSectionSummary = NavigationPinSectionSummary;

export interface NavigationMutationReceipt {
  navigation: NavigationMutation;
}
export type FavoriteMutationResponse = FavoriteSetResponse;

export type ProjectDeleteResult = ProjectDeleteResponse;

export function isPinSectionNotFound(error: unknown): boolean {
  return error instanceof WireError && error.evenerErrorInfo === "resourceNotFound";
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

export async function assignSessionPin(
  client: AppwireClientLike,
  ref: string,
  target: { section_id: string } | { section_name: string },
): Promise<SessionPinAssignResponse> {
  return client.request(
    "evener/session-pin/assign",
    "section_id" in target
      ? { sessionRef: ref, sectionId: target.section_id }
      : { sessionRef: ref, sectionName: target.section_name },
  );
}

export async function unpinSession(client: AppwireClientLike, ref: string): Promise<SessionPinUnpinResponse> {
  return client.request("evener/session-pin/unpin", { sessionRef: ref });
}

export async function renamePinSection(
  client: AppwireClientLike,
  id: string,
  name: string,
): Promise<PinSectionRenameResponse> {
  return client.request("evener/pin-section/rename", { sectionId: id, name });
}

export async function deletePinSection(client: AppwireClientLike, id: string): Promise<PinSectionDeleteResponse> {
  return client.request("evener/pin-section/delete", { sectionId: id });
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
