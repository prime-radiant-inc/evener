// hostScopedSurface.tsx is the frame every host-scoped settings pane shares
// (component 07b): the ONE shared HostPicker, plus the body for the host the
// settings route selected. A pane renders its own body for whatever host it is
// handed; it never grows a picker of its own, and it never falls back to the
// controller's data for a host that is not this hub.
//
// A host that is not attached (or no longer configured) is refused honestly:
// the registry's own answer is the only thing that may call a host gone, and
// until it says so the body renders and shows that host's own state or error -
// the selected host's own refusal, never this hub's.
import type { ReactNode } from "react";
import { isLocalHost } from "../../../stores/hostRouting";
import { isConfiguredHost, useHostsStore } from "../../../stores/hosts";
import { requireClass } from "../../../widgets/internal/requireClass";
import { HostPicker } from "../HostPicker";
import { useSettingsHost } from "../settingsHost";
import styles from "./hostScopedSurface.module.css";

const CLASS = {
  root: requireClass(styles.root, "hostScopedSurface.module.css", "root"),
  note: requireClass(styles.note, "hostScopedSurface.module.css", "note"),
};

export interface HostScopedSurfaceProps {
  /** Renders the settings body for the selected host. `local` (LOCAL_HOST) is
   * the controller's own hub. */
  children: (host: string) => ReactNode;
}

export function HostScopedSurface({ children }: HostScopedSurfaceProps) {
  const { host } = useSettingsHost();
  const load = useHostsStore((state) => state.load);
  // Only the registry's own answer may call a host gone: while it is still
  // being read, the body renders and shows that host's own state (or its typed
  // refusal) instead of a premature "no longer configured".
  const gone = !isLocalHost(host) && load.phase === "ready" && !isConfiguredHost(load, host);
  return (
    <div className={CLASS.root}>
      <HostPicker />
      {gone ? (
        <p className={CLASS.note} role="status">
          Host {host} is no longer configured, so it has no settings to show here.
        </p>
      ) : (
        children(host)
      )}
    </div>
  );
}
