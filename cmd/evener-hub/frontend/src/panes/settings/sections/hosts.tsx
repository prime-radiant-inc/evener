import { friendlyErrorMessage, type HostRow } from "@evener/appwire-client";
import { useState } from "react";
import { hostsStore, useHostsStore } from "../../../stores/hosts";
import { Button, Chip, ConfirmDialog, Dialog, EmptyState, FormRow, Input, Skeleton, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hosts.module.css";
import { Code } from "./settingsField";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "hosts.module.css", "root"),
  help: requireClass(styles.help, "hosts.module.css", "help"),
  error: requireClass(styles.error, "hosts.module.css", "error"),
  list: requireClass(styles.list, "hosts.module.css", "list"),
  row: requireClass(styles.row, "hosts.module.css", "row"),
  rowName: requireClass(styles.rowName, "hosts.module.css", "rowName"),
  rowDetail: requireClass(styles.rowDetail, "hosts.module.css", "rowDetail"),
  rowActions: requireClass(styles.rowActions, "hosts.module.css", "rowActions"),
  form: requireClass(styles.form, "hosts.module.css", "form"),
  formError: requireClass(styles.formError, "hosts.module.css", "formError"),
};

export interface HostsSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

function stateChip(row: HostRow, connecting: boolean) {
  if (row.removed) return <Chip tone="neutral">removed</Chip>;
  // A server-reported in-progress attach (midAttach) renders like the local
  // connecting state: the row is mid-attach, not offline.
  if (connecting || row.midAttach) return <Chip tone="attention">connecting</Chip>;
  if (row.attached) return <Chip tone="alive">online</Chip>;
  return <Chip tone="neutral">offline</Chip>;
}

function rowDetail(row: HostRow): string | null {
  const parts: string[] = [];
  if (row.address) parts.push(row.address);
  if (row.attached && row.hubVersion) parts.push(`hub ${row.hubVersion}`);
  if (row.attached && (row.os || row.arch)) parts.push([row.os, row.arch].filter(Boolean).join("/"));
  parts.push(row.origin === "hub.toml" ? "hub.toml" : "added in UI");
  if (row.lastAttachError) parts.push(row.lastAttachError);
  return parts.length > 0 ? parts.join(" · ") : null;
}

/**
 * Settings -> Hosts (component 08 slice 1): the host registry surface. Rows
 * come from evener/host/list with truthful online state (attached rows read
 * the live channel, offline rows render last-known state, never dialing);
 * Add opens the name/address/key dialog over evener/host/add; Connect drives
 * the same evener/host/attach the spawn picker uses, with a retry affordance
 * on failure; Remove confirms over evener/host/remove. hub.toml-declared
 * rows cannot be removed here — the confirm explains to edit the file.
 */
export function HostsSection(_props: HostsSectionProps) {
  const load = useHostsStore((s) => s.load);
  const toasts = useToasts();
  const [addOpen, setAddOpen] = useState(false);
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [keyPath, setKeyPath] = useState("");
  const [addError, setAddError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [connecting, setConnecting] = useState<ReadonlySet<string>>(() => new Set());
  const [pendingRemove, setPendingRemove] = useState<HostRow | null>(null);
  const [removing, setRemoving] = useState(false);

  useConnectedEffect(() => hostsStore.getState().fetch(), []);

  async function handleAdd(): Promise<void> {
    setAdding(true);
    setAddError(null);
    try {
      await hostsStore.getState().add({ name: name.trim(), address: address.trim(), keyPath: keyPath.trim() });
      setAddOpen(false);
      setName("");
      setAddress("");
      setKeyPath("");
      toasts.push("success", "Host added");
    } catch (err) {
      setAddError(friendlyErrorMessage(err));
    } finally {
      setAdding(false);
    }
  }

  async function handleConnect(row: HostRow): Promise<void> {
    if (connecting.has(row.name)) return;
    setConnecting((current) => new Set(current).add(row.name));
    try {
      await hostsStore.getState().connect(row.name);
      toasts.push("success", `Connected to ${row.name}`);
    } catch (err) {
      // The row keeps its Connect button: the failure toast names the error
      // and the user retries by pressing Connect again.
      toasts.push("error", `Connect ${row.name} failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setConnecting((current) => {
        if (!current.has(row.name)) return current;
        const next = new Set(current);
        next.delete(row.name);
        return next;
      });
    }
  }

  async function handleRemove(): Promise<void> {
    if (pendingRemove === null) return;
    const row = pendingRemove;
    setRemoving(true);
    try {
      await hostsStore.getState().remove(row.name);
      setPendingRemove(null);
      toasts.push("success", `Removed ${row.name}`);
    } catch (err) {
      toasts.push("error", `Remove ${row.name} failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setRemoving(false);
    }
  }

  if (load.phase === "loading") return <Skeleton lines={3} />;
  if (load.phase === "error") {
    return (
      <EmptyState
        title="Couldn't load hosts"
        hint={load.message}
        action={
          <Button size="sm" onClick={() => void hostsStore.getState().fetch()}>
            Retry
          </Button>
        }
      />
    );
  }

  return (
    <div className={CLASS.root}>
      <p className={CLASS.help}>
        Remote hubs this controller can attach to. Add a host with its SSH address, Connect to bring it online, or
        remove it. Hosts declared in <Code>hub.toml</Code> are read-only here — edit the file to change them.
      </p>
      <div>
        <Button
          size="sm"
          onClick={() => {
            setAddOpen(true);
            setAddError(null);
          }}
        >
          Add host
        </Button>
      </div>
      {load.hosts.length === 0 ? (
        <EmptyState title="No hosts yet" hint="Add a host to attach this controller to a remote hub." />
      ) : (
        <ul className={CLASS.list}>
          {load.hosts.map((row) => {
            // The server-reported midAttach rides the local connecting state:
            // an attach already in progress renders the same chip and keeps
            // Connect disabled until it settles, instead of showing an enabled
            // Connect button on a row the server says is mid-attach.
            const isConnecting = connecting.has(row.name) || row.midAttach;
            const detail = rowDetail(row);
            return (
              <li key={row.name} className={CLASS.row}>
                <span className={CLASS.rowName}>{row.name}</span>
                {stateChip(row, isConnecting)}
                {detail !== null && <span className={CLASS.rowDetail}>{detail}</span>}
                <span className={CLASS.rowActions}>
                  {!row.attached && !row.removed && (
                    <Button size="sm" variant="quiet" disabled={isConnecting} onClick={() => void handleConnect(row)}>
                      {isConnecting ? "Connecting…" : "Connect"}
                    </Button>
                  )}
                  {row.origin === "sidecar" && !row.removed && (
                    <Button size="sm" variant="quiet" onClick={() => setPendingRemove(row)}>
                      Remove
                    </Button>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <Dialog
        open={addOpen}
        onClose={() => {
          if (!adding) setAddOpen(false);
        }}
        title="Add host"
        footer={
          <>
            <Button variant="quiet" disabled={adding} onClick={() => setAddOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              disabled={adding || name.trim() === "" || address.trim() === ""}
              onClick={() => void handleAdd()}
            >
              {adding ? "Adding…" : "Add host"}
            </Button>
          </>
        }
      >
        <div className={CLASS.form}>
          <FormRow label="Name" htmlFor="hosts-add-name" help="The source ID used in refs and URLs.">
            <Input id="hosts-add-name" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </FormRow>
          <FormRow
            label="SSH address"
            htmlFor="hosts-add-address"
            help="SSH destination, e.g. host.example or user@host.example."
          >
            <Input
              id="hosts-add-address"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              autoComplete="off"
            />
          </FormRow>
          <FormRow
            label="Key path"
            htmlFor="hosts-add-key"
            help="Optional SSH private-key path used when dialing this host."
          >
            <Input id="hosts-add-key" value={keyPath} onChange={(e) => setKeyPath(e.target.value)} autoComplete="off" />
          </FormRow>
          {addError !== null && (
            <p className={CLASS.formError} role="alert">
              {addError}
            </p>
          )}
        </div>
      </Dialog>
      <ConfirmDialog
        open={pendingRemove !== null}
        title={pendingRemove !== null ? `Remove ${pendingRemove.name}?` : "Remove host?"}
        confirmLabel="Remove"
        busy={removing}
        onConfirm={() => void handleRemove()}
        onCancel={() => {
          if (!removing) setPendingRemove(null);
        }}
      >
        {pendingRemove !== null && pendingRemove.origin === "hub.toml"
          ? "This host is declared in hub.toml and cannot be removed here. Edit the file to remove it."
          : `Remove "${pendingRemove?.name}"? Its channel drops and the entry is gone until re-added. Sessions on the remote host itself are untouched.`}
      </ConfirmDialog>
    </div>
  );
}
