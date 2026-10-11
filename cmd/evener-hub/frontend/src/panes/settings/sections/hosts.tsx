import {
  friendlyErrorMessage,
  HOST_CHANGED_MESSAGE,
  HOST_ENTRY_FIELD_TEXT,
  type HostEntry,
  type HostRow,
  hostChangedSinceOpened,
  hostFieldError,
  rootsFromText,
  rootsToText,
} from "@evener/appwire-client";
import { useEffect, useState } from "react";
import {
  clearedOutcomeLine,
  deployRefusalAction,
  type HostOpRecovery,
  hostOperationView,
  hostOpsStore,
  operationNeedsRead,
  operationReadPending,
  operationShownOnHost,
  planNoTokenAction,
  restartRefusalAction,
  retryArmResolved,
  retryOutcomeLine,
  TEARDOWN_RECOVER_STATEMENT,
  teardownRecoverRefusalAction,
  teardownRetryRefusalAction,
  useHostOpsStore,
} from "../../../stores/hostOps";
import { hostsStore, useHostsStore } from "../../../stores/hosts";
import {
  Button,
  Chip,
  ConfirmDialog,
  Dialog,
  EmptyState,
  FormRow,
  Input,
  Loader,
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

// How often the section reads a tracked operation's record while it is
// mounted (S15). The read is a cheap controller-local store read that never
// dials (08b §6, §10), and an operation runs for minutes, so one second keeps
// the row's progress line fresh without hammering the hub; unlike the row poll
// there is no separate slow cadence to settle for, because the read is exact
// and bounded (one record by id).
export const OPERATION_POLL_MS = 1000;

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
  planFields: requireClass(styles.planFields, "hosts.module.css", "planFields"),
  planField: requireClass(styles.planField, "hosts.module.css", "planField"),
  planLabel: requireClass(styles.planLabel, "hosts.module.css", "planLabel"),
  planValue: requireClass(styles.planValue, "hosts.module.css", "planValue"),
  planNotice: requireClass(styles.planNotice, "hosts.module.css", "planNotice"),
  rowOperation: requireClass(styles.rowOperation, "hosts.module.css", "rowOperation"),
  rowOperationError: requireClass(styles.rowOperationError, "hosts.module.css", "rowOperationError"),
  rowRepair: requireClass(styles.rowRepair, "hosts.module.css", "rowRepair"),
  rowRepairNotice: requireClass(styles.rowRepairNotice, "hosts.module.css", "rowRepairNotice"),
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
  // The tracked operations, keyed by host name: each row renders its own
  // operation's progress/terminal state (S15).
  const operations = useHostOpsStore((s) => s.operations);
  const toasts = useToasts();
  const [dialog, setDialog] = useState<HostDialogState>(null);
  const [connecting, setConnecting] = useState<ReadonlySet<string>>(() => new Set());
  const [pendingRemove, setPendingRemove] = useState<HostRow | null>(null);
  const [removing, setRemoving] = useState(false);
  // The row each deploy-pipeline dialog was opened for. Non-null mounts the
  // dialog; the row is the snapshot the operator acted on, which is exactly
  // the intended (generation, incarnation id) pair Restart carries.
  const [deployTarget, setDeployTarget] = useState<HostRow | null>(null);
  const [restartTarget, setRestartTarget] = useState<HostRow | null>(null);
  // The NAME whose remnant repair dialog is open, and the action it started
  // from (the escalated row's recover affordance opens the attestation form).
  // Only the name is captured: the dialog renders the LIVE row, so a
  // poll-driven escalation or a changed remnant reaches it without a reload.
  const [repairTarget, setRepairTarget] = useState<{ name: string; action: "retry" | "recover" } | null>(null);

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

  // The operation poll (S15): every tracked operation that still owes a read is
  // read once per tick, so its row renders progress through the terminal state
  // (registry spec 08 §13, deploy-pipeline spec 08b §6). A settled record is
  // skipped — terminal state ends the loop — while a terminal seed from a
  // replay is still read once for the retained body. Unmount clears the
  // interval and cancels any in-flight read's publish.
  useEffect(() => {
    const id = setInterval(() => {
      const tracked = hostOpsStore.getState().operations;
      for (const name of Object.keys(tracked)) {
        const operation = tracked[name];
        // A ref suppressed from the row (an older incarnation after a
        // same-name re-add) is still read to its terminal state: render
        // suppression is the row's decision, and stopping the poll here would
        // leave the ref non-terminal forever.
        if (operation === undefined || !operationNeedsRead(operation) || operationReadPending(name, operation.id)) {
          continue;
        }
        void hostOpsStore.getState().pollOperation(name);
      }
    }, OPERATION_POLL_MS);
    return () => {
      clearInterval(id);
      for (const name of Object.keys(hostOpsStore.getState().operations)) {
        hostOpsStore.getState().stopOperationPoll(name);
      }
    };
  }, []);

  // A repair dialog can only render for a live row that still names a remnant:
  // when either goes away, drop the target so a LATER remnant on the name never
  // auto-opens a dialog the operator did not ask for. (The dialog's own unmount
  // cleanup drops its repair entry.)
  const repairTargetName = repairTarget?.name;
  useEffect(() => {
    if (repairTargetName === undefined) return;
    const row =
      load.phase === "ready" ? load.hosts.find((candidate) => candidate.name === repairTargetName) : undefined;
    if (row === undefined || (row.openRemnantId ?? "") === "") setRepairTarget(null);
  }, [repairTargetName, load]);

  async function handleAdd(entry: HostEntry): Promise<void> {
    await hostsStore.getState().add(entry);
    toasts.push("success", `Added ${entry.name}`);
  }

  // row is the snapshot the dialog opened on, so the edit echoes the pair of
  // the row the person saw, never a newer one a poll brought in since.
  async function handleEdit(row: HostRow, entry: HostEntry): Promise<void> {
    await hostsStore.getState().update({
      name: row.name,
      entry,
      expected: { generation: row.generation, incarnationId: row.incarnationId },
    });
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

  // The live row the repair dialog renders: resolved by name on every render,
  // so the poll's escalation/remnant changes reach an open dialog, and a row
  // whose remnant disappeared unmounts the dialog honestly.
  const repairRow =
    repairTarget === null ? undefined : load.hosts.find((candidate) => candidate.name === repairTarget.name);

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
            const operation = operations[row.name];
            const operationView =
              operation === undefined || !operationShownOnHost(operation, row) ? null : hostOperationView(operation);
            // The remnant repair affordance (registry spec 08 §13): a row whose
            // name holds an open remnant carries teardown-retry (escalating to
            // teardown-recover past the bound).
            const remnantId = row.openRemnantId ?? "";
            return (
              <li key={row.name} className={CLASS.row}>
                <span className={CLASS.rowName}>{row.name}</span>
                {stateChip(row, isConnecting)}
                {detail !== null && <span className={CLASS.rowDetail}>{detail}</span>}
                {operationView !== null && (
                  // The operation's own row carries its progress/terminal
                  // state (registry spec 08 §13): the chip names the state, the
                  // line is the record's own prose — a failure's 04b message
                  // verbatim — and a stopped read says so instead of stalling.
                  <span className={CLASS.rowOperation}>
                    <Chip tone={operationView.tone}>{operationView.label}</Chip>
                    {operationView.line !== null && <span className={CLASS.rowDetail}>{operationView.line}</span>}
                    {operationView.replay !== null && <span className={CLASS.rowDetail}>{operationView.replay}</span>}
                    {operationView.unavailable !== null && (
                      <span className={CLASS.rowOperationError} role="alert">
                        {operationView.unavailable}
                      </span>
                    )}
                  </span>
                )}
                {remnantId !== "" && (
                  <span className={CLASS.rowRepair}>
                    <Button
                      size="sm"
                      variant="quiet"
                      onClick={() => {
                        setRepairTarget({ name: row.name, action: "retry" });
                      }}
                    >
                      Teardown retry
                    </Button>
                    {row.escalationAgeSec !== undefined && (
                      <Button
                        size="sm"
                        variant="quiet"
                        onClick={() => {
                          setRepairTarget({ name: row.name, action: "recover" });
                        }}
                      >
                        Recover remnant
                      </Button>
                    )}
                  </span>
                )}
                <span className={CLASS.rowActions}>
                  {!row.attached && !row.removed && (
                    <Button size="sm" variant="quiet" disabled={isConnecting} onClick={() => void handleConnect(row)}>
                      {isConnecting ? "Connecting…" : "Connect"}
                    </Button>
                  )}
                  {/* Deploy and Restart render on every row: a detached live
                      host is a legitimate target (plan answers `unattached`
                      with a Connect-first affordance, and restart attach-firsts
                      under its own gate), while a tombstone row disables both
                      and never fires (registry spec 08 §13). */}
                  <Button size="sm" variant="quiet" disabled={row.removed} onClick={() => setDeployTarget(row)}>
                    Deploy
                  </Button>
                  <Button size="sm" variant="quiet" disabled={row.removed} onClick={() => setRestartTarget(row)}>
                    Restart
                  </Button>
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
      {deployTarget !== null && (
        <DeployDialog
          // Keyed per host, like the Add/Edit dialog: a dialog opened for a
          // different host is a fresh mount that plans against its own row.
          key={`deploy:${deployTarget.name}`}
          row={deployTarget}
          onClose={() => {
            hostOpsStore.getState().discardPlan(deployTarget.name);
            // The dialog owns its remnant repair state: closing it drops the
            // arm/refusal it rendered, so a later remnant starts clean.
            hostOpsStore.getState().clearRepair(deployTarget.name);
            setDeployTarget(null);
          }}
        />
      )}
      {restartTarget !== null && (
        <RestartDialog
          key={`restart:${restartTarget.name}`}
          row={restartTarget}
          onClose={() => {
            hostOpsStore.getState().discardRestart(restartTarget.name);
            hostOpsStore.getState().clearRepair(restartTarget.name);
            setRestartTarget(null);
          }}
        />
      )}
      {repairTarget !== null && repairRow !== undefined && (repairRow.openRemnantId ?? "") !== "" && (
        <RemnantRepairDialog
          // Keyed per (name, remnant): a dialog opened for another remnant is a
          // fresh mount, so no stale form state carries across repairs.
          key={`repair:${repairRow.name}:${repairRow.openRemnantId}`}
          row={repairRow}
          initialAction={repairTarget.action}
          onClose={() => {
            hostOpsStore.getState().clearRepair(repairTarget.name);
            setRepairTarget(null);
          }}
        />
      )}
    </div>
  );
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
        message: hostChangedSinceOpened(err) ? HOST_CHANGED_MESSAGE : friendlyErrorMessage(err),
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
          label={HOST_ENTRY_FIELD_TEXT.address.label}
          htmlFor={`${prefix}-address`}
          help={HOST_ENTRY_FIELD_TEXT.address.help}
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
          label={HOST_ENTRY_FIELD_TEXT.user.label}
          htmlFor={`${prefix}-user`}
          help={HOST_ENTRY_FIELD_TEXT.user.help}
          error={fieldError("user")}
        >
          <Input id={`${prefix}-user`} value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label={HOST_ENTRY_FIELD_TEXT.keyPath.label}
          htmlFor={`${prefix}-key`}
          help={HOST_ENTRY_FIELD_TEXT.keyPath.help}
          error={fieldError("keyPath")}
        >
          <Input id={`${prefix}-key`} value={keyPath} onChange={(e) => setKeyPath(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label={HOST_ENTRY_FIELD_TEXT.evenerPath.label}
          htmlFor={`${prefix}-evener`}
          help={HOST_ENTRY_FIELD_TEXT.evenerPath.help}
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
          label={HOST_ENTRY_FIELD_TEXT.configPath.label}
          htmlFor={`${prefix}-config`}
          help={HOST_ENTRY_FIELD_TEXT.configPath.help}
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
          label={HOST_ENTRY_FIELD_TEXT.addr.label}
          htmlFor={`${prefix}-addr`}
          help={HOST_ENTRY_FIELD_TEXT.addr.help}
          error={fieldError("addr")}
        >
          <Input id={`${prefix}-addr`} value={addr} onChange={(e) => setAddr(e.target.value)} autoComplete="off" />
        </FormRow>
        <FormRow
          label={HOST_ENTRY_FIELD_TEXT.roots.label}
          htmlFor={`${prefix}-roots`}
          help={HOST_ENTRY_FIELD_TEXT.roots.help}
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

// --- the deploy-pipeline dialogs (S14) ---------------------------------------

interface HostOpDialogProps {
  row: HostRow;
  onClose: () => void;
}

const RECOVERY_LABEL: Record<Exclude<HostOpRecovery, "none">, string> = {
  replan: "Retry plan",
  retry: "Retry deploy",
  connect: "Connect and plan",
};

function PlanField({ label, value }: { label: string; value: string }) {
  return (
    <div className={CLASS.planField}>
      <span className={CLASS.planLabel}>{label}</span>
      <span className={CLASS.planValue}>{value}</span>
    </div>
  );
}

/**
 * The Deploy confirmation (registry spec 08 §13): opening it calls
 * `evener/host/plan` and renders the controller-minted, token-bound plan;
 * confirming submits exactly that token with a client operation ID, never a
 * plan the operator has not seen. A no-token response branches on its reason
 * (`unattached` Connects first, the retry arms re-plan, `remnant-open` names
 * the blocking remnant with neither affordance, the terminal arms disable the
 * confirmation). A token/stale refusal re-plans in the store, so the dialog
 * re-renders the fresh confirmation with the refusal's own sentence.
 */
function DeployDialog({ row, onClose }: HostOpDialogProps) {
  const name = row.name;
  const state = useHostOpsStore((s) => s.plans[name]);
  const [busy, setBusy] = useState(false);
  const toasts = useToasts();

  useEffect(() => {
    // Opening the dialog calls evener/host/plan (§13). The dialog is keyed per
    // host, so one opening is exactly one plan mint.
    void hostOpsStore.getState().plan(name);
  }, [name]);

  async function submit(action: "deploy" | HostOpRecovery): Promise<void> {
    setBusy(true);
    try {
      if (action === "deploy" || action === "retry") {
        await hostOpsStore.getState().deploy(name);
        if (hostOpsStore.getState().plans[name]?.phase === "started") {
          // 08b §10: a freshly created record reports `pending`; any other state
          // is the existing record a dedup hit answered. A replay may be
          // running, not just terminal, and the toast must not claim a fresh
          // start the row would contradict.
          const record = hostOpsStore.getState().operations[name];
          if (record !== undefined && record.state !== "pending") {
            toasts.push("info", `Deploy ${name} repeated its existing operation (${record.state}).`);
          } else {
            toasts.push("success", `Deploy started for ${name}`);
          }
          onClose();
        }
        return;
      }
      if (action === "replan") {
        await hostOpsStore.getState().plan(name);
        return;
      }
      if (action === "connect") {
        await hostOpsStore.getState().connectAndPlan(name);
      }
    } finally {
      setBusy(false);
    }
  }

  const planned = state?.phase === "planned" ? state : null;
  const noToken = state?.phase === "no-token" ? state : null;
  const refusal = state?.phase === "error" ? state.refusal : (planned?.refusal ?? null);
  let recovery: HostOpRecovery = "none";
  if (state?.phase === "error") recovery = state.recovery;
  else if (planned !== null && planned.refusal !== null) recovery = deployRefusalAction(planned.refusal.kind);
  const noTokenRecovery = noToken !== null ? planNoTokenAction(noToken.staleFacts.reason, noToken.terminal) : "none";

  return (
    <Dialog
      open
      onClose={() => {
        if (!busy) onClose();
      }}
      title={`Deploy ${name}`}
      footer={
        <>
          <Button variant="quiet" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || planned === null || planned.refusal !== null}
            onClick={() => void submit("deploy")}
          >
            {busy ? "Deploying…" : "Deploy"}
          </Button>
        </>
      }
    >
      {state === undefined || state.phase === "planning" ? (
        <Loader label={`Planning deploy for ${name}…`} />
      ) : noToken !== null ? (
        <div className={CLASS.planFields}>
          <p className={CLASS.formError} role="alert">
            {noToken.staleFacts.message}
          </p>
          {noToken.remnantId !== null && (
            <>
              <p className={CLASS.formError}>
                {`Blocking remnant ${noToken.remnantId}: resume it with teardown-retry before deploying.`}
              </p>
              {/* §13: remnant-open surfaces the blocking remnantId with a
                  teardown-retry affordance — never Connect, never re-plan (a
                  re-plan mints nothing while the remnant is open). */}
              <RemnantRepairControls
                // A different remnant is a different repair: a fresh mount
                // resets the collected attestation with it.
                key={`repair-controls:${noToken.remnantId}`}
                name={name}
                remnantId={noToken.remnantId}
                escalated={row.escalationAgeSec !== undefined}
                onContinue={() => void submit("replan")}
              />
            </>
          )}
          {noTokenRecovery !== "none" && (
            <Button size="sm" variant="quiet" disabled={busy} onClick={() => void submit(noTokenRecovery)}>
              {RECOVERY_LABEL[noTokenRecovery]}
            </Button>
          )}
        </div>
      ) : planned !== null ? (
        <div className={CLASS.planFields}>
          {planned.notice !== null && <p className={CLASS.planNotice}>{planned.notice}</p>}
          <PlanField label="Target host" value={planned.plan.host} />
          <PlanField label="Controller revision" value={planned.plan.controllerRevision} />
          <PlanField label="Remote target path" value={planned.plan.targetPath} />
          <PlanField label="Restart follows" value={planned.plan.restartFollows ? "yes" : "no"} />
          <PlanField label="Running build" value={planned.plan.runningVersion} />
          <PlanField
            label="Facts captured"
            value={`${planned.plan.factsCapturedAt} (${planned.plan.factsAgeSec}s ago)`}
          />
          {refusal !== null && (
            <p className={CLASS.formError} role="alert">
              {refusal.message}
            </p>
          )}
          {refusal?.kind === "remnant-open" && refusal.remnantId !== undefined && (
            <RemnantRepairControls
              key={`repair-controls:${refusal.remnantId}`}
              name={name}
              remnantId={refusal.remnantId}
              escalated={row.escalationAgeSec !== undefined}
              onContinue={() => void submit("replan")}
            />
          )}
          {recovery !== "none" && (
            <Button size="sm" variant="quiet" disabled={busy} onClick={() => void submit(recovery)}>
              {RECOVERY_LABEL[recovery]}
            </Button>
          )}
        </div>
      ) : state.phase === "error" ? (
        <div className={CLASS.planFields}>
          <p className={CLASS.formError} role="alert">
            {state.refusal.message}
          </p>
          {state.recovery !== "none" && (
            <Button size="sm" variant="quiet" disabled={busy} onClick={() => void submit(state.recovery)}>
              {RECOVERY_LABEL[state.recovery]}
            </Button>
          )}
        </div>
      ) : (
        <p className={CLASS.planNotice}>Deploy started.</p>
      )}
    </Dialog>
  );
}

/**
 * The Restart confirmation: opening it pins the intended (generation,
 * incarnation id) pair to the row the operator acted on, and confirming opens
 * the restart operation for exactly that pair (08b §6, §10). A stale-entry
 * refusal re-reads the registry and retries once with the pair it answers; a
 * held gate's operation or an open remnant renders its concrete message with
 * the retry disabled (the open/wait and teardown-retry affordances are
 * S15/S16's).
 */
function RestartDialog({ row, onClose }: HostOpDialogProps) {
  const name = row.name;
  const attempt = useHostOpsStore((s) => s.restarts[name]);
  const [busy, setBusy] = useState(false);
  const toasts = useToasts();

  useEffect(() => {
    hostOpsStore.getState().beginRestart(name, { generation: row.generation, incarnationId: row.incarnationId });
  }, [name, row.generation, row.incarnationId]);

  async function finishRestart(): Promise<void> {
    if (hostOpsStore.getState().restarts[name]?.phase === "started") {
      // The replay arm, as in the deploy dialog: a non-pending state is 08b
      // §10's dedup-hit discriminator, never a fresh start.
      const record = hostOpsStore.getState().operations[name];
      if (record !== undefined && record.state !== "pending") {
        toasts.push("info", `Restart ${name} repeated its existing operation (${record.state}).`);
      } else {
        toasts.push("success", `Restart started for ${name}`);
      }
      onClose();
    }
  }

  async function handleRestart(): Promise<void> {
    setBusy(true);
    try {
      await hostOpsStore.getState().restart(name);
      await finishRestart();
    } finally {
      setBusy(false);
    }
  }

  async function handleConnectAndRestart(): Promise<void> {
    setBusy(true);
    try {
      await hostOpsStore.getState().connectAndRestart(name);
    } finally {
      setBusy(false);
    }
  }

  async function handleReSeedRestart(): Promise<void> {
    // The continuation a resolved remnant earns: re-seed the confirmation
    // against the pair the registry now answers, under a fresh operation ID.
    setBusy(true);
    try {
      await hostOpsStore.getState().reSeedRestart(name);
    } finally {
      setBusy(false);
    }
  }

  const refusal = attempt?.refusal ?? null;
  // The recovery the refusal earns (restartRefusalAction): the bare Restart
  // stays disabled exactly when a plain repeat would refuse again or loop -
  // the fenced/busy arms and the detached host, whose way out is Connect.
  const recovery: HostOpRecovery = refusal !== null ? restartRefusalAction(refusal.kind) : "retry";
  const blocked = recovery === "none" || recovery === "connect";
  return (
    <Dialog
      open
      onClose={() => {
        if (!busy) onClose();
      }}
      title={`Restart ${name}?`}
      footer={
        <>
          <Button variant="quiet" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            // No attempt to submit yet (the seeding effect has not run, or the
            // dialog is mid-reopen): restart() is a no-op without one, so the
            // button must not read as an enabled action.
            disabled={busy || attempt === undefined || blocked}
            onClick={() => void handleRestart()}
          >
            {busy ? "Restarting…" : "Restart"}
          </Button>
        </>
      }
    >
      <p className={CLASS.help}>
        {`Restarting "${name}" drops its channel while the hub restarts and reattaches; sessions on the remote host itself are untouched.`}
      </p>
      {refusal !== null && (
        <>
          <p className={CLASS.formError} role="alert">
            {refusal.message}
          </p>
          {refusal.kind === "remnant-open" && refusal.remnantId !== undefined && (
            <RemnantRepairControls
              key={`repair-controls:${refusal.remnantId}`}
              name={name}
              remnantId={refusal.remnantId}
              escalated={row.escalationAgeSec !== undefined}
              onContinue={() => void handleReSeedRestart()}
              continueLabel="Continue restart"
            />
          )}
          {recovery === "connect" && (
            <Button size="sm" variant="quiet" disabled={busy} onClick={() => void handleConnectAndRestart()}>
              Connect and restart
            </Button>
          )}
        </>
      )}
    </Dialog>
  );
}

// --- the remnant repair surfaces (S16) ---------------------------------------

/**
 * RemnantRepairControls renders one name's remnant repair state and its
 * affordances (registry spec 08 §6/§11): the `teardown-retry` action, the
 * escalated `teardown-recover` form with the wire's exact attestation fields
 * (operator / statement / observedAt), the arm or refusal the hub answered —
 * each rendered arm-by-arm, never an aggregate success. Shared by the row's
 * repair dialog and the deploy/restart confirmations' remnant-open arms.
 */
function RemnantRepairControls({
  name,
  remnantId,
  escalated,
  initialRecovering = false,
  onContinue,
  continueLabel = "Plan again",
}: {
  name: string;
  remnantId: string;
  escalated: boolean;
  initialRecovering?: boolean;
  onContinue?: () => void;
  continueLabel?: string;
}) {
  const storedRepair = useHostOpsStore((s) => s.repairs[name]);
  // The stored entry belongs to ONE remnant: a different remnant on the name
  // renders neutral, never the previous remnant's arm, refusal, or outcome.
  const repair = storedRepair !== undefined && storedRepair.remnantId === remnantId ? storedRepair : undefined;
  const [recovering, setRecovering] = useState(initialRecovering);
  const [operator, setOperator] = useState("");
  // The statement starts at the wire's pinned literal; the hub remains the one
  // validator, so an edited value is submitted and refused concretely rather
  // than silently corrected here.
  const [statement, setStatement] = useState(TEARDOWN_RECOVER_STATEMENT);
  // The observation instant the attestation records: prefilled with now (the
  // hub validates it as RFC3339 within its maximum age), editable.
  const [observedAt, setObservedAt] = useState(() => new Date().toISOString());
  const [busy, setBusy] = useState(false);

  const submitting = repair?.phase === "retrying" || repair?.phase === "recovering";
  const resolved = repair?.phase === "cleared" || (repair?.phase === "retried" && retryArmResolved(repair.result));
  const failedArm = repair?.phase === "retried" && !retryArmResolved(repair.result);
  const refusal = repair?.phase === "refused" ? repair.refusal : null;
  // Which repair a refusal came from: a refused recovery resumes the RECOVERY
  // (its collected attestation is the submission), never a bare teardown retry.
  const refusedAction: "retry" | "recover" = repair?.phase === "refused" ? repair.action : "retry";
  const resumeAllowed =
    !submitting &&
    !resolved &&
    (refusal === null ||
      (refusedAction === "recover"
        ? teardownRecoverRefusalAction(refusal.kind) === "retry"
        : teardownRetryRefusalAction(refusal.kind) === "retry"));
  const resumeLabel =
    refusedAction === "recover"
      ? "Retry recovery"
      : refusal !== null || failedArm
        ? "Retry teardown"
        : "Teardown retry";
  // The unknown/purged remnant id has no repair action at all: the only way
  // out is re-grounding on the list, so the surface offers exactly that.
  const unknownRemnant = refusal?.kind === "teardown-unknown-key";
  // The escalation comes from the row's stamp, or from the retry arm itself
  // when it ran past the bound (the removed arms under-stamp the row fields).
  const escalatedNow = escalated || (repair?.phase === "retried" && repair.result.escalationAgeSec !== undefined);
  // The recover escalation obeys the recover's own refusal actions too, and is
  // never offered beside the resume of a refused recovery (that resume IS the
  // recovery, attestation and all).
  const recoverAllowed =
    escalatedNow &&
    !submitting &&
    !resolved &&
    !recovering &&
    (refusal === null || (refusedAction === "retry" && teardownRecoverRefusalAction(refusal.kind) === "retry"));

  async function submitRetry(): Promise<void> {
    setBusy(true);
    try {
      await hostOpsStore.getState().teardownRetry(name, remnantId);
    } finally {
      setBusy(false);
    }
  }

  async function submitRecover(): Promise<void> {
    setBusy(true);
    try {
      await hostOpsStore.getState().teardownRecover(name, remnantId, {
        operator: operator.trim(),
        statement: statement.trim(),
        observedAt: observedAt.trim(),
      });
    } finally {
      setBusy(false);
    }
  }

  async function resumeRefusedAction(): Promise<void> {
    if (refusedAction === "recover") {
      await submitRecover();
      return;
    }
    await submitRetry();
  }

  async function rereadUnknownRemnant(): Promise<void> {
    // The refusal names a remnant id the hub no longer knows: re-read the list
    // so the row converges, and drop the refusal so the operator can act on
    // whatever the list now names instead of closing and reopening into the
    // same refusal.
    setBusy(true);
    try {
      await hostsStore.getState().reReadForced();
      hostOpsStore.getState().clearRepairRefusal(name);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={CLASS.planFields}>
      {repair?.phase === "retrying" && <Loader label={`Retrying teardown for remnant ${remnantId}…`} />}
      {repair?.phase === "recovering" && <Loader label={`Recovering remnant ${remnantId}…`} />}
      {refusal !== null && (
        <p className={CLASS.formError} role="alert">
          {refusal.message}
        </p>
      )}
      {repair?.phase === "retried" && (
        <>
          <p className={CLASS.planNotice}>{retryOutcomeLine(repair.result)}</p>
          {repair.result.hostRemoved && (
            <p className={CLASS.planNotice}>The host remains a removed tombstone until it is re-added.</p>
          )}
        </>
      )}
      {repair?.phase === "cleared" && (
        <>
          <p className={CLASS.planNotice}>{clearedOutcomeLine(repair.result)}</p>
          {repair.result.hostKind === "removed" && (
            <p className={CLASS.planNotice}>The host remains a removed tombstone until it is re-added.</p>
          )}
        </>
      )}

      {recovering && !resolved ? (
        <div className={CLASS.form}>
          <p className={CLASS.planNotice}>
            The audited recovery requires the wire's attestation: operator, statement, and the observed time of the
            out-of-band verification.
          </p>
          <FormRow
            label="Operator"
            htmlFor={`hosts-repair-operator-${name}`}
            help="The authenticated operator making the clearance decision."
          >
            <Input
              id={`hosts-repair-operator-${name}`}
              value={operator}
              onChange={(e) => setOperator(e.target.value)}
              autoComplete="off"
            />
          </FormRow>
          <FormRow
            label="Statement"
            htmlFor={`hosts-repair-statement-${name}`}
            help="The only statement the wire accepts for a cleared remnant."
          >
            <Input
              id={`hosts-repair-statement-${name}`}
              value={statement}
              onChange={(e) => setStatement(e.target.value)}
              autoComplete="off"
            />
          </FormRow>
          <FormRow
            label="Observed at"
            htmlFor={`hosts-repair-observed-${name}`}
            help="When the operator confirmed the remnant's target absent, RFC3339."
          >
            <Input
              id={`hosts-repair-observed-${name}`}
              value={observedAt}
              onChange={(e) => setObservedAt(e.target.value)}
              autoComplete="off"
            />
          </FormRow>
          <div className={CLASS.rowRepair}>
            <Button size="sm" variant="quiet" disabled={busy} onClick={() => setRecovering(false)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void submitRecover()}>
              {busy ? "Recovering…" : "Recover remnant"}
            </Button>
          </div>
        </div>
      ) : (
        <>
          {escalatedNow && !resolved && (
            <p className={CLASS.planNotice}>Past the escalation bound: recovering requires the audited attestation.</p>
          )}
          <div className={CLASS.rowRepair}>
            {resumeAllowed && (
              <Button size="sm" variant="quiet" disabled={busy} onClick={() => void resumeRefusedAction()}>
                {busy ? "Retrying…" : resumeLabel}
              </Button>
            )}
            {recoverAllowed && (
              <Button size="sm" variant="quiet" disabled={busy} onClick={() => setRecovering(true)}>
                Recover remnant
              </Button>
            )}
            {unknownRemnant && (
              <Button size="sm" variant="quiet" disabled={busy} onClick={() => void rereadUnknownRemnant()}>
                Re-read host list
              </Button>
            )}
            {resolved && onContinue !== undefined && (
              <Button size="sm" variant="quiet" disabled={busy} onClick={onContinue}>
                {continueLabel}
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

/** RemnantRepairDialog is the row's repair surface: the shared controls under
 * a dialog, so a slow teardown and its arm have room to render. */
function RemnantRepairDialog({
  row,
  initialAction,
  onClose,
}: {
  row: HostRow;
  initialAction: "retry" | "recover";
  onClose: () => void;
}) {
  const remnantId = row.openRemnantId ?? "";
  // The dialog owns the name's repair entry: every exit drops it, including an
  // unmount because the live row's remnant disappeared (the parent closes over
  // the same clear for its own close path).
  useEffect(() => () => hostOpsStore.getState().clearRepair(row.name), [row.name]);
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Repair remnant ${remnantId}`}
      footer={
        <Button variant="quiet" onClick={onClose}>
          Close
        </Button>
      }
    >
      <RemnantRepairControls
        name={row.name}
        remnantId={remnantId}
        escalated={row.escalationAgeSec !== undefined}
        initialRecovering={initialAction === "recover"}
      />
    </Dialog>
  );
}
