// preflight is the working-directory preflight seam (floor §1.13): validate
// the cwd via appwire "serf/path/validate" and create it via REST
// "POST /api/dirs/create" (no appwire method exists for creation - verified).
// T1 defines the signatures + a minimal working body; T2 wires them into the
// real submission flow and adds the "doesn't exist yet -> offer to create"
// discrimination (the offer-create outcome + the in-form Create&start dialog,
// spawn.js:527-566).
import type { AppwireClientLike } from "../../protocol/testing/fakeClient";

export type PreflightOutcome =
  | { kind: "ok" }
  | { kind: "abort"; message: string } // deterministic "not fixable" (spawn.js:582-588)
  | { kind: "offer-create"; path: string }; // in-form Create&start dialog (spawn.js:527-566)

export async function preflightDir(client: AppwireClientLike, path: string): Promise<PreflightOutcome> {
  try {
    const result = await client.request("serf/path/validate", { path, kind: "dir" });
    if (result.valid) return { kind: "ok" };
    // T1 treats every invalid directory as a deterministic abort; T2 adds the
    // offer-create branch (a not-yet-existing but creatable directory).
    return { kind: "abort", message: result.error ?? "invalid working directory" };
  } catch {
    // Fail OPEN: if the CHECK itself fails (RPC down, etc.), never block the
    // spawn - let submission proceed (spawn.js:573-580).
    return { kind: "ok" };
  }
}

export async function createDir(path: string): Promise<void> {
  const res = await fetch("/api/dirs/create", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ path }),
  });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // Non-JSON (or empty) error body: keep the HTTP status line.
    }
    throw new Error(message);
  }
}
