// preflight is the working-directory preflight seam (floor §1.13): validate
// the cwd via appwire "evener/path/validate". Creating it is the launch-config
// store's createDirectory.
// A cwd that does not exist yet is not a failure: the outcome offers the
// in-form Create&start dialog instead.
import type { AppwireClientLike } from "@evener/appwire-client";
import { hostRequest } from "@evener/appwire-client";

export type PreflightOutcome =
  | { kind: "ok" }
  | { kind: "abort"; message: string } // deterministic "not fixable"
  | { kind: "offer-create"; path: string }; // in-form Create&start dialog

// The validator's own literal error strings for a cwd that can never be fixed
// by creating a directory (floor §1.13): an empty path, a non-absolute path,
// or a path that exists but is a plain file. Every OTHER invalid reason for a
// dir-kind validate is a not-yet-existing directory (fspaths ValidateLaunchPath
// surfaces os.Stat's raw "no such file or directory" there), which IS fixable
// by creating it. Compared verbatim, matching the daemon's fspaths strings.
const NON_FIXABLE_REASONS = new Set(["path is not a directory", "absolute path required", "path is required"]);

export async function preflightDir(
  client: AppwireClientLike,
  path: string,
  host: string = "local",
): Promise<PreflightOutcome> {
  try {
    // Host scoping (component 07b): a remote host's directory is validated on
    // the host's own filesystem through evener/host/request; the local host
    // keeps the plain call.
    const result = await hostRequest(client, host, "evener/path/validate", { path, kind: "dir" });
    if (result.valid) return { kind: "ok" };
    if (result.error && NON_FIXABLE_REASONS.has(result.error)) {
      return { kind: "abort", message: result.error };
    }
    // Any other invalid reason is a creatable, not-yet-existing directory:
    // offer the in-form "Create it and start the session?" dialog.
    return { kind: "offer-create", path };
  } catch {
    // Fail OPEN: if the CHECK itself fails (RPC down, etc.), never block the
    // spawn - let submission proceed.
    return { kind: "ok" };
  }
}
