// The one place the frontend calls the hub's `evener/git/head` method, shared
// by the two surfaces that show a directory's git location:
//
//   - the Spawn pane's branch chip, which needs only the branch
//     (resolveHeadBranch), and
//   - the session composer's location line, which also links the branch to its
//     forge repo page and so needs the "origin" remote (resolveGitLocation).
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
import type { AppwireClientLike } from "../protocol/clientLike";

export interface GitLocation {
  // Branch name, or a detached-HEAD short SHA, or "" when unknown.
  branch: string;
  // The repo's sanitized "origin" remote URL, or "" when unset, unreadable, or
  // not requested.
  originUrl: string;
}

const EMPTY: GitLocation = { branch: "", originUrl: "" };

async function requestGitHead(client: AppwireClientLike, cwd: string, includeOrigin: boolean): Promise<GitLocation> {
  if (cwd.trim() === "") return EMPTY;
  try {
    const params = includeOrigin ? { cwd, includeOrigin: true } : { cwd };
    const data = await client.request("evener/git/head", params);
    return { branch: data.head, originUrl: data.originUrl ?? "" };
  } catch {
    return EMPTY;
  }
}

export async function resolveHeadBranch(client: AppwireClientLike, cwd: string): Promise<string> {
  return (await requestGitHead(client, cwd, false)).branch;
}

export async function resolveGitLocation(client: AppwireClientLike, cwd: string): Promise<GitLocation> {
  return requestGitHead(client, cwd, true);
}
