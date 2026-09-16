// actions.ts wraps the application mutations the rail's row menu drives.
// Navigation pinning, favorite, archive, project deletion, and session
// deletion use the typed AppWire client. No optimistic UI: callers await the
// response's exact navigation targets before removing their overlay.

import type {
  AppwireClientLike,
  FavoriteSetResponse,
  NavigationMutation,
  PinSectionDeleteResponse,
  PinSectionRenameResponse,
  ProjectDeleteResponse,
  SessionDeleteResponse,
  SessionPinAssignResponse,
  SessionPinUnpinResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
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

/** A project's owning sources, split by the decision store's keys: `local` is
 * true when this hub's own sessions are in the project, and `hosts` names every
 * remote host that also owns rows in it. A summary that reports no sources is
 * the controller's own project — the same default the hub's decision readers
 * apply to an empty source list. */
export interface ProjectOwnership {
  local: boolean;
  hosts: string[];
}

/** Classifies the owning sources a project summary carries ("local" plus
 * configured host names) for the per-source requests below. A merged project
 * reports several: it needs one request per source, and a mutation that can
 * only name one owner must refuse instead of picking a host. */
export function projectOwnership(sources?: readonly string[]): ProjectOwnership {
  const hosts = new Set<string>();
  let local = false;
  for (const raw of sources ?? []) {
    const source = normalizeDecisionSource(raw);
    if (source === "") local = true;
    else hosts.add(source);
  }
  return { local: local || hosts.size === 0, hosts: [...hosts] };
}

/** The decision store's key for a source a summary or a request spells: the
 * hub's own projects key on "", and the wire's "local" is its name for them. */
function normalizeDecisionSource(source: string): string {
  const trimmed = source.trim();
  return trimmed === "local" ? "" : trimmed;
}

/** Source-qualified params for one owning source. The controller's own source
 * omits the field, which is how every pre-existing local mutation reads. */
function sourceParams(source: string): { source?: string } {
  return source === "" ? {} : { source };
}

/** The receipt a fan-out returns: the last request's, which carries the
 * navigation mutation that converges the client. projectOwnership always
 * reports at least the controller's own source, so the list is never empty. */
function finalReceipt<T>(receipts: readonly T[]): T {
  return receipts[receipts.length - 1] as T;
}

/** Sets a project favorite through the typed hub AppWire method. A favorite is
 * keyed by (source, project ID), so a project merged across hosts gets one
 * request per owning source: the read side shows a favorite when any owner
 * holds one, and clearing it must clear every owner or the row stays
 * favorited. */
export async function setFavorite(
  client: AppwireClientLike,
  kind: "project",
  id: string,
  favorited: boolean,
  sources?: readonly string[],
): Promise<FavoriteMutationResponse> {
  const ownership = projectOwnership(sources);
  const receipts: FavoriteMutationResponse[] = [];
  if (ownership.local) {
    receipts.push(await client.request("evener/favorite/set", { kind, id, favorited }));
  }
  for (const host of ownership.hosts) {
    receipts.push(await client.request("evener/favorite/set", { kind, id, favorited, ...sourceParams(host) }));
  }
  return finalReceipt(receipts);
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
 * and omitted for kind="session". A project archive is keyed by (source,
 * project ID) and the tree archives a row only once every owning source has
 * archived it, so a merged project gets one request per source; a session's ID
 * is already its host-qualified ref, so it is never source-qualified here. */
export async function setArchived(
  kind: "session" | "project",
  id: string,
  archived: boolean,
  workingDir?: string,
  sources?: readonly string[],
): Promise<NavigationMutationReceipt> {
  const client: AppwireClientLike | null = connectionStore.getState().client;
  if (!client) {
    throw new Error("archive action: no client connected; call connectionStore.connect(client) first");
  }
  const ownership = kind === "project" ? projectOwnership(sources) : { local: true, hosts: [] };
  const receipts: NavigationMutationReceipt[] = [];
  if (ownership.local) {
    receipts.push(
      await client.request("evener/archive/set", {
        kind,
        id,
        archived,
        ...(workingDir === undefined ? {} : { workingDir }),
      }),
    );
  }
  for (const host of ownership.hosts) {
    receipts.push(
      await client.request("evener/archive/set", {
        kind,
        id,
        archived,
        ...(workingDir === undefined ? {} : { workingDir }),
        ...sourceParams(host),
      }),
    );
  }
  return finalReceipt(receipts);
}

/** Deletes every removable session in a path-validated local project through
 * evener/project/delete. A live session at entry rejects the whole request as
 * an AppWire conflict; concurrent resumes are returned in skipped. Deletion is
 * local-only, so a project that also has remote owners is refused here instead
 * of being asked for without a source: a request that named no source would
 * address this hub's own project of the same ID and path. */
export async function deleteProject(
  key: string,
  workingDir: string,
  sources?: readonly string[],
): Promise<ProjectDeleteResult> {
  const ownership = projectOwnership(sources);
  if (ownership.hosts.length > 0) {
    throw new Error(`project delete is local-only; this project also belongs to ${ownership.hosts.join(", ")}`);
  }
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
