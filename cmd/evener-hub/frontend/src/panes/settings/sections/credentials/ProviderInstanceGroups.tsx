// ProviderInstanceGroups.tsx: the provider-grouped instance listing shared by
// the credentials section's editor (interactive rows) and the host-scoped view
// of a remote host's own listing (read-only rows). Grouping, the provider
// header label, and the list chrome live here once, so the two surfaces render
// the same listing and the same group CSS and cannot drift; the only difference
// is whether a row is tappable.
import type { InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import { groupByProvider } from "@evener/appwire-client";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { InstanceRow } from "./InstanceRow";
import { supportsHostDeviceSignIn } from "./oauthFlow";
import styles from "./ProviderInstanceGroups.module.css";

const CLASS = {
  groups: requireClass(styles.groups, "ProviderInstanceGroups.module.css", "groups"),
  group: requireClass(styles.group, "ProviderInstanceGroups.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "ProviderInstanceGroups.module.css", "groupHeader"),
  list: requireClass(styles.list, "ProviderInstanceGroups.module.css", "list"),
};

export interface ProviderInstanceGroupsProps {
  instances: InstanceEntry[];
  availableProviders: ProviderDescriptor[];
  /** Read-only rows: the host-scoped view of a remote host's own listing, where
   * nothing is actionable from this browser. */
  readOnly?: boolean;
  /** Called with the instance name when a row is selected. Required whenever
   * readOnly is not set. */
  onSelect?: (name: string) => void;
  /** Remote scope only: begins a device-code sign-in ON the selected host for a
   * Codex instance (component 07d's "Sign in on host"). Absent for the
   * controller's own listing, and never offered on a provider whose sign-in
   * needs a browser the remote host does not have. */
  onHostSignIn?: (name: string) => void;
}

export function ProviderInstanceGroups({
  instances,
  availableProviders,
  readOnly = false,
  onSelect,
  onHostSignIn,
}: ProviderInstanceGroupsProps) {
  const groups = groupByProvider(instances);
  return (
    <div className={CLASS.groups}>
      {groups.map((group) => (
        <div key={group.providerId} className={CLASS.group}>
          {/* `name || id`, the same label the Add dialog gives a provider - one
              pane must not name a provider two ways. */}
          <div className={CLASS.groupHeader}>
            {availableProviders.find((provider) => provider.id === group.providerId)?.name || group.providerId}
          </div>
          <ul className={CLASS.list}>
            {group.instances.map((instance) =>
              readOnly ? (
                <InstanceRow
                  key={instance.name}
                  instance={instance}
                  readOnly
                  onHostSignIn={
                    onHostSignIn !== undefined && supportsHostDeviceSignIn(instance) ? onHostSignIn : undefined
                  }
                />
              ) : (
                <InstanceRow key={instance.name} instance={instance} onSelect={() => onSelect?.(instance.name)} />
              ),
            )}
          </ul>
        </div>
      ))}
    </div>
  );
}
