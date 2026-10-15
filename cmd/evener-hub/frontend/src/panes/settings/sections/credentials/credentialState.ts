// credentialState.ts: where one instance's credential stands, as the row and the
// detail sheet both show it. The meta line's lead, the heading dot and the
// `data-credential-state` marker all follow from this one value, so the words
// and the glyph cannot disagree about the same instance.
import type { InstanceEntry } from "@evener/appwire-client";
import { credentialLayers, keylessByDesign } from "@evener/appwire-client";
import type { CadenceState } from "../../../../widgets";

/** The hub found an error with the credential, it resolves to one, it needs
 * none (keylessByDesign), or the key it requires is missing. */
export type CredentialState = "error" | "configured" | "keyless" | "missing";

export function credentialState(instance: InstanceEntry, authError: string | undefined): CredentialState {
  if (authError) return "error";
  if (credentialLayers(instance).length > 0) return "configured";
  return keylessByDesign(instance) ? "keyless" : "missing";
}

export const CREDENTIAL_DOT: Record<CredentialState, CadenceState> = {
  error: "failed",
  configured: "idle",
  keyless: "idle",
  missing: "ended",
};
