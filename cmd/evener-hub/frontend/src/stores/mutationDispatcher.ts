// The dispatcher is the package's (state/mutation/dispatcher.ts): serialized
// per-ref dispatch over D26's storage port, with the no-blind-replay rule and
// every reaction to an outcome an option this app supplies. This module is the
// web's name for it, so its importers do not move.
export { MutationDispatcher, validConsumedClientMutationIds } from "@evener/appwire-client/state/mutation";
