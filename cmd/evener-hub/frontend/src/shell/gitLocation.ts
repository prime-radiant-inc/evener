// The one place the frontend calls the hub's `evener/git/head` method, shared
// by the surfaces that show a directory's git location:
//
//   - the Spawn pane's branch chip, which needs only the branch
//     (resolveHeadBranch), and
//   - the session location line, which also links the branch to its
//     forge repo page and so needs the "origin" remote (resolveGitLocation),
//   - the About tab's branch row (useHeadBranch), which like the Spawn chip
//     renders only the branch and so does not ask for the origin.
//
// Origin is opt-in on the wire: resolveHeadBranch does not ask for it, so the
// hub neither runs the origin lookup nor sends the remote to a caller that
// cannot render it.
//
// Both fail soft: git is not guaranteed present, the directory may not be a
// repo, and the hub may be unreachable. Every failure yields empty values rather
// than a thrown error, so a caller can render "nothing to show" without an error
// path of its own - the same contract the Spawn pane's branch chip has always
// had (floor §1.7).
import type { AppwireClientLike } from "@evener/appwire-client";
import { hostRequest } from "@evener/appwire-client";
import { useEffect, useState } from "react";

export interface GitLocation {
  // Branch name, or a detached-HEAD short SHA, or "" when unknown.
  branch: string;
  // The repo's sanitized "origin" remote URL, or "" when unset, unreadable, or
  // not requested.
  originUrl: string;
}

// Frozen, because this one object is returned by reference from every
// empty/error path: a caller that mutated it would corrupt every later "nothing
// to show" result in the app.
const EMPTY: GitLocation = Object.freeze({ branch: "", originUrl: "" });

async function requestGitHead(
  client: AppwireClientLike,
  cwd: string,
  includeOrigin: boolean,
  host: string,
): Promise<GitLocation> {
  if (cwd.trim() === "") return EMPTY;
  try {
    const params = includeOrigin ? { cwd, includeOrigin: true } : { cwd };
    // Host scoping (component 07b): a remote working directory's HEAD is read
    // on that host through evener/host/request, never on the controller's own
    // filesystem; "local" keeps the plain call.
    const data = await hostRequest(client, host, "evener/git/head", params);
    return { branch: data.head, originUrl: data.originUrl ?? "" };
  } catch {
    return EMPTY;
  }
}

export async function resolveHeadBranch(
  client: AppwireClientLike,
  cwd: string,
  host: string = "local",
): Promise<string> {
  return (await requestGitHead(client, cwd, false, host)).branch;
}

export async function resolveGitLocation(client: AppwireClientLike, cwd: string): Promise<GitLocation> {
  // The session location line reads a SESSION's directory, and the
  // session surfaces are not host-scoped yet, so this keeps the controller's
  // own call (a tracked follow-up, not a regression: the session surface has no
  // remote host to pass).
  return requestGitHead(client, cwd, true, "local");
}

// The hook forms serve the components that show a directory's git location
// while mounted: the session location line (useGitLocation) and the About
// tab's branch row (useHeadBranch). A mounted component's lookup must
// survive a hub drop: a lookup attempted while the hub was down fails soft
// to empty and would otherwise stay empty for the component's life, so the
// hook retries on each transition of this client into "ready". A late
// answer - a retry racing an earlier lookup, or an answer for a cwd or client
// the caller has since left - never displays against the current ones.
// `enabled` false asks the hub nothing: the caller gates locality (a
// source-backed session's cwd belongs to another host) and any wire-supplied
// value that wins over a view-time lookup.

// The last answer, tagged with the client that answered and the cwd it
// belongs to: a response for a cwd the caller has since left, or one the
// previous hub answered before this client reconnected, must never display
// against the current client or cwd.
interface ResolvedHead {
  client: AppwireClientLike;
  cwd: string;
  location: GitLocation;
}

function useGitHead(client: AppwireClientLike, cwd: string, includeOrigin: boolean, enabled: boolean): GitLocation {
  const [resolved, setResolved] = useState<ResolvedHead | null>(null);
  useEffect(() => {
    if (!enabled || cwd.trim() === "") return undefined;
    let active = true;
    // Fences a retry's response against an earlier lookup's in the same mount.
    let generation = 0;
    const resolve = () => {
      const mine = ++generation;
      void requestGitHead(client, cwd, includeOrigin, "local").then((location) => {
        if (active && mine === generation) setResolved({ client, cwd, location });
      });
    };
    resolve();
    const unwatch = client.onStateChange((state) => {
      if (state === "ready") resolve();
    });
    return () => {
      active = false;
      unwatch();
    };
  }, [client, cwd, enabled, includeOrigin]);
  return resolved !== null && resolved.client === client && resolved.cwd === cwd ? resolved.location : EMPTY;
}

/** The mounted, fail-soft, retry-on-recovery form of resolveHeadBranch. */
export function useHeadBranch(client: AppwireClientLike, cwd: string, enabled: boolean = true): string {
  return useGitHead(client, cwd, false, enabled).branch;
}

/** The mounted, fail-soft, retry-on-recovery form of resolveGitLocation. */
export function useGitLocation(client: AppwireClientLike, cwd: string, enabled: boolean = true): GitLocation {
  return useGitHead(client, cwd, true, enabled);
}
