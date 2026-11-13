import { hostIdOf, LOCAL_HOST, type SessionHost, sessionHosts } from "@evener/appwire-client";
import { useConnectionStore } from "../../../stores/connection";
import { selectDisplaySources } from "../../../stores/navigation/selectors";
import { useNavigationStore } from "../../../stores/navigation/store";

/** The host a session runs on, for its details: null on a hub with no other
 * host, where every session runs in the one place there is and a host row
 * would say nothing. Reads the manifest's display sources, so a host keeps its
 * name and last-known state while the manifest revalidates. */
export function useSessionHost(ref: string): SessionHost | null {
  const sources = useNavigationStore(selectDisplaySources);
  const connected = useConnectionStore((state) => state.state === "ready");
  const hostId = hostIdOf(ref);
  if (hostId === LOCAL_HOST && !sources.some((source) => source.id !== LOCAL_HOST)) return null;
  return sessionHosts(sources, null, connected)(hostId);
}
