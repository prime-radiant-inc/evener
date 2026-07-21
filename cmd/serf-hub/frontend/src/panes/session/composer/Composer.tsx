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
import { type FormEvent, type KeyboardEvent as ReactKeyboardEvent, useRef, useState } from "react";
import { WireError } from "../../../protocol/errors";
import { deriveSendQueueAvailability } from "../../../protocol/sendQueueAvailability";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Button, Chip, IconButton, KeyHint, Textarea, useToasts } from "../../../widgets";
import { Dropzone } from "../../../widgets/dropzone";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { useAttachments } from "./attachments/useAttachments";
import styles from "./composer.module.css";
import { clearDraft, readDraft, writeDraft } from "./draft";
import { readEnterToSendPref } from "./enterToSendPref";
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
  const attachments = useAttachments(textareaRef);

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
    setText(event.target.value);
    writeDraft(ref, event.target.value);
  }

  // clearIfUnchanged mirrors clearComposerDraftIfUnchanged (parity-m5-
  // composer.md §A): reads the LIVE textarea DOM value, not `submittedText`
  // (a `const` closed over by this async handler at call time, which never
  // changes after that point regardless of later renders) nor the current
  // `text` state variable (same reason) - only the real DOM node reflects
  // whatever the user has actually typed since submit.
  function clearIfUnchanged(submittedText: string): void {
    if (textareaRef.current?.value === submittedText) {
      setText("");
      clearDraft(ref);
    }
  }

  async function submitAction(kind: "send" | "queue" | "steer" | "drain"): Promise<void> {
    const submittedText = text;
    const submittedMarkers = new Set(attachments.items.map((item) => item.marker));
    const payload = attachments.toInputAttachments();
    setBusyAction(kind === "send" || kind === "queue" ? "submit" : "steer");
    try {
      if (kind === "send") await threadsStore.getState().send(ref, submittedText, payload);
      else if (kind === "queue") await threadsStore.getState().queue(ref, submittedText, payload);
      else if (kind === "steer") await threadsStore.getState().steer(ref, submittedText, payload);
      else await threadsStore.getState().drainAsSteer(ref, submittedText, payload);
      clearIfUnchanged(submittedText);
      attachments.clearSubmitted(submittedMarkers);
    } catch (err) {
      if (kind === "drain" && isQueuedDrainPartial(err)) {
        // Already queued before the drain step failed - clears anyway.
        clearIfUnchanged(submittedText);
        attachments.clearSubmitted(submittedMarkers);
        toasts.push("error", `Drain failed after queueing: ${errorMessage(err)}`);
      } else {
        const label = kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
        toasts.push("error", `${label} failed: ${errorMessage(err)}`);
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
      {/* T4: ask dock - renders above the queue strip; T4 also owns hiding/
          inerting the input row below while a question is pending (see
          askDock's own future contract - this file has no ask-pending
          state to gate on). */}
      {/* T3: queue strip - renders the queue preview (model.queue) above
          the input row. */}
      {hasAttachments && (
        <div className={styles.chips}>
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
          <div className={styles.inputCard}>
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
