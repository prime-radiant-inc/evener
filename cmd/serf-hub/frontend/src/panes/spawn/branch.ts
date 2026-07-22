// Branch/worktree HEAD auto-resolution (floor §1.7). REST-ONLY: appwire has no
// git/head method, so this always goes through GET /api/git/head?cwd= (the
// legacy no-ops the whole thing when appwire is present, spawn.js:376-379). The
// resolved HEAD ref only fills the branch chip's DISPLAY - the branch value is
// never sent on the wire (see startThread.ts's branch note). Fails soft: any
// error yields "" (no branch shown), never a thrown error, so a flaky /api or a
// non-git working dir never blocks the form.
const FETCH_INIT: RequestInit = { credentials: "same-origin" };

export async function resolveHeadBranch(cwd: string): Promise<string> {
  if (cwd.trim() === "") return "";
  try {
    const res = await fetch(`/api/git/head?cwd=${encodeURIComponent(cwd)}`, FETCH_INIT);
    if (!res.ok) return "";
    const data = (await res.json()) as { branch?: string };
    return data.branch ?? "";
  } catch {
    return "";
  }
}
