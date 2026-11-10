import type { InstanceEntry } from "@evener/appwire-client";

/** One non-default openai-chat instance that needs a credential and has none,
 * unless `overrides` says otherwise. */
export function instance(
  overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">,
): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}
