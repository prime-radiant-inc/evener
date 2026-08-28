import type { AppwireClientLike } from "../../protocol/testing/fakeClient";

// Branch/worktree HEAD auto-resolution (floor §1.7). The resolved HEAD ref
// only fills the branch chip's DISPLAY - the branch value is never sent on the
// wire (see startThread.ts's branch note). Fails soft: any error yields "" (no
// branch shown), never a thrown error, so a disconnected hub or a non-git
// working dir never blocks the form.

export async function resolveHeadBranch(client: AppwireClientLike, cwd: string): Promise<string> {
  if (cwd.trim() === "") return "";
  try {
    const data = await client.request("evener/git/head", { cwd });
    return data.head;
  } catch {
    return "";
  }
}
