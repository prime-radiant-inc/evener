import {
  type CredentialInstancesStore,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { randomUUID } from "expo-crypto";

// This app's identity on the auth mutations it issues: the hub echoes it in
// evener/auth/updated so every client attributes the change to its origin.
// One per process is enough - nothing durable compares it later.
const nativeClientId = `native-${randomUUID()}`;

/** One credential store per hub screen, shared by the provider list and any
 * sign-in flow open on it; the screen's connection effect drives its
 * connectionChanged. */
export function createNativeCredentialStore(): CredentialInstancesStore {
  return createCredentialInstancesStore({ ownClientId: () => nativeClientId });
}
