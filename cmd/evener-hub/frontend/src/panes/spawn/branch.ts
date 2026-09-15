import type { AppwireClientLike } from "../../protocol/clientLike";
import { hostRequest } from "../../stores/hostRouting";

// Branch/worktree HEAD auto-resolution (floor §1.7). The resolved HEAD ref
// only fills the branch chip's DISPLAY - the branch value is never sent on the
// wire (see startThread.ts's branch note). Fails soft: any error yields "" (no
// branch shown), never a thrown error, so a disconnected hub or a non-git
// working dir never blocks the form.

export async function resolveHeadBranch(
  client: AppwireClientLike,
  cwd: string,
  host: string = "local",
): Promise<string> {
  if (cwd.trim() === "") return "";
  try {
    // Host scoping (component 07b): a remote working directory's HEAD is read
    // on the host, not the controller's filesystem.
    const data = await hostRequest(client, host, "evener/git/head", { cwd });
    return data.head;
  } catch {
    return "";
  }
}
