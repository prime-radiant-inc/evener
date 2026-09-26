import type { SecureRandomSource } from "@evener/appwire-client/state/mutation";
import { createSecureUUID as createSecureUUIDFromSource } from "@evener/appwire-client/state/mutation";
import { browserRandomSource } from "./browserRandomSource";

export type { SecureRandomSource };

// Resolved once at module load, like mutationClientIdentity.ts's own
// browserRandomSource() call: both are the web's callers of a package
// function that takes this port.
const randomSource = browserRandomSource();

export function createSecureUUID(): string {
  return createSecureUUIDFromSource(randomSource);
}
