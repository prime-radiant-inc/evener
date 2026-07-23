// SessionActionsMenu: aside/compact/clear/shutdown/rename/set-goal, gated by
// ThreadCapabilities, behind a single quiet "⋯" icon trigger (Menu's own
// popup positioning is zero-layout-shift - see menu.module.css's .popup -
// so opening this never reflows the status row). Destructive actions
// (clear, shutdown) confirm via the base Dialog widget composed by hand - a
// ConfirmDialog widget exists only on the parallel settings wave's branch
// (W7), not this one; a post-merge unification is already tracked, per the
// wave dispatch.
//
// Fork moved out of this menu to a per-user-message affordance (a sibling
// stream's work) - a session-chrome menu item had no specific transcript
// message as its context anyway (sessionActions.ts's own doc comment on
// lastUserMessageText, now unused here), which per-message placement fixes
// properly rather than guessing at "the most recent user message". Aside
// stays: unlike Fork it isn't about any particular message.
import { type ChangeEvent, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { workspaceStore } from "../../../shell/workspace";
import { threadsStore } from "../../../stores/threads";
import { Button, Dialog, Input, Menu, type MenuItem, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./sessionactionsmenu.module.css";

export interface SessionActionsMenuProps {
  sessionRef: string;
  model: ThreadModel;
  // Optional so a caller with no goal-dialog seam to wire up can still
  // render this menu; SessionChrome, the real caller, always passes it (see
  // GoalControlProps.dialogOpen's own doc comment - same reasoning).
  onSetGoal?: () => void;
}

const CLASS = {
  srOnly: requireClass(styles.srOnly, "sessionactionsmenu.module.css", "srOnly"),
  field: requireClass(styles.field, "sessionactionsmenu.module.css", "field"),
  label: requireClass(styles.label, "sessionactionsmenu.module.css", "label"),
  footer: requireClass(styles.footer, "sessionactionsmenu.module.css", "footer"),
  body: requireClass(styles.body, "sessionactionsmenu.module.css", "body"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// openChildPane is Aside's success path: the response describes a DIFFERENT
// ref (the new child thread, stores/threads.ts's own forkFromTurn doc
// comment), so the caller here opens it as its own pane rather than
// touching the parent's tracked model.
function openChildPane(childRef: string): void {
  workspaceStore.getState().openPane("session", { ref: childRef });
}

export function SessionActionsMenu({ sessionRef, model, onSetGoal = () => undefined }: SessionActionsMenuProps) {
  const toasts = useToasts();
  const [busy, setBusy] = useState(false);

  const [renameOpen, setRenameOpen] = useState(false);
  const [renameValue, setRenameValue] = useState("");

  const [clearOpen, setClearOpen] = useState(false);
  const [shutdownOpen, setShutdownOpen] = useState(false);

  // runGuarded is the shared shape for every dialog-confirmed action below:
  // disable the confirm control for the duration of the request (guards a
  // double-submit the same way queue promote/edit/cancel's own
  // setQueuedRowActionsDisabled does, per-row there / whole-menu here since
  // only one of these dialogs can ever be open at once), toast on failure
  // per the wave's failure-feedback convention, and leave dialog-open state
  // entirely to the caller - a caller that wants to close on success does so
  // itself, inside `action`, so a failure naturally leaves the dialog open
  // with whatever the user typed still intact.
  async function runGuarded(action: () => Promise<void>, failureMessage: (message: string) => string) {
    setBusy(true);
    try {
      await action();
    } catch (err) {
      toasts.push("error", failureMessage(errorMessage(err)));
    } finally {
      setBusy(false);
    }
  }

  function handleAside() {
    void runGuarded(
      async () => {
        const resp = await threadsStore.getState().forkFromTurn(sessionRef, { aside: true });
        openChildPane(resp.thread.serf.ref);
      },
      (msg) => `Couldn't create aside: ${msg}`,
    );
  }

  function handleCompact() {
    void runGuarded(
      async () => {
        await threadsStore.getState().compact(sessionRef);
      },
      (msg) => `Couldn't compact: ${msg}`,
    );
  }

  function handleClearConfirm() {
    void runGuarded(
      async () => {
        await threadsStore.getState().clearThread(sessionRef);
        setClearOpen(false);
      },
      (msg) => `Couldn't clear conversation: ${msg}`,
    );
  }

  function handleShutdownConfirm() {
    void runGuarded(
      async () => {
        await threadsStore.getState().shutdown(sessionRef);
        setShutdownOpen(false);
      },
      (msg) => `Couldn't shut down session: ${msg}`,
    );
  }

  function handleRenameSave() {
    const trimmed = renameValue.trim();
    if (!trimmed) return;
    void runGuarded(
      async () => {
        await threadsStore.getState().rename(sessionRef, trimmed);
        setRenameOpen(false);
      },
      (msg) => `Couldn't rename session: ${msg}`,
    );
  }

  const items: MenuItem[] = [
    { id: "set-goal", label: "Set goal…", disabled: !model.capabilities.goal, onSelect: onSetGoal },
    { id: "aside", label: "Aside", disabled: !model.capabilities.forkFromTurn, onSelect: handleAside },
    { id: "compact", label: "Compact", disabled: !model.capabilities.compact, onSelect: handleCompact },
    { id: "clear", label: "Clear", disabled: !model.capabilities.clear, onSelect: () => setClearOpen(true) },
    {
      id: "shutdown",
      label: "Shut down",
      disabled: !model.capabilities.shutdown,
      onSelect: () => setShutdownOpen(true),
    },
    {
      id: "rename",
      label: "Rename",
      disabled: !model.capabilities.rename,
      onSelect: () => {
        setRenameValue(model.name);
        setRenameOpen(true);
      },
    },
  ];

  return (
    <>
      <Menu
        variant="quiet"
        trigger={
          <>
            <span aria-hidden="true">⋯</span>
            <span className={CLASS.srOnly}>Session actions</span>
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
            <Button variant="primary" onClick={handleRenameSave} disabled={busy || !renameValue.trim()}>
              Save
            </Button>
          </div>
        }
      >
        <div className={CLASS.field}>
          <label className={CLASS.label} htmlFor="session-actions-rename-input">
            Name
          </label>
          <Input
            id="session-actions-rename-input"
            value={renameValue}
            onChange={(e: ChangeEvent<HTMLInputElement>) => setRenameValue(e.target.value)}
          />
        </div>
      </Dialog>

      <Dialog
        open={clearOpen}
        onClose={() => setClearOpen(false)}
        title="Clear conversation?"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setClearOpen(false)}>
              Cancel
            </Button>
            <Button variant="danger" onClick={handleClearConfirm} disabled={busy}>
              Clear
            </Button>
          </div>
        }
      >
        <p className={CLASS.body}>This removes every message in this session's transcript. This cannot be undone.</p>
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
            <Button variant="danger" onClick={handleShutdownConfirm} disabled={busy}>
              Shut down
            </Button>
          </div>
        }
      >
        <p className={CLASS.body}>
          The agent process for this session will stop. You can still read the transcript afterward.
        </p>
      </Dialog>
    </>
  );
}
