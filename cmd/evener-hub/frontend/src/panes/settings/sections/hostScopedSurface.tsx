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
//
// The BODY is keyed on the host's identity - a switch, and a host removed and
// re-added under the same name, both REMOUNT it. Every pane body here
// holds state that belongs to the host it was loaded from - a launch form's
// draft, an AGENTS.md draft and its baseline, a marketplace selection and its
// editor draft, an MCP add-form's draft, a pending Remove confirmation - and
// none of that is the next host's: left in place it is shown against the new
// host's data, and Save submits it to the NEW host. Re-rendering the body (a
// plain prop change) cannot promise otherwise, and a reset effect cannot either
// - it lands a commit late, with the old host's values already standing under
// the new host. A key is the one mechanism that leaves no such commit, and it
// covers every pane that renders through this frame rather than trusting each
// one to remember. The picker above stays outside the key, so the selection is
// not remounted by its own change.
import { Fragment, type ReactNode } from "react";
import { isLocalHost } from "../../../stores/hostRouting";
import { hostInstanceIdentity, registrySaysHostGone, useHostsStore } from "../../../stores/hosts";
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
  // refusal) instead of a premature "no longer configured". The predicate is the
  // SHARED one (stores/hosts.ts's registrySaysHostGone), so this refusal and the
  // per-host registries' eviction can never disagree about what "gone" means.
  const gone = !isLocalHost(host) && registrySaysHostGone(load, host);
  // The body's key is the host's INSTANCE identity, not its name: a host removed
  // and re-added under the same name is a different registration whose per-host
  // stores are new instances, and the name alone would leave the previous
  // registration's draft standing under it (see stores/hosts.ts's
  // hostInstanceIdentity, which changes on exactly the transitions that rebuild
  // one of those instances and on nothing else - not on a refetch, and not on
  // the live session state the hub reports on the same row). The local hub has
  // no registration to key on, so it keeps its own name, stable as ever.
  const bodyKey = isLocalHost(host) ? host : `host:${hostInstanceIdentity(host)}`;
  return (
    <div className={CLASS.root}>
      <HostPicker />
      {gone ? (
        <p className={CLASS.note} role="status">
          Host {host} is no longer configured, so it has no settings to show here.
        </p>
      ) : (
        <Fragment key={bodyKey}>{children(host)}</Fragment>
      )}
    </div>
  );
}
