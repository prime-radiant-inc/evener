// Composer: the session pane's input surface, mounted by Session.tsx below
// the transcript (T1 carves this slot; Session.tsx is FROZEN for the wave
// once T1 lands — every stream below edits only inside this subtree).
//
// T2 (this file): the Textarea, send-vs-steer-vs-queue-vs-drain routing via
// protocol/sendQueueAvailability's deriveSendQueueAvailability +
// submitRouting.ts's own steer/drain fork, Enter-to-send preference,
// per-ref drafts, attachments (paste/drag/picker), interrupt affordance.
// T3/T4 render their own subtrees inside the two marked slots below without
// ever touching the surrounding structure - see each slot's own comment.
import { type FormEvent, type KeyboardEvent as ReactKeyboardEvent, useLayoutEffect, useRef, useState } from "react";
import { WireError } from "../../../protocol/errors";
import { deriveSendQueueAvailability } from "../../../protocol/sendQueueAvailability";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, Dropzone, IconButton, KeyHint, Textarea, useToasts } from "../../../widgets";
import { AskDock, useAskDockPending } from "./askDock";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { type TextEditor, useAttachments } from "./attachments/useAttachments";
import styles from "./composer.module.css";
import { clearDraft, readDraft, writeDraft } from "./draft";
import { readEnterToSendPref } from "./enterToSendPref";
import { QueueStrip, submitWithPendingTracking } from "./queue";
import { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";

export interface ComposerProps {
  ref: string;
}

type BusyAction = "submit" | "steer" | "interrupt" | null;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// isQueuedDrainPartial mirrors appwire.QueuedDrainPartial's own
// serfErrorInfo discriminator (code -32013, SAME code turn-CAS Conflict
// uses - the discriminator is the string, never the code alone; see
// stores/threads.ts's mapConflict for the sibling case). A drain that fails
// with this specific error already queued the text before the drain step
// itself failed, so the composer still clears (parity-m5-composer.md §A)
// while every other drain failure leaves it untouched like any other
// submit failure.
function isQueuedDrainPartial(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "queuedDrainPartial";
}

export function Composer({ ref }: ComposerProps) {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const toasts = useToasts();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  // Set by textEditor.write() below; consumed (and cleared) by the
  // cursor-restore layout effect once `text`'s new value has committed.
  const cursorToRestoreRef = useRef<number | null>(null);

  // Restore-on-mount is unconditional, not leak-guarded: under dockview a
  // session pane's `ref` never changes across a mounted Composer's
  // lifetime (shell/paneRegistry.ts marks "session" non-singleton, so a
  // different ref is always a DIFFERENT pane/mount, never this same
  // instance re-parented - see draft.ts's own header comment for the full
  // trail). A fresh mount's React state starts empty by construction, so
  // there is no "stale text from a different ref" a lazy initializer could
  // ever observe here, unlike the legacy DOM-morph world drafts.ts's own
  // isOtherSessionsDraft guarded against.
  const [text, setText] = useState(() => readDraft(ref));
  const [busyAction, setBusyAction] = useState<BusyAction>(null);

  // textRef mirrors `text`, updated SYNCHRONOUSLY by updateText() below -
  // unlike `text` itself (a plain per-render const) or the textarea DOM
  // node's own `.value` (only updated once React actually commits),
  // textRef.current is correct the INSTANT any text-changing path runs,
  // regardless of which render's closure is asking or whether React has
  // had a chance to re-render yet. Both properties matter: useAttachments'
  // decode-failure callback (useAttachments.ts) can resume long after the
  // render that registered it - a closure over plain `text` would read
  // however stale that render's value was, correctly stripping the marker
  // from it but then overwriting whatever the user has typed SINCE with
  // that same stale text (a real, reproduced bug: paste an image whose
  // decode later fails, type before it settles, watch the typed text get
  // silently reverted - Composer.test.tsx's own regression test). And two
  // attachment gestures fired back-to-back with no intervening render
  // (also tested) would see the SAME staleness from a DOM read, since
  // React hasn't committed the first gesture's `setText` yet by the time
  // the second one asks.
  const textRef = useRef(text);

  function updateText(nextText: string): void {
    textRef.current = nextText;
    setText(nextText);
  }

  // Bridges useAttachments' pure string-splice logic to this component's
  // own controlled `text` state, instead of a direct DOM `.value` mutation
  // - see useAttachments.ts's TextEditor doc comment for the React
  // controlled-input restoration bug that direct mutation ran into. Also
  // keeps the draft in sync with attachment-driven edits (marker insert on
  // ingest, marker strip on remove/decode-failure), not just typing -
  // otherwise a decode failure's stripped marker would leave a stale,
  // now-invalid "[image N]" fragment sitting in the stored draft even
  // though the visible textarea correctly no longer shows it.
  //
  // read()'s cursor prefers cursorToRestoreRef.current (this component's
  // OWN pending, not-yet-committed cursor intent) over the DOM's live
  // selectionStart - reusing that ref rather than adding a parallel one,
  // since it already means exactly "the last write() call's intended
  // cursor, whenever the layout effect hasn't applied it to the DOM yet".
  // Needed for the identical reason textRef is: a second ingestFiles call
  // landing before any render (e.g. two attachment gestures fired back to
  // back - Composer.test.tsx's own regression test) would otherwise read
  // the DOM's selectionStart, which the browser hasn't moved yet because
  // the layout effect that moves it hasn't run - inserting the second
  // marker at the FIRST marker's stale pre-insertion position instead of
  // chaining after it. Once the layout effect actually applies a cursor
  // and clears this ref (back to null), read() correctly falls back to the
  // live DOM value - which is what must be trusted for genuine user-driven
  // cursor movement (clicking, arrow keys) that this component has no
  // other hook into.
  const textEditor: TextEditor = {
    read: () => ({
      text: textRef.current,
      cursor: cursorToRestoreRef.current ?? textareaRef.current?.selectionStart ?? textRef.current.length,
    }),
    write: (nextText, cursor) => {
      updateText(nextText);
      writeDraft(ref, nextText);
      cursorToRestoreRef.current = cursor;
    },
  };
  const attachments = useAttachments(textEditor);

  // askPending gates hiding/inerting the input row below (AskDock's own
  // seam - see AskDock.tsx's header comment: "that is the composer's own
  // surface to show/hide, and T2 owns it") - read unconditionally alongside
  // the rest of this component's hooks, ahead of the `!model` early return
  // below, per the rules of hooks.
  const askPending = useAskDockPending(ref);

  // Runs after `text`'s new value has committed to the DOM (via React's own
  // controlled-value reconciliation) - only then is it safe to move the
  // native cursor without React clobbering it. Keyed on `text` so it fires
  // once per actual text change, not on every unrelated re-render (e.g. a
  // live model update); a no-op whenever textEditor.write() wasn't the
  // cause of this particular change (ordinary typing has no ref to
  // restore).
  // biome-ignore lint/correctness/useExhaustiveDependencies: text is a deliberate trigger-only dep - the effect body only reads cursorToRestoreRef, but must still re-run on every text change to pick up a write() that just landed, same idiom as widgets/textarea's own autoGrow effect
  useLayoutEffect(() => {
    const cursor = cursorToRestoreRef.current;
    if (cursor === null) return;
    cursorToRestoreRef.current = null;
    const el = textareaRef.current;
    if (el) {
      el.selectionStart = cursor;
      el.selectionEnd = cursor;
    }
  }, [text]);

  if (!model) return null; // Session.tsx only mounts this once its own model is hydrated; defensive only.

  // Captured as plain consts (not read as `model.xyz` again below): a
  // closure that references `model` directly cannot inherit the `if
  // (!model) return null` narrowing above through a nested function
  // declaration (a TypeScript limitation, not a real possible-undefined
  // case - Session.tsx never mounts this component before its own model is
  // hydrated), so every handler below reads these already-narrowed values
  // instead of `model.<field>` directly.
  const activeTurnId = model.activeTurnId;
  const availability = deriveSendQueueAvailability({ statusType: model.status.type, capabilities: model.capabilities });
  const busy = isTurnActive(model.status.type, activeTurnId);
  const queueDepth = model.queue?.depth ?? 0;
  const hasText = text.trim() !== "";
  const hasAttachments = attachments.items.length > 0;
  const hasContent = hasText || hasAttachments;
  const enterToSend = readEnterToSendPref();

  const showStop =
    model.status.type !== "ended" &&
    model.status.type !== "closed" &&
    (model.capabilities.interrupt || model.capabilities.steer || model.capabilities.send || model.capabilities.queue);
  const showSteer = model.capabilities.steer || model.capabilities.send || model.capabilities.queue;
  const submitLabel = availability.canQueue ? "Queue" : "Send";

  function handleTextChange(event: { target: { value: string } }): void {
    updateText(event.target.value);
    writeDraft(ref, event.target.value);
  }

  // clearIfUnchanged mirrors clearComposerDraftIfUnchanged (parity-m5-
  // composer.md §A): reads textRef.current, not `submittedText` (a `const`
  // closed over by this async handler at call time, which never changes
  // after that point regardless of later renders) - textRef.current is
  // kept synchronously current by updateText() regardless of which
  // render's closure this particular submitAction call started from.
  function clearIfUnchanged(submittedText: string): void {
    if (textRef.current === submittedText) {
      updateText("");
      clearDraft(ref);
    }
  }

  // handleDrainSuccess is QueueStrip's own onDrainSuccess seam (its "Steer
  // now" button, a SEPARATE trigger from this component's own classic
  // steer/drain path below): mirrors the legacy "the textarea clears" rule
  // on a successful drain. Unlike clearIfUnchanged, the callback carries no
  // snapshot of what was drained (QueueStripProps.onDrainSuccess takes no
  // arguments), so there is no "unchanged since submit" check possible here
  // - this unconditionally clears whatever is CURRENTLY present. A real,
  // narrow asymmetry with this component's own drain path (submitAction
  // below DOES check clearIfUnchanged) - see w5-integration-wiring-report.md.
  function handleDrainSuccess(): void {
    updateText("");
    clearDraft(ref);
    attachments.clearSubmitted(new Set(attachments.items.map((item) => item.marker)));
  }

  // restoreTextToComposer implements the shared "put text back into the
  // composer without clobbering what's already typed there" merge (parity-
  // m5-composer.md line 101, byte-ported from renderer.js:6823-6837's own
  // restoreTextToComposer): existing text is right-trimmed then kept, the
  // incoming text is appended after a blank line, textEditor.write() keeps
  // the draft in sync (mirrors the legacy synthetic `input` event) and
  // parks the cursor at the very end, and the textarea is refocused - same
  // as legacy's own ta.focus() call.
  //
  // Both wave-5 "restore to composer" seams share this exact behavior:
  // QueueStrip's own onRestoreToComposer (a queued-entry edit) AND AskDock's
  // onFallbackToComposer (a turn/start Conflict on the ask-answers path).
  // Legacy's OWN two equivalent functions actually diverge here -
  // dropComposedTextIntoComposer (renderer.js:6238-6245) unconditionally
  // overwrites `ta.value = text`, dropping whatever was already typed -
  // but legacy never had to contend with a hidden composer whose own React
  // state survives underneath (this rewrite's AskDock only hides/inerts the
  // input row via useAskDockPending below; it never clears Composer's own
  // `text` state). Overwriting here would silently discard a draft the user
  // started before an unrelated question ever arrived, so this wave's own
  // integration deliberately reuses the queue-edit merge behavior for both
  // seams rather than porting the overwrite - a considered choice, not a
  // parity citation.
  //
  // Deliberately typed to accept only `restoredText`, not the second
  // `attachments` parameter QueueStripProps.onRestoreToComposer's own
  // signature allows for - a queued entry's edit is a text-only recompose
  // per parity (contracts-composer-queue-pending.md:70, parity-m5-
  // composer.md:102): dropped image attachments surface via QueueStrip's
  // own reportRemovedImages warning toast, never restored here. Any
  // attachments argument a caller passes is simply extra to a JS/TS call
  // and never reaches this function's body.
  function restoreTextToComposer(restoredText: string): void {
    const existing = textRef.current;
    const merged = existing.trim() === "" ? restoredText : `${existing.replace(/\s+$/, "")}\n\n${restoredText}`;
    textEditor.write(merged, merged.length);
    textareaRef.current?.focus();
  }

  // getComposerText is QueueStrip's own seam for reading this composer's
  // CURRENT text/attachments at the moment its drain affordance is used -
  // textRef.current (not the `text` state closure) for the identical
  // liveness reason textEditor.read() above reads it, and
  // toInputAttachments() for the same wire shape submitAction's own payload
  // already uses. Attachments still mid-encode are silently omitted by
  // toInputAttachments() itself (its own doc comment: callers should only
  // invoke it once hasPending is false) - QueueStrip's "Steer now" button
  // has no hasPending guard of its own (unlike this component's classic
  // submit paths), a narrow, pre-existing gap flagged in
  // w5-integration-wiring-report.md rather than fixed here (QueueStrip.tsx
  // is outside this integration's manifest).
  function getComposerText() {
    return { text: textRef.current, attachments: attachments.toInputAttachments() };
  }

  // submitAction wraps every method in submitWithPendingTracking (the wave's
  // own beyond-parity decision - w5-task-3-report.md: "T2's own send/steer
  // submissions should wrap their threadsStore.send/.steer calls in
  // submitWithPendingTracking the same way QueueStrip's own drain handler
  // wraps drainAsSteer... that's what makes optimistic pending genuinely
  // uniform across all four methods"), exactly mirroring QueueStrip.tsx's
  // own handleDrain shape.
  //
  // Toast/bookkeeping split, reconciled against pendingTurnsStore's own
  // documented contract (its submitWithPendingTracking doc comment: onFailure
  // is for toasting, "rethrowing so the caller's OWN catch can still run its
  // own non-toast bookkeeping"): the toast lives ENTIRELY inside onFailure
  // (same as QueueStrip's own handleDrain), and the outer catch below only
  // ever re-runs the isQueuedDrainPartial state-clearing (never a second
  // toast) - so a failure surfaces exactly once regardless of which branch
  // it takes. Before this reconciliation, both sides pushed a toast for the
  // SAME failure; this is the fix, not a pre-existing split.
  async function submitAction(kind: "send" | "queue" | "steer" | "drain"): Promise<void> {
    const submittedText = text;
    const submittedMarkers = new Set(attachments.items.map((item) => item.marker));
    const payload = attachments.toInputAttachments();
    setBusyAction(kind === "send" || kind === "queue" ? "submit" : "steer");
    try {
      await submitWithPendingTracking(
        {
          ref,
          method: kind,
          text: submittedText,
          attachments: payload,
          onFailure: (err) => {
            if (kind === "drain" && isQueuedDrainPartial(err)) {
              toasts.push("error", `Drain failed after queueing: ${errorMessage(err)}`);
            } else {
              const label =
                kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
              toasts.push("error", `${label} failed: ${errorMessage(err)}`);
            }
          },
        },
        () => {
          if (kind === "send") return threadsStore.getState().send(ref, submittedText, payload);
          if (kind === "queue") return threadsStore.getState().queue(ref, submittedText, payload);
          if (kind === "steer") return threadsStore.getState().steer(ref, submittedText, payload);
          return threadsStore.getState().drainAsSteer(ref, submittedText, payload);
        },
      );
      clearIfUnchanged(submittedText);
      attachments.clearSubmitted(submittedMarkers);
    } catch (err) {
      // Already reported via onFailure above. A queuedDrainPartial failure
      // still clears the composer (the text was already queued before the
      // drain step itself failed) - every other failure leaves it untouched,
      // same as before this method was wrapped.
      if (kind === "drain" && isQueuedDrainPartial(err)) {
        clearIfUnchanged(submittedText);
        attachments.clearSubmitted(submittedMarkers);
      }
    } finally {
      setBusyAction(null);
    }
  }

  function handleFormSubmit(event: FormEvent): void {
    event.preventDefault();
    if (busyAction !== null) return;
    if (!hasContent) return; // empty composer: no-op, no request, no message
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const route = decideSubmitRoute({ hasContent, availability });
    if (route === "none") {
      toasts.push("error", "Send is not available for this session");
      return;
    }
    void submitAction(route);
  }

  function handleSteerClick(): void {
    if (busyAction !== null) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const route = decideSteerRoute({ hasText, hasAttachments, queueDepth });
    if (route === "none") {
      textareaRef.current?.focus();
      return;
    }
    if (route === "steer" && !activeTurnId) {
      toasts.push("error", "Steer failed: no active turn");
      return;
    }
    void submitAction(route);
  }

  async function handleInterruptClick(): Promise<void> {
    if (busyAction !== null) return;
    setBusyAction("interrupt");
    try {
      await threadsStore.getState().interrupt(ref);
    } catch (err) {
      toasts.push("error", `Interrupt failed: ${errorMessage(err)}`);
    } finally {
      setBusyAction(null);
    }
  }

  // Legacy suppresses every one of these keybindings entirely inside a
  // framed side-pane iframe (isInPane()) - verified moot, not assumed: this
  // rewrite's multi-pane layout is dockview panels in the SAME document
  // (shell/DockHost.tsx), never cross-document <iframe> panes at all (grep
  // for "iframe" across src turns up nothing) - the whole concept this
  // legacy gate defended against doesn't exist here, so there is no
  // isInPane()-equivalent check to port.
  function handleKeyDown(event: ReactKeyboardEvent<HTMLTextAreaElement>): void {
    if (event.key !== "Enter") return;
    if (event.metaKey || event.ctrlKey) {
      event.preventDefault();
      formRef.current?.requestSubmit();
      return;
    }
    const enterToSendNow = readEnterToSendPref(); // fresh, not the render-time `enterToSend` closure
    if (event.shiftKey) {
      if (enterToSendNow) return; // literal newline - avoids doubling up enterToSend's own Enter-submits meaning
      event.preventDefault();
      handleSteerClick();
      return;
    }
    if (!event.altKey && enterToSendNow) {
      event.preventDefault();
      formRef.current?.requestSubmit();
    }
    // else: literal newline (enterToSend off, or an unhandled modifier combo)
  }

  function handlePaste(event: { clipboardData: DataTransfer | null }): void {
    const files = imageFilesFromClipboard(event.clipboardData);
    if (files.length === 0) return; // text-only paste: never preventDefault, let the browser insert it
    attachments.ingestFiles(files, (message) => toasts.push("error", message));
    // Never preventDefault, even for an image+text paste: the text portion
    // still needs the browser's own default insertion (parity-m5-
    // composer.md §G).
  }

  function handleFilePickerChange(event: { target: HTMLInputElement }): void {
    const files = Array.from(event.target.files ?? []);
    if (files.length > 0) attachments.ingestFiles(files, (message) => toasts.push("error", message));
    event.target.value = ""; // re-picking the identical file must re-fire change
  }

  return (
    <div className={styles.composer}>
      {/* T4: ask dock - renders above the queue strip; onFallbackToComposer
          reuses the same restoreTextToComposer merge as QueueStrip's own
          onRestoreToComposer above (see that function's own doc comment for
          why both seams share it). useAskDockPending(ref) (askPending,
          above) hides/inerts the input row below while a question is
          pending - AskDock owns answering while questions pend. */}
      <AskDock ref={ref} onFallbackToComposer={restoreTextToComposer} />
      {/* T3: queue strip - the queue preview (model.queue) above the input
          row; getComposerText/onRestoreToComposer/onDrainSuccess are this
          integration's own seam implementations, see each one's own doc
          comment above. */}
      <QueueStrip
        ref={ref}
        getComposerText={getComposerText}
        onRestoreToComposer={restoreTextToComposer}
        onDrainSuccess={handleDrainSuccess}
      />
      {hasAttachments && (
        <div className={styles.chips} hidden={askPending} inert={askPending}>
          {attachments.items.map((item) => (
            <Chip key={item.marker} tone="neutral" onRemove={() => attachments.removeItem(item.marker)}>
              {/* A single template-literal string, not several sibling
                  expressions: Chip's own removeLabelFor only folds children
                  into the remove button's accessible name ("Remove <text>")
                  when children is unambiguously a string - multiple child
                  nodes would silently fall back to a bare "Remove". */}
              {`${item.name}${typeof item.width === "number" && typeof item.height === "number" ? ` (${item.width}×${item.height})` : ""}`}
            </Chip>
          ))}
        </div>
      )}
      <form ref={formRef} onSubmit={handleFormSubmit}>
        <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
          <div className={styles.inputCard} hidden={askPending} inert={askPending}>
            <Textarea
              ref={textareaRef}
              value={text}
              onChange={handleTextChange}
              onKeyDown={handleKeyDown}
              onPaste={handlePaste}
              autoGrow
              placeholder="Message the agent…"
              aria-label="Message"
            />
            <div className={styles.controls}>
              <IconButton
                label="Attach image"
                icon="+"
                variant="quiet"
                type="button"
                onClick={() => fileInputRef.current?.click()}
              />
              <div className={styles.controlsRight}>
                {showStop && (
                  <IconButton
                    label="Stop"
                    icon="■"
                    variant="danger"
                    type="button"
                    onClick={() => void handleInterruptClick()}
                    disabled={!busy || !model.capabilities.interrupt || busyAction !== null}
                  />
                )}
                {showSteer && (
                  <Button
                    variant="quiet"
                    type="button"
                    onClick={handleSteerClick}
                    disabled={!busy || !model.capabilities.steer || busyAction !== null}
                  >
                    Steer {!enterToSend && <KeyHint keys={["Shift", "Enter"]} />}
                  </Button>
                )}
                <Button
                  type="submit"
                  variant="primary"
                  disabled={busyAction !== null || !hasContent || (!availability.canSend && !availability.canQueue)}
                >
                  {submitLabel} <KeyHint keys={enterToSend ? ["Enter"] : ["Mod", "Enter"]} />
                </Button>
              </div>
            </div>
          </div>
        </Dropzone>
        <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={handleFilePickerChange} />
      </form>
    </div>
  );
}
