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

/** The two variants, as a type rather than a comment - the same contract
 * InstanceRow enforces for one row: a listing that is not read-only MUST carry
 * onSelect (or its rows render as full-width buttons that do nothing), and a
 * read-only listing has nothing to select.
 *
 * onHostSignIn belongs to the READ-ONLY variant alone, for the same reason: a
 * listing that is not read-only is THIS hub's own, which has no host to sign in
 * on - so the interactive variant does not accept the prop at all (`?: never`),
 * rather than accepting one it could only ignore. */
export type ProviderInstanceGroupsProps = {
  instances: InstanceEntry[];
  availableProviders: ProviderDescriptor[];
} & (
  | {
      readOnly: true;
      /** Remote scope only: begins a device-code sign-in ON the selected host
       * for a Codex instance (component 07d's "Sign in on host"). Absent for the
       * controller's own listing, and never offered on a provider whose sign-in
       * needs a browser the remote host does not have. */
      onHostSignIn?: (name: string) => void;
    }
  | { readOnly?: false; onSelect: (name: string) => void; onHostSignIn?: never }
);

export function ProviderInstanceGroups(props: ProviderInstanceGroupsProps) {
  const { instances, availableProviders } = props;
  const groups = groupByProvider(instances);
  // The read-only variant has no onSelect to call, and the type guarantees the
  // other variant has one - so an interactive row can never render a dead button.
  const onSelect = props.readOnly === true ? null : props.onSelect;
  // Read from the variant that has it: the type keeps an interactive listing
  // from passing one at all (see ProviderInstanceGroupsProps).
  const onHostSignIn = props.readOnly === true ? props.onHostSignIn : undefined;
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
              onSelect === null ? (
                <InstanceRow
                  key={instance.name}
                  instance={instance}
                  readOnly
                  onHostSignIn={
                    onHostSignIn !== undefined && supportsHostDeviceSignIn(instance) ? onHostSignIn : undefined
                  }
                />
              ) : (
                <InstanceRow key={instance.name} instance={instance} onSelect={() => onSelect(instance.name)} />
              ),
            )}
          </ul>
        </div>
      ))}
    </div>
  );
}
