import type { AppwireClientLike } from "../../protocol/clientLike";
import { resolveGitLocation } from "../../shell/gitLocation";

// Branch/worktree HEAD auto-resolution (floor §1.7). The resolved HEAD ref
// only fills the branch chip's DISPLAY - the branch value is never sent on the
// wire (see startThread.ts's branch note). Fails soft: any error yields "" (no
// branch shown), never a thrown error, so a disconnected hub or a non-git
// working dir never blocks the form.
//
// The AppWire call and its fail-soft contract live in shell/gitLocation.ts,
// shared with the composer's location line; this keeps the spawn pane's own
// branch-only signature.

export async function resolveHeadBranch(client: AppwireClientLike, cwd: string): Promise<string> {
  return (await resolveGitLocation(client, cwd)).branch;
}
