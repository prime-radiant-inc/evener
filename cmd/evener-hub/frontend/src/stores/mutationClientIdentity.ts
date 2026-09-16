// The web's client-identity port over sessionStorage: per-tab, stable across
// that tab's reloads, which is what the durable mutation outbox's provenance
// rule (see the package) needs to route a shared origin's records back to
// their sender.
export {
  isOwnMutationRecord,
  ownClientId,
  setMutationClientIdentityForTests,
} from "@evener/appwire-client/state/mutation";
