import {
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { AttachmentTile } from "../panes/session/composer/AttachmentTile";
import { askDockStore, useAskDockPending } from "../panes/session/composer/askDock/askDockStore";
import { type TextEditor, useAttachments } from "../panes/session/composer/attachments/useAttachments";
import { consumeComposerFocus, useComposerFocusRequest } from "../panes/session/composer/composerFocus";
import { readDraftRevision } from "../panes/session/composer/draft";
import { QueueStrip } from "../panes/session/composer/queue";
import { SkillEditor, type SkillEditorHandle } from "../panes/session/composer/SkillEditor";
import type { SkillEditorValue } from "../panes/session/composer/skillDocument";
import type { ComposerSourceState } from "../panes/session/composer/sourceState";
import { useComposerActions } from "../panes/session/composer/useComposerActions";
import { useInlineStopReady } from "../panes/session/composer/useInlineStopReady";
import { prefsStore } from "../stores/prefs";
import { Button, PromptCard, SendIcon } from "../widgets";
import styles from "./comic.module.css";

/** A full-page interaction area, never mounted by an inline child reader. */
export function ComicComposer({
  source,
  sessionActions,
  headingStatus,
}: {
  source: ComposerSourceState;
  sessionActions?: ReactNode;
  headingStatus?: ReactNode;
}): React.ReactElement {
  const snapshot = useSyncExternalStore(source.subscribe, source.getSnapshot, source.getSnapshot);
  const { text, skillNames, commandNames, mentions, restoreEpoch, activeRecoveryId } = snapshot;
  const actions = useComposerActions(source);
  const askPending = useAskDockPending(source.ref);
  const focusRequest = useComposerFocusRequest(source.ref);
  const attachments = useAttachments(source.editor, source.attachments);
  const editor = useRef<SkillEditorHandle>(null);
  const mounted = useRef(false);
  const composing = useRef(false);
  const cursor = useRef<number | null>(null);
  const [cursorEpoch, setCursorEpoch] = useState(0);
  const restoreCursor = useCallback((position: number) => {
    cursor.current = position;
    setCursorEpoch((epoch) => epoch + 1);
  }, []);
  const binding = useMemo<TextEditor>(
    () => ({
      read: () => {
        const text = source.getSnapshot().text;
        const position = cursor.current ?? editor.current?.getCursor() ?? text.length;
        const selection =
          cursor.current === null && editor.current
            ? editor.current.getSelection()
            : { start: position, end: position };
        return { text, cursor: position, selection };
      },
      write: (text, position, editSource) => {
        const mayPersist =
          editSource === undefined ||
          editSource === "edit" ||
          source.continuity.ownedDraftRevisionRef.current === readDraftRevision(source.ref);
        source.writeDraft(text, mayPersist, editSource);
        restoreCursor(position);
      },
    }),
    [source, restoreCursor],
  );
  useLayoutEffect(() => {
    mounted.current = true;
    const unbind = source.bindEditor(binding);
    return () => {
      mounted.current = false;
      unbind();
    };
  }, [source, binding]);
  useLayoutEffect(() => {
    if (!focusRequest || askPending || !source.alive || !editor.current) return;
    editor.current.focus();
    consumeComposerFocus(source.ref);
  }, [focusRequest, askPending, source]);
  const previousRecovery = useRef(activeRecoveryId);
  useLayoutEffect(() => {
    const activating = previousRecovery.current === null && activeRecoveryId !== null;
    previousRecovery.current = activeRecoveryId;
    // A non-null owner rollover preserves selection. Explicit Edit restores below.
    if (activating) restoreCursor(source.getSnapshot().text.length);
  }, [activeRecoveryId, source, restoreCursor]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: the epoch commits a scheduled caret even for unchanged text
  useLayoutEffect(() => {
    if (cursor.current === null) return;
    editor.current?.setSelection(cursor.current);
    cursor.current = null;
  }, [cursorEpoch]);
  const hasContent =
    text.trim() !== "" || skillNames.length > 0 || commandNames.length > 0 || attachments.items.length > 0;
  const inlineStop = actions.showStop && !hasContent;
  const stopReady = useInlineStopReady(source.ref, inlineStop ? "stop" : "send");
  function change(value: SkillEditorValue) {
    source.editSkillNames(value.skillNames);
    source.updateSelectionMeta(value);
    source.editText(value.text);
    if (source.continuity.recoveryOwnerLeaseRef.current === null) source.persistDraft(value.text);
  }
  function keyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== "Enter" || event.nativeEvent.isComposing || composing.current) return;
    if (event.metaKey || event.ctrlKey || (!event.shiftKey && !event.altKey && prefsStore.getState().enterToSend)) {
      event.preventDefault();
      submit();
    }
  }
  function submit() {
    if ((askDockStore.getState().byRef.get(source.ref)?.batches.length ?? 0) > 0) return;
    void actions.submitFromLiveState();
  }
  return (
    <section className={styles.interaction} aria-label="Your message" data-comic-composer={source.ref}>
      {sessionActions || headingStatus ? (
        <div className={styles.composerHeading}>
          <div>
            <strong>You</strong>
            {headingStatus}
          </div>
          {sessionActions && <div>{sessionActions}</div>}
        </div>
      ) : (
        <strong>You</strong>
      )}
      <QueueStrip
        {...actions.queueProps}
        onRestoreToComposer={(...args) => {
          actions.queueProps.onRestoreToComposer(...args);
          if (mounted.current) editor.current?.focus();
        }}
        onEditRecovery={async (record) => {
          const epoch = source.getSnapshot().restoreEpoch;
          await actions.queueProps.onEditRecovery?.(record);
          if (mounted.current && source.alive && source.getSnapshot().restoreEpoch !== epoch) {
            restoreCursor(source.getSnapshot().text.length);
            editor.current?.focus();
          }
        }}
      />
      {attachments.items.length > 0 && (
        <div className={styles.recoveredAttachments}>
          {attachments.items.map((item) => (
            <AttachmentTile key={item.marker} item={item} onRemove={() => attachments.removeItem(item.marker)} />
          ))}
        </div>
      )}
      <form
        hidden={askPending}
        inert={askPending}
        onSubmit={(event) => {
          event.preventDefault();
          if (!composing.current) submit();
        }}
        onCompositionStart={() => {
          composing.current = true;
        }}
        onCompositionEnd={() => {
          composing.current = false;
        }}
      >
        <PromptCard
          field={
            <SkillEditor
              ref={editor}
              value={{ text, skillNames, commandNames, mentions }}
              restoreEpoch={restoreEpoch}
              onChange={change}
              onKeyDown={keyDown}
              aria-label="Message"
              placeholder="Message the agent…"
            />
          }
          actions={
            <>
              {actions.showStop && (
                <Button
                  key="stop"
                  type="button"
                  variant="dangerQuiet"
                  data-testid="composer-stop"
                  disabled={actions.busy || (inlineStop && !stopReady)}
                  onClick={() => void actions.stop()}
                >
                  Stop
                </Button>
              )}
              {!inlineStop && (
                <Button
                  key="send"
                  type="submit"
                  data-testid="composer-submit"
                  icon={<SendIcon />}
                  disabled={
                    actions.busy || !hasContent || !(actions.availability.canSend || actions.availability.canQueue)
                  }
                  onClick={(event) => {
                    // Move focus before disabling Send, never after an asynchronous completion.
                    if (event.currentTarget.ownerDocument.activeElement === event.currentTarget)
                      editor.current?.focus();
                  }}
                >
                  {actions.availability.canQueue && !actions.availability.canSend ? "Queue" : "Send"}
                </Button>
              )}
            </>
          }
        />
      </form>
    </section>
  );
}
