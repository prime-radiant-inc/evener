import { createContext, type ReactNode, useContext, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { installNavigationGuard } from "../../shell/routing";
import { ConfirmDialog } from "../../widgets";

type DraftRegistry = { dirty: Set<symbol>; update: (id: symbol, dirty: boolean) => void };
const Drafts = createContext<DraftRegistry | null>(null);

/** Registration follows the editor's lifetime, never a host or persistent cache. */
export function useSettingsDirty(isDirty: boolean): void {
  const drafts = useContext(Drafts);
  const [id] = useState(() => Symbol("settings draft"));
  useLayoutEffect(() => {
    drafts?.update(id, isDirty);
    return () => {
      drafts?.update(id, false);
    };
  }, [drafts, id, isDirty]);
}

export function useSettingsDraftProtection(): boolean {
  return useContext(Drafts) !== null;
}

/** For widget-owned staged values exposed through existing render slots. */
export function SettingsDraft({ dirty, children }: { dirty: boolean; children: ReactNode }) {
  useSettingsDirty(dirty);
  return children;
}

export function SettingsDraftsProvider({ children }: { children: ReactNode }) {
  const [isDirty, setIsDirty] = useState(false);
  const [drafts] = useState<DraftRegistry>(() => {
    const dirty = new Set<symbol>();
    return {
      dirty,
      update: (id, value) => {
        if (value) dirty.add(id);
        else dirty.delete(id);
        setIsDirty(dirty.size > 0);
      },
    };
  });
  const pending = useRef<(() => void) | null>(null);
  const [confirming, setConfirming] = useState(false);
  useLayoutEffect(() => {
    const removeGuard = installNavigationGuard((proceed) => {
      // Never retarget an open confirmation, including when a save completes.
      if (pending.current !== null) return false;
      if (drafts.dirty.size === 0) return true;
      pending.current = proceed;
      setConfirming(true);
      return false;
    });
    return () => {
      removeGuard();
      pending.current = null;
    };
  }, [drafts]);
  useLayoutEffect(() => {
    if (!isDirty) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [isDirty]);

  function stay() {
    pending.current = null;
    setConfirming(false);
  }
  function discard() {
    const proceed = pending.current;
    pending.current = null;
    setConfirming(false);
    proceed?.();
  }
  return (
    <Drafts.Provider value={drafts}>
      {children}
      {/* A navigation request can originate in the mobile rail or an editor
          sheet. Its answer must sit above those sibling overlays, not behind
          their scrims merely because Settings appears earlier in the shell. */}
      {createPortal(
        <ConfirmDialog
          open={confirming}
          title="Discard unsaved changes?"
          confirmLabel="Discard changes"
          cancelLabel="Stay"
          onConfirm={discard}
          onCancel={stay}
        >
          Your changes have not been saved. Stay to finish editing, or discard them and leave this form.
        </ConfirmDialog>,
        document.body,
      )}
    </Drafts.Provider>
  );
}
