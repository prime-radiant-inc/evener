// Access-mode chip options and their sandbox mapping (floor §1.8). The four
// rows are fixed and static (never fetched); each maps 1:1 to a launch-config
// `sandbox` value, mirroring web_spawn.go's sandboxForAccessMode /
// launchOverridesWithAccessMode exactly - including the "the advanced schema's
// explicit sandbox wins" precedence.
import type { LaunchConfigLayer } from "../../protocol/types.gen";

export interface AccessModeOption {
  value: string;
  label: string;
}

// Fixed order full → read-only → workspace-write → restricted (floor §1.8,
// spawn.js:4-9). Labels are sentence case per the design system.
export const ACCESS_MODE_OPTIONS: readonly AccessModeOption[] = [
  { value: "full", label: "Full access" },
  { value: "read-only", label: "Read-only" },
  { value: "workspace-write", label: "Workspace write" },
  { value: "restricted", label: "Restricted" },
];

// Mirrors web_spawn.go:sandboxForAccessMode. Note "full" maps to the sandbox
// being OFF; every other named mode maps to its own literal; anything else
// (including the empty "no explicit access mode") maps to no sandbox at all.
export function sandboxForAccessMode(mode: string): string {
  switch (mode.trim()) {
    case "full":
      return "off";
    case "read-only":
    case "workspace-write":
    case "restricted":
      return mode.trim();
    default:
      return "";
  }
}

// Inverse of sandboxForAccessMode, for labelling: the access-mode row a
// resolved launch-config `sandbox` value corresponds to. "off" maps back to
// "full" (Full access); an unknown or empty sandbox has no row.
export function accessModeForSandbox(sandbox: string): AccessModeOption | undefined {
  const value = sandbox.trim() === "off" ? "full" : sandbox.trim();
  return ACCESS_MODE_OPTIONS.find((option) => option.value === value);
}

// The empty option's label for the access-mode control: names the sandbox a
// session started now would inherit, in the control's own friendly wording -
// "Workspace write (default)". Plain "(default)" until a resolve lands with
// one; a sandbox value outside the four fixed rows is named raw rather than
// dropped (same rule the effort ladder's preserved values follow).
export function accessModeDefaultLabel(resolvedSandbox: string): string {
  const sandbox = resolvedSandbox.trim();
  if (sandbox === "") return "(default)";
  const option = accessModeForSandbox(sandbox);
  return `${option?.label ?? sandbox} (default)`;
}

// Mirrors web_spawn.go:launchOverridesWithAccessMode. Merges the access-mode
// sandbox into launch overrides ONLY when the advanced-options schema hasn't
// already set `sandbox` explicitly (floor §1.8) - the schema value wins.
// Never mutates the caller's object.
export function mergeAccessModeSandbox(
  overrides: LaunchConfigLayer | undefined,
  accessMode: string,
): LaunchConfigLayer | undefined {
  const sandbox = sandboxForAccessMode(accessMode);
  if (sandbox === "") return overrides;
  if (!overrides) return { sandbox };
  if (overrides.sandbox && overrides.sandbox.trim() !== "") return overrides;
  return { ...overrides, sandbox };
}
