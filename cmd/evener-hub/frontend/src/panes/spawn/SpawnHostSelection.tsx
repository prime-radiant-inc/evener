import { Button, Card, ListRow } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./SpawnHostSelection.module.css";

/** One launch source as the host list renders it. */
export interface SpawnHostOption {
  id: string;
  label: string;
  online: boolean;
}

export interface SpawnHostSelectionProps {
  options: readonly SpawnHostOption[];
  value: string;
  connecting: ReadonlySet<string>;
  onSelect: (id: string) => void;
  onConnect: (id: string) => void;
  onConnectHost: () => void;
}

const CLASS = {
  root: requireClass(styles.root, "SpawnHostSelection.module.css", "root"),
  list: requireClass(styles.list, "SpawnHostSelection.module.css", "list"),
  separator: requireClass(styles.separator, "SpawnHostSelection.module.css", "separator"),
};

/**
 * The one host list shared by the desktop quiet-row disclosure and the phone
 * bottom Sheet: online sources are choices, an offline source keeps its Connect
 * action on the same row, and the last row opens the hosts settings for a host
 * the hub does not list yet.
 */
export function SpawnHostSelection({
  options,
  value,
  connecting,
  onSelect,
  onConnect,
  onConnectHost,
}: SpawnHostSelectionProps) {
  return (
    <Card variant="outlined">
      <fieldset className={CLASS.root} aria-label="Host options">
        <ul className={CLASS.list}>
          {options.map((option) => {
            if (!option.online) {
              const pending = connecting.has(option.id);
              return (
                <li key={option.id}>
                  <ListRow
                    variant="plain"
                    label={option.label}
                    meta="Offline"
                    action={
                      <Button
                        variant="quiet"
                        size="xs"
                        type="button"
                        aria-label={pending ? `Connecting ${option.label}…` : `Connect ${option.label}`}
                        disabled={pending}
                        onClick={() => onConnect(option.id)}
                      >
                        {pending ? "Connecting…" : "Connect"}
                      </Button>
                    }
                  />
                </li>
              );
            }
            return (
              <li key={option.id}>
                <ListRow
                  variant="plain"
                  label={option.label}
                  aria-current={option.id === value ? "true" : undefined}
                  onSelect={() => onSelect(option.id)}
                />
              </li>
            );
          })}
        </ul>
        <hr className={CLASS.separator} />
        <ListRow variant="plain" label="Connect host" onSelect={onConnectHost} />
      </fieldset>
    </Card>
  );
}
