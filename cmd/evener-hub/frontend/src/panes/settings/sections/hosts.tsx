import { friendlyErrorMessage, type HostEntry, type HostRow, hostFieldError } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { hostsStore, useHostsStore } from "../../../stores/hosts";
import {
  Button,
  Chip,
  ConfirmDialog,
  Dialog,
  EmptyState,
  FormRow,
  Input,
  Skeleton,
  Textarea,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hosts.module.css";
import { Code } from "./settingsField";
import { useConnectedEffect } from "./useConnectedEffect";

// How often the section re-reads host rows while it is mounted (the poll
// below). Matches the HubResidents section's poll cadence.
export const HOST_POLL_MS = 2000;

const CLASS = {
  root: requireClass(styles.root, "hosts.module.css", "root"),
  help: requireClass(styles.help, "hosts.module.css", "help"),
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

type HostDialogState = { mode: "add" } | { mode: "edit"; row: HostRow } | null;

function stateChip(row: HostRow, connecting: boolean) {
  if (row.removed) return <Chip tone="neutral">removed</Chip>;
  // connecting is the row's combined in-progress state, derived once by the
  // caller: a locally triggered attach or a server-reported midAttach.
  if (connecting) return <Chip tone="attention">connecting</Chip>;
  if (row.attached) return <Chip tone="alive">online</Chip>;
  return <Chip tone="neutral">offline</Chip>;
}

function rowDetail(row: HostRow): string | null {
  const parts: string[] = [];
  if (row.address) parts.push(row.address);
  // Version/OS/arch are the last-known facts the wire contract keeps on
  // offline rows, so they render detached too — the state chip carries the
  // live/offline state, and dropping them here would hide facts the moment
  // a host goes offline.
  if (row.hubVersion) parts.push(`hub ${row.hubVersion}`);
  if (row.os || row.arch) parts.push([row.os, row.arch].filter(Boolean).join("/"));
  // The row's origin names the file it lives in: the machine-managed hub.toml
  // for every host (registry spec 08 §6). Render the server's own value so the
  // pane cannot drift from the wire's answer.
  if (row.origin) parts.push(row.origin);
  if (row.lastAttachError) parts.push(row.lastAttachError);
  return parts.length > 0 ? parts.join(" · ") : null;
}

/**
 * Settings -> Hosts (component 08 slice 1): the host registry surface. Rows
 * come from evener/host/list with truthful online state (attached rows read
 * the live channel, offline rows render last-known state, never dialing);
 * Add opens the full-entry Add/Edit dialog over evener/host/add, and a row's
 * Edit reopens the same dialog over evener/host/update with the name held
 * fixed — it is the immutable target, so edit mode only ever changes the
 * entry's other fields; Connect drives the same evener/host/attach the spawn
 * picker uses, with a retry affordance on failure; Remove confirms over
 * evener/host/remove. Every host lives in the machine-managed hub.toml and is
 * editable and removable here; the file's banner tells the operator that the
 * hub rewrites it in place and that comments and formatting are not
 * preserved.
 */
export function HostsSection(_props: HostsSectionProps) {
  const load = useHostsStore((s) => s.load);
  const toasts = useToasts();
  const [dialog, setDialog] = useState<HostDialogState>(null);
  const [connecting, setConnecting] = useState<ReadonlySet<string>>(() => new Set());
  const [pendingRemove, setPendingRemove] = useState<HostRow | null>(null);
  const [removing, setRemoving] = useState(false);

  useConnectedEffect(() => hostsStore.getState().fetch(), []);

  // Re-read while the section is mounted: host rows change out-of-band — an
  // external attach completes unobserved, the SSH supervisor's background
  // reconnect settles, a detached host's channel drops — and no controller-side
  // host lifecycle notification exists on the wire to subscribe to
  // (evener/host/notification relays the remote hub's own config
  // notifications, not this controller's attach lifecycle), so the pane must
  // poll. Gating the poll on a row reporting mid-attach alone would leave
  // the other out-of-band transitions stale: a detached host would stay
  // "online" with no Connect button until some local action re-read. The quiet refresh never
  // flashes the loading state and keeps the last rows on failure, so the idle
  // poll is invisible; the poll is owned by the component like HubResidents'
  // and stops when the section unmounts.
  useEffect(() => {
    const id = setInterval(() => void hostsStore.getState().refresh(), HOST_POLL_MS);
    return () => clearInterval(id);
  }, []);

  async function handleAdd(entry: HostEntry): Promise<void> {
    await hostsStore.getState().add(entry);
    toasts.push("success", `Added ${entry.name}`);
  }

  async function handleEdit(row: HostRow, entry: HostEntry): Promise<void> {
    await hostsStore.getState().update({ name: row.name, entry });
    toasts.push("success", `Updated ${row.name}`);
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
        remove it. Hosts live in <Code>hub.toml</Code>, which the hub manages and rewrites in place — comments and
        formatting in that file are not preserved.
      </p>
      <div>
        <Button
          size="sm"
          onClick={() => {
            setDialog({ mode: "add" });
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
                  {!row.removed && (
                    <Button
                      size="sm"
                      variant="quiet"
                      onClick={() => {
                        setDialog({ mode: "edit", row });
                      }}
                    >
                      Edit
                    </Button>
                  )}
                  {!row.removed && (
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
      {dialog !== null && (
        <HostEntryDialog
          // The key is what makes an edit dialog start from its row: a dialog
          // opened for a different host (or for Add after an Edit) is a fresh
          // mount with fresh field state, never the previous host's values.
          key={dialog.mode === "edit" ? `edit:${dialog.row.name}` : "add"}
          mode={dialog.mode}
          row={dialog.mode === "edit" ? dialog.row : undefined}
          onClose={() => setDialog(null)}
          onSubmit={async (entry) => {
            if (dialog.mode === "edit") await handleEdit(dialog.row, entry);
            else await handleAdd(entry);
          }}
        />
      )}
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
        {`Remove "${pendingRemove?.name}"? Its channel drops and the entry is gone until re-added. Sessions on the remote host itself are untouched.`}
      </ConfirmDialog>
    </div>
  );
}

// rootsFromText parses the roots field: one root per line, trimmed, with blank
// lines dropped — so a stray blank line is not an empty root the hub refuses
// (hostreg's ErrEmptyRoot). The field is a Textarea rather than the settings
// cluster's browse-assisted PathListEditor because these paths live on the
// REMOTE host: a controller-side directory picker would offer the wrong
// machine's directories.
function rootsFromText(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

function rootsToText(roots: readonly string[] | undefined): string {
  return (roots ?? []).join("\n");
}

// HostEntryDialog is the one Add/Edit dialog (spec §3.5, §13): every mutable
// HostConfig field under the wire's own spelling — the spelling a refusal's
// field uses, so a message lands on the input it names — plus slice 1's Key path
// control. Add carries the name; Edit does not, because a name is immutable and
// the host being edited is named in the title. A refusal the hub blames on an
// input is placed in that row's own error slot; anything else is form-level.
interface HostEntryDialogProps {
  mode: "add" | "edit";
  row?: HostRow;
  onClose: () => void;
  onSubmit: (entry: HostEntry) => Promise<void>;
}

function HostEntryDialog({ mode, row, onClose, onSubmit }: HostEntryDialogProps) {
  const [name, setName] = useState("");
  const [address, setAddress] = useState(row?.address ?? "");
  const [user, setUser] = useState(row?.user ?? "");
  const [keyPath, setKeyPath] = useState(row?.keyPath ?? "");
  const [evenerPath, setEvenerPath] = useState(row?.evenerPath ?? "");
  const [configPath, setConfigPath] = useState(row?.configPath ?? "");
  const [addr, setAddr] = useState(row?.addr ?? "");
  const [roots, setRoots] = useState(rootsToText(row?.roots));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ field: string | null; message: string } | null>(null);

  // A backend refusal may name an input this mode deliberately omits (notably
  // immutable name in Edit). Only rendered fields get an inline slot; every
  // other blamed field falls back to the form-level alert instead of vanishing.
  const renderedFields = new Set<string>([
    ...(mode === "add" ? ["name"] : []),
    "address",
    "user",
    "keyPath",
    "evenerPath",
    "configPath",
    "addr",
    "roots",
  ]);

  const fieldError = (field: string): string | undefined =>
    error !== null && error.field === field ? error.message : undefined;
  const formError = error !== null && error.field === null ? error.message : null;
  // Submission stays available for invalid values so the hub remains the one
  // validator and can blame the precise input. It is disabled only in flight.

  async function handleSubmit(): Promise<void> {
    setBusy(true);
    setError(null);
    try {
      await onSubmit({
        ...(mode === "add" ? { name: name.trim() } : {}),
        address: address.trim(),
        user: user.trim(),
        keyPath: keyPath.trim(),
        evenerPath: evenerPath.trim(),
        configPath: configPath.trim(),
        addr: addr.trim(),
        roots: rootsFromText(roots),
      });
      onClose();
    } catch (err) {
      // Remove's posture, carried over: the failure is shown, never swallowed.
      // A refusal that names an input goes on that input; everything else is
      // form-level.
      const field = hostFieldError(err);
      setError({
        field: field !== undefined && renderedFields.has(field) ? field : null,
        message: friendlyErrorMessage(err),
      });
    } finally {
      setBusy(false);
    }
  }

  const prefix = `hosts-${mode}`;
  return (
    <Dialog
      open
      onClose={() => {
        if (!busy) onClose();
      }}
      title={mode === "add" ? "Add host" : `Edit ${row?.name ?? ""}`.trim()}
      footer={
        <>
          <Button variant="quiet" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy} onClick={() => void handleSubmit()}>
            {busy ? "Saving…" : mode === "add" ? "Add host" : "Save"}
          </Button>
        </>
      }
    >
      <div className={CLASS.form}>
        {mode === "add" && (
          <FormRow
            label="Name"
            htmlFor={`${prefix}-name`}
            help="The source ID used in refs and URLs. A name is fixed once the host exists."
            error={fieldError("name")}
          >
            <Input id={`${prefix}-name`} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
          </FormRow>
        )}
        <FormRow
          label="SSH address"
          htmlFor={`${prefix}-address`}
          help="SSH destination, e.g. host.example or user@host.example."
          error={fieldError("address")}
        >
          <Input
            id={`${prefix}-address`}
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            autoComplete="off"
          />
        </FormRow>
        <FormRow
          label="User"
          htmlFor={`${prefix}-user`}
          help="Optional SSH user; leave empty when the address already names one."
          error={fieldError("user")}
        >
          <Input id={`${prefix}-user`} value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Key path"
          htmlFor={`${prefix}-key`}
          help="Optional SSH private-key path used when dialing this host."
          error={fieldError("keyPath")}
        >
          <Input id={`${prefix}-key`} value={keyPath} onChange={(e) => setKeyPath(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Evener path"
          htmlFor={`${prefix}-evener`}
          help="Optional path to the evener binary on the host, when it is not on PATH."
          error={fieldError("evenerPath")}
        >
          <Input
            id={`${prefix}-evener`}
            value={evenerPath}
            onChange={(e) => setEvenerPath(e.target.value)}
            autoComplete="off"
          />
        </FormRow>
        <FormRow
          label="Hub config path"
          htmlFor={`${prefix}-config`}
          help="Optional path to the host's hub.toml, when it is not the default."
          error={fieldError("configPath")}
        >
          <Input
            id={`${prefix}-config`}
            value={configPath}
            onChange={(e) => setConfigPath(e.target.value)}
            autoComplete="off"
          />
        </FormRow>
        <FormRow
          label="Hub address"
          htmlFor={`${prefix}-addr`}
          help="Optional listen address of the host's hub, when it is not the default."
          error={fieldError("addr")}
        >
          <Input id={`${prefix}-addr`} value={addr} onChange={(e) => setAddr(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label="Roots"
          htmlFor={`${prefix}-roots`}
          help="Optional directories on the host to serve. One per line."
          error={fieldError("roots")}
        >
          <Textarea id={`${prefix}-roots`} value={roots} onChange={(e) => setRoots(e.target.value)} rows={3} />
        </FormRow>
        {formError !== null && (
          <p className={CLASS.formError} role="alert">
            {formError}
          </p>
        )}
      </div>
    </Dialog>
  );
}
