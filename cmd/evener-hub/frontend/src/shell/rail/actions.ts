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

/** The settled outcome of a mutation fanned out to a project's owning sources:
 * the receipt of the last owner that answered (what callers converge on) plus
 * every owner the fan-out could not reach. A partial fan-out is still a
 * commit — the owners that answered hold the decision — so callers get it
 * back to surface instead of losing it behind the first rejection. */
export type SourceFanOutReceipt<Receipt> = Receipt & {
  /** Owners whose request rejected, named the way the wire spells them
   * ("local" for this hub's own source); empty when every owner answered. */
  readonly failedSources: readonly string[];
};

/** A favorite fan-out's settled receipt: the value the read side presents for
 * the project now. The read side shows a favorite when any owner holds one, so
 * a set is presented once an owner committed it, and a clear only once every
 * owner answered — a clear that could not reach an owner leaves that owner's
 * decision in place. */
export type FavoriteFanOutReceipt = SourceFanOutReceipt<FavoriteMutationResponse> & {
  readonly favorite: boolean;
};

/** What the caller knows about a favorite before the fan-out runs.
 * `favoritedBefore` is the favorite the read side presented for the project
 * when the caller decided to toggle it: the rail's own row value. */
export interface FavoriteFanOutOptions {
  readonly favoritedBefore: boolean;
}

/** The value the read side presents once a favorite fan-out has settled: a set
 * is presented as soon as any owner committed it (the read side shows a
 * favorite when any owner holds one), while a clear is presented only when
 * every owner answered or the caller already knew that no owner held one. An
 * owner that failed kept its decision, so a failure is not on its own a
 * favorite: with `favoritedBefore` false the caller has the proof that no owner
 * held one, which is what keeps clearing an unfavorited project from flipping
 * the row back to favorited just because an owner was unreachable. */
function settledFavorite(
  favorited: boolean,
  settled: SettledFanOut<FavoriteMutationResponse>,
  favoritedBefore: boolean,
): boolean {
  if (favorited) return settled.answered > 0;
  return settled.failures.length > 0 && favoritedBefore;
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

/** One fan-out's settlement: the last owner that answered, how many owners
 * answered at all, and every rejection with the owner that produced it. */
interface SettledFanOut<Receipt> {
  receipt: Receipt | undefined;
  answered: number;
  failures: Array<{ owner: string; error: unknown }>;
}

/** The owners a project fan-out addresses: the controller's own source first —
 * its requests omit the wire field, so it is the empty decision key — then
 * every remote host that owns rows in the project. */
function fanOutOwners(ownership: ProjectOwnership): string[] {
  return [...(ownership.local ? [""] : []), ...ownership.hosts];
}

/** The name an owner is reported under: the controller's own source is the
 * wire's "local", a remote owner its host name. */
const ownerName = (owner: string) => (owner === "" ? "local" : owner);

/** settleFanOut issues every owner's request and waits for all of them: a
 * rejection is recorded against its owner rather than aborting the fan-out,
 * so a failing controller request cannot leave a reachable remote owner
 * unasked and an answer that committed is never lost. */
async function settleFanOut<Receipt>(
  owners: readonly string[],
  request: (owner: string) => Promise<Receipt>,
): Promise<SettledFanOut<Receipt>> {
  const settled = await Promise.allSettled(owners.map((owner) => request(owner)));
  const receipts: Receipt[] = [];
  const failures: Array<{ owner: string; error: unknown }> = [];
  owners.forEach((owner, index) => {
    const result = settled[index];
    if (result?.status === "fulfilled") receipts.push(result.value);
    else if (result) failures.push({ owner, error: result.reason });
  });
  return { receipt: receipts.length === 0 ? undefined : finalReceipt(receipts), answered: receipts.length, failures };
}

/** The rejection a fan-out throws when no owner answered: the first owner's
 * error, annotated with every owner that failed once there is more than one. */
function fanOutFailure(failures: ReadonlyArray<{ owner: string; error: unknown }>): Error {
  const [first] = failures;
  if (first === undefined) return new Error("project fan-out: no owner was asked");
  if (failures.length === 1) return first.error instanceof Error ? first.error : new Error(String(first.error));
  const reason = first.error instanceof Error ? first.error.message : String(first.error);
  return new Error(`${reason} (failed for ${failures.map((failure) => ownerName(failure.owner)).join(", ")})`, {
    cause: first.error,
  });
}

/** The warning a caller shows when a settled fan-out left owners behind: names
 * them so the reader can retry, because a decision a failed owner already
 * holds is left in place rather than compensated. Empty when every owner
 * answered. */
export function partialFanOutNotice(failedSources: readonly string[]): string | undefined {
  if (failedSources.length === 0) return undefined;
  return `${failedSources.join(", ")} did not answer; retry to update ${
    failedSources.length === 1 ? "that owner" : "those owners"
  }`;
}

/** Sets a project favorite through the typed hub AppWire method. A favorite is
 * keyed by (source, project ID), so a project merged across hosts gets one
 * request per owning source: the read side shows a favorite when any owner
 * holds one, and clearing it must clear every owner or the row stays
 * favorited. Every owner is asked even when one rejects, and the returned
 * value is derived from the settled set: `favorite` reports what the read side
 * presents now and `failedSources` names the owners whose decision is still
 * unknown. Only a fan-out no owner answered rejects.
 *
 * `sources` and `options` are both required: a derived favorite is only as
 * truthful as the caller's knowledge of the project, and the fan-out cannot tell
 * an owner that held a favorite from one that never did without it. Pass the
 * summary's own sources (or undefined when it reports none) and the favorite the
 * read side presented for the row. */
export async function setFavorite(
  client: AppwireClientLike,
  kind: "project",
  id: string,
  favorited: boolean,
  sources: readonly string[] | undefined,
  options: FavoriteFanOutOptions,
): Promise<FavoriteFanOutReceipt> {
  const ownership = projectOwnership(sources);
  const settled = await settleFanOut(fanOutOwners(ownership), (owner) =>
    client.request("evener/favorite/set", { kind, id, favorited, ...sourceParams(owner) }),
  );
  if (!settled.receipt) throw fanOutFailure(settled.failures);
  return {
    ...settled.receipt,
    failedSources: settled.failures.map((failure) => ownerName(failure.owner)),
    favorite: settledFavorite(favorited, settled, options.favoritedBefore),
  };
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
 * is already its host-qualified ref, so it is never source-qualified here. The
 * fan-out settles every owner, and a partial result is a commit for the owners
 * that answered: the receipt converges the client and `failedSources` names the
 * owners whose decision the archive still lacks. Only a fan-out no owner
 * answered rejects.
 *
 * workingDir names a path on the owner being asked, so it rides the local leg
 * only: the controller resolves its own project from it, while a host's leg is
 * validated against the path that host reported for the ID
 * (validateHostProjectArchive) and the controller's path would fail every
 * remote leg of a project whose owners use different paths. The (source, ID)
 * key is what makes a remote leg unambiguous. */
export async function setArchived(
  kind: "session" | "project",
  id: string,
  archived: boolean,
  workingDir?: string,
  sources?: readonly string[],
): Promise<SourceFanOutReceipt<NavigationMutationReceipt>> {
  const client: AppwireClientLike | null = connectionStore.getState().client;
  if (!client) {
    throw new Error("archive action: no client connected; call connectionStore.connect(client) first");
  }
  const ownership = kind === "project" ? projectOwnership(sources) : { local: true, hosts: [] };
  const settled = await settleFanOut(fanOutOwners(ownership), (owner) =>
    client.request("evener/archive/set", {
      kind,
      id,
      archived,
      ...(owner === "" && workingDir !== undefined ? { workingDir } : {}),
      ...sourceParams(owner),
    }),
  );
  if (!settled.receipt) throw fanOutFailure(settled.failures);
  return { ...settled.receipt, failedSources: settled.failures.map((failure) => ownerName(failure.owner)) };
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
