// SessionActionsMenu: fork/aside/compact/clear/shutdown/rename, gated by
// ThreadCapabilities. Destructive actions (clear, shutdown) confirm via the
// base Dialog widget composed by hand - a ConfirmDialog widget exists only
// on the parallel settings wave's branch (W7), not this one; a post-merge
// unification is already tracked, per the wave dispatch. Fork/Aside are the
// SAME wire method (thread/fork) with mutually exclusive param sets - see
// stores/threads.ts's own ForkFromTurnOptions doc comment - so both share
// the one openChildPane success path below.
import { type ChangeEvent, useState } from "react";
import type { ThreadModel } from "../../../protocol/model";
import { workspaceStore } from "../../../shell/workspace";
import { threadsStore } from "../../../stores/threads";
import { Button, Dialog, Input, Menu, type MenuItem, Textarea, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { lastUserMessageText } from "./sessionActions";
import styles from "./sessionactionsmenu.module.css";

export interface SessionActionsMenuProps {
  sessionRef: string;
  model: ThreadModel;
}

const CLASS = {
  field: requireClass(styles.field, "sessionactionsmenu.module.css", "field"),
  label: requireClass(styles.label, "sessionactionsmenu.module.css", "label"),
  footer: requireClass(styles.footer, "sessionactionsmenu.module.css", "footer"),
  body: requireClass(styles.body, "sessionactionsmenu.module.css", "body"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// openChildPane is fork/aside's shared success path: the response describes
// a DIFFERENT ref (the new child thread, stores/threads.ts's own
// forkFromTurn doc comment), so the caller here opens it as its own pane
// rather than touching the parent's tracked model.
function openChildPane(childRef: string): void {
  workspaceStore.getState().openPane("session", { ref: childRef });
}

export function SessionActionsMenu({ sessionRef, model }: SessionActionsMenuProps) {
  const toasts = useToasts();
  const [busy, setBusy] = useState(false);

  const [renameOpen, setRenameOpen] = useState(false);
  const [renameValue, setRenameValue] = useState("");

  const [clearOpen, setClearOpen] = useState(false);
  const [shutdownOpen, setShutdownOpen] = useState(false);

  const [forkOpen, setForkOpen] = useState(false);
  const [forkValue, setForkValue] = useState("");

  const lastUserMessage = lastUserMessageText(model);

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

  function handleFork() {
    if (!lastUserMessage) return;
    setForkValue(lastUserMessage.text);
    setForkOpen(true);
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

  function handleForkSubmit() {
    if (!lastUserMessage) return;
    const trimmed = forkValue.trim();
    if (!trimmed) return;
    void runGuarded(
      async () => {
        const resp = await threadsStore.getState().forkFromTurn(sessionRef, {
          sourceTurnId: lastUserMessage.turnId,
          editedInput: trimmed,
        });
        openChildPane(resp.thread.serf.ref);
        setForkOpen(false);
      },
      (msg) => `Couldn't fork session: ${msg}`,
    );
  }

  const items: MenuItem[] = [
    { id: "fork", label: "Fork", disabled: !model.capabilities.forkFromTurn || !lastUserMessage, onSelect: handleFork },
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
      <Menu trigger="Session actions" items={items} />

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

      <Dialog
        open={forkOpen}
        onClose={() => setForkOpen(false)}
        title="Fork from your last message"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setForkOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" onClick={handleForkSubmit} disabled={busy || !forkValue.trim()}>
              Fork
            </Button>
          </div>
        }
      >
        <div className={CLASS.field}>
          <label className={CLASS.label} htmlFor="session-actions-fork-textarea">
            Message to fork from
          </label>
          <Textarea
            id="session-actions-fork-textarea"
            autoGrow
            value={forkValue}
            onChange={(e: ChangeEvent<HTMLTextAreaElement>) => setForkValue(e.target.value)}
          />
        </div>
      </Dialog>
    </>
  );
}
