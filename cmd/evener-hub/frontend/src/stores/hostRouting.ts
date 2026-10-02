// Compatibility entry point for browser consumers; the SDK owns host routing.
export type { HostDependentDiscoveryMethod } from "@evener/appwire-client";
export {
  HOST_DEPENDENT_DISCOVERY_METHODS,
  hostRequest,
  isLocalHost,
  LOCAL_HOST,
  normalizeHost,
} from "@evener/appwire-client";
