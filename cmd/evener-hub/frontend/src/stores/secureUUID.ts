import type { SecureRandomSource } from "@evener/appwire-client/state/mutation";
import { createSecureUUID as createSecureUUIDFromSource } from "@evener/appwire-client/state/mutation";
import { browserRandomSource } from "./browserRandomSource";

export type { SecureRandomSource };

export function createSecureUUID(): string {
  return createSecureUUIDFromSource(browserRandomSource());
}
