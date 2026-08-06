// SessionMenu: THE session "⋯" menu, shared by the session pane's chrome
// and the sidebar rail row (2026-08-05-unified-session-context-menu-design).
// One component owns the item list, the grouping separators, and every
// dialog (Rename / Shut down / Delete / PinSectionPicker - Task 4 adds the
// last two); each render site maps its own data source into the normalized
// props and owns its mutation side effects behind SessionMenuActions.
//
// Failure convention: the ADAPTER toasts (Rail's runAction, the chrome's
// own try/catch) and the rejected promise propagates back here, so a failed
// confirm leaves the dialog open with the confirm button re-enabled; only
// success closes it. Slash-command actions (goal/aside/compact/clear) are
// deliberately NOT here - the command palette owns those.
import { type ChangeEvent, useState } from "react";
import type { SessionPanelKind } from "../../panes/sessionPanels";
import type { TreeNode as ApiTreeNode, PinSectionSummary } from "../../stores/tree";
import { Button, Dialog, Input, Menu, type MenuEntry } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { PinSectionPicker } from "../rail/PinSectionPicker";
import { isTopLevelSession } from "../rail/sessionKind";
import styles from "./sessionmenu.module.css";

export type PinTarget = { section_id: string } | { section_name: string };

export interface SessionMenuActions {
  onOpenPane(pane: SessionPanelKind): void;
  onRename(name: string): Promise<void>;
  onShutdown(): Promise<void>;
  onPin(target: PinTarget, section?: PinSectionSummary): Promise<void>;
  onUnpin(): Promise<void>;
  onToggleArchive(): Promise<void>;
  onDelete(): Promise<void>;
}

export interface SessionMenuProps {
  sessionRef: string;
  title: string;
  triggerLabel: string; // sr-only trigger name: "Session actions" / `Actions for ${title}`
  canRename: boolean;
  canShutdown: boolean;
  treeNode?: ApiTreeNode; // Task 4: presence enables Pin/Archive/Delete per rules
  panesOpen: { details: boolean; tasks: boolean; activity: boolean };
  taskLabel?: string; // e.g. "Tasks 3/7"; defaults to "Tasks"
  activityLabel?: string; // e.g. "Activity · 2"; defaults to "Activity"
  actions: SessionMenuActions;
  triggerTabIndex?: number; // -1 inside rail rows (roving tabindex contract)
}

const CLASS = {
  srOnly: requireClass(styles.srOnly, "sessionmenu.module.css", "srOnly"),
  field: requireClass(styles.field, "sessionmenu.module.css", "field"),
  label: requireClass(styles.label, "sessionmenu.module.css", "label"),
  footer: requireClass(styles.footer, "sessionmenu.module.css", "footer"),
  body: requireClass(styles.body, "sessionmenu.module.css", "body"),
};

const checked = (label: string, open: boolean) => (open ? `${label} ✓` : label);

export function SessionMenu({
  sessionRef,
  title,
  triggerLabel,
  canRename,
  canShutdown,
  treeNode,
  panesOpen,
  taskLabel,
  activityLabel,
  actions,
  triggerTabIndex,
}: SessionMenuProps) {
  const [busy, setBusy] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [renameValue, setRenameValue] = useState("");
  const [shutdownOpen, setShutdownOpen] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  // confirm runs a dialog-confirmed action: busy-lock the confirm button
  // against double-submit, close ONLY on success (a rejection was already
  // toasted by the adapter - see the header comment).
  async function confirm(action: () => Promise<void>, close: () => void) {
    setBusy(true);
    try {
      await action();
      close();
    } catch {
      // adapter toasted; leave the dialog open
    } finally {
      setBusy(false);
    }
  }

  // Pin/Archive/Delete are decisions about a TOP-LEVEL rail row, so they are
  // gated on a treeNode that isTopLevelSession accepts; delete additionally
  // requires a local host because hubcore only deletes local transcripts.
  const organizationEligible = treeNode !== undefined && isTopLevelSession(treeNode);
  const deleteEligible = organizationEligible && treeNode.host_id === "local";

  // Groups joined by separators: panes / organize / destructive. Both
  // separators always render because Rename and Shut down are always present;
  // the eligible-only items slot into their groups without orphaning a rule.
  const paneItems: MenuEntry[] = [
    { id: "details", label: checked("Details", panesOpen.details), onSelect: () => actions.onOpenPane("details") },
    { id: "tasks", label: checked(taskLabel ?? "Tasks", panesOpen.tasks), onSelect: () => actions.onOpenPane("tasks") },
    {
      id: "activity",
      label: checked(activityLabel ?? "Activity", panesOpen.activity),
      onSelect: () => actions.onOpenPane("activity"),
    },
  ];
  const organizeItems: MenuEntry[] = [
    {
      id: "rename",
      label: "Rename",
      disabled: !canRename,
      onSelect: () => {
        setRenameValue(title);
        setRenameOpen(true);
      },
    },
  ];
  if (organizationEligible) {
    organizeItems.push(
      treeNode.pin_section_id
        ? { id: "unpin", label: "Unpin", onSelect: () => void confirm(actions.onUnpin, () => undefined) }
        : { id: "pin", label: "Pin this session…", onSelect: () => setPickerOpen(true) },
      {
        id: "archive",
        label: treeNode.tier === "archived" ? "Unarchive" : "Archive",
        onSelect: () => void confirm(actions.onToggleArchive, () => undefined),
      },
    );
  }
  const destructiveItems: MenuEntry[] = [
    {
      id: "shutdown",
      label: "Shut down",
      disabled: !canShutdown,
      onSelect: () => setShutdownOpen(true),
    },
  ];
  if (deleteEligible) {
    destructiveItems.push({ id: "delete", label: "Delete…", onSelect: () => setDeleteOpen(true) });
  }
  const items: MenuEntry[] = [
    ...paneItems,
    { kind: "separator", id: "sep-organize" },
    ...organizeItems,
    { kind: "separator", id: "sep-destructive" },
    ...destructiveItems,
  ];

  return (
    <>
      <Menu
        variant="quiet"
        triggerTabIndex={triggerTabIndex}
        trigger={
          <>
            <span aria-hidden="true">⋯</span>
            <span className={CLASS.srOnly}>{triggerLabel}</span>
          </>
        }
        items={items}
      />

      <Dialog
        open={renameOpen}
        onClose={() => setRenameOpen(false)}
        title="Rename session"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setRenameOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={() =>
                void confirm(
                  () => actions.onRename(renameValue.trim()),
                  () => setRenameOpen(false),
                )
              }
              disabled={busy || !renameValue.trim()}
            >
              Rename
            </Button>
          </div>
        }
      >
        <div className={CLASS.field}>
          <label className={CLASS.label} htmlFor={`session-menu-rename-${sessionRef}`}>
            Name
          </label>
          <Input
            id={`session-menu-rename-${sessionRef}`}
            value={renameValue}
            onChange={(e: ChangeEvent<HTMLInputElement>) => setRenameValue(e.target.value)}
          />
        </div>
      </Dialog>

      <Dialog
        open={shutdownOpen}
        onClose={() => setShutdownOpen(false)}
        title="Shut down this session?"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setShutdownOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => void confirm(actions.onShutdown, () => setShutdownOpen(false))}
              disabled={busy}
            >
              Shut down
            </Button>
          </div>
        }
      >
        <p className={CLASS.body}>
          The agent process for this session will stop. You can still read the transcript afterward.
        </p>
      </Dialog>

      <Dialog
        open={deleteOpen}
        onClose={() => setDeleteOpen(false)}
        title="Delete session?"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => void confirm(actions.onDelete, () => setDeleteOpen(false))}
              disabled={busy}
            >
              Delete
            </Button>
          </div>
        }
      >
        <p className={CLASS.body}>Permanently delete "{title}"? This removes its transcript and cannot be undone.</p>
      </Dialog>

      {/* The picker reports its own assign errors inline; SessionMenu closes
          it only once onPin resolves - the same close-on-success contract as
          the `confirm` helper above. */}
      {pickerOpen && treeNode && (
        <PinSectionPicker
          session={treeNode}
          onAssign={async (target, section) => {
            await actions.onPin(target, section);
            setPickerOpen(false);
          }}
          onClose={() => setPickerOpen(false)}
        />
      )}
    </>
  );
}
