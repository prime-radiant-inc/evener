// The one place the frontend calls the hub's `evener/git/head` method, shared
// by the two surfaces that show a directory's git location: the Spawn pane's
// branch chip (panes/spawn/branch.ts, which wants only the branch) and the
// session composer's location line (which also wants the "origin" remote URL
// so it can link the branch to its forge repo page).
//
// Fails soft: git is not guaranteed present, the directory may not be a repo,
// and the hub may be unreachable. Every failure yields empty values rather than
// a thrown error, so a caller can render "no branch shown" without its own
// error path - the same contract panes/spawn/branch.ts's floor §1.7 documented.
import type { AppwireClientLike } from "../protocol/clientLike";

export interface GitLocation {
  // Branch name, or a detached-HEAD short SHA, or "" when unknown.
  branch: string;
  // The "origin" remote URL, or "" when unset or unreadable.
  originUrl: string;
}

const EMPTY: GitLocation = { branch: "", originUrl: "" };

export async function resolveGitLocation(client: AppwireClientLike, cwd: string): Promise<GitLocation> {
  if (cwd.trim() === "") return EMPTY;
  try {
    const data = await client.request("evener/git/head", { cwd });
    return { branch: data.head, originUrl: data.originUrl ?? "" };
  } catch {
    return EMPTY;
  }
}
