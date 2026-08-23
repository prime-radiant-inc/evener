// Composer — the primary input dock at the bottom of ConversationScreen.
// Shows a text field bound to the conversation draft, attachment and voice
// (placeholder) actions, a model/effort summary label, and the primary
// action button that follows session state:
//   - Idle (ready): Send
//   - Running: Steer (or Queue when there is an explicit queue depth)
//   - Stop is visible alongside during generating when interrupt is available.
//
// Capability removal: if the conversation's capability for the current action
// is false, the action is disabled and "Unavailable for this source" is shown.
// Mutations are disabled entirely while the connection is not open
// (reconnecting/closed), so hidden controls cannot focus or submit.
//
// The dock uses visual-viewport/keyboard information and safe-area padding via
// the global CSS tokens (--viewport-height, --keyboard-inset, --safe-area-*).

import { type JSX, useCallback } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
import type { InputItem } from "../../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationService } from "../../services/conversation";
import type { AttachmentState } from "../../state/attachments";
import type { ConversationState } from "../../state/conversation";
import "./Composer.css";

export interface ComposerProps {
  readonly conversationStore: UseBoundStore<StoreApi<ConversationState>>;
  readonly conversationService: ConversationService;
  readonly attachmentStore: UseBoundStore<StoreApi<AttachmentState>>;
}

// The primary action kind, derived from conversation status and queue state.
type PrimaryAction = "send" | "steer" | "queue";

function primaryAction(status: string, queueDepth: number): PrimaryAction {
  if (status === "running") {
    return queueDepth > 0 ? "queue" : "steer";
  }
  return "send";
}

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

export function Composer({
  conversationStore,
  conversationService,
  attachmentStore,
}: ComposerProps): JSX.Element {
  const conversation = conversationStore((s) => s.conversation);
  const status = conversationStore((s) => s.status);
  const draft = conversationStore((s) => s.draft);
  const attachments = attachmentStore((s) => s.attachments);

  const handleSubmit = useCallback(() => {
    if (conversation === null) return;
    const caps = conversation.capabilities;
    const queueDepth = conversation.queue.depth;
    const action = primaryAction(conversation.status, queueDepth);
    const actionCap =
      action === "send"
        ? caps.send
        : action === "steer"
          ? caps.steer
          : caps.queue;
    const hasContent = draft.trim().length > 0;
    if (!actionCap || !hasContent) return;
    const input = textInput(draft);
    const store = conversationStore.getState();
    if (action === "send") {
      void store.send(conversationService, input);
    } else if (action === "steer") {
      void store.steer(conversationService, input);
    } else {
      void store.queue(conversationService, input);
    }
  }, [conversation, draft, conversationStore, conversationService]);

  const handleStop = useCallback(() => {
    void conversationStore.getState().interrupt(conversationService);
  }, [conversationStore, conversationService]);

  const handleDraftChange = useCallback(
    (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      conversationStore.getState().setDraft(e.target.value);
    },
    [conversationStore],
  );

  const handleRemoveAttachment = useCallback(
    (id: string) => {
      attachmentStore.getState().remove(id);
    },
    [attachmentStore],
  );

  // No conversation to compose into (should not render, but guard anyway).
  if (conversation === null) {
    return <div className="evener-composer" data-testid="composer" />;
  }

  const caps = conversation.capabilities;
  const queueDepth = conversation.queue.depth;
  const action = primaryAction(conversation.status, queueDepth);
  const isRunning = conversation.status === "running";
  const canInterrupt = isRunning && caps.interrupt;
  const connected = status === "open";

  const actionCap =
    action === "send"
      ? caps.send
      : action === "steer"
        ? caps.steer
        : caps.queue;

  const hasContent = draft.trim().length > 0;
  const actionDisabled = !actionCap || !connected || !hasContent;
  const fieldDisabled = !connected;

  const summary = [conversation.modelProvider];
  if (conversation.reasoningEffort !== undefined) {
    summary.push(conversation.reasoningEffort);
  }

  const actionLabel =
    action === "send" ? "Send" : action === "steer" ? "Steer" : "Queue";
  const actionTestId = `composer-${action}`;

  return (
    <div className="evener-composer" data-testid="composer">
      <div
        className="evener-composer__dock"
        data-viewport-height="var(--viewport-height)"
        data-keyboard-inset="var(--keyboard-inset)"
      >
        <div
          className="evener-composer__summary"
          data-testid="composer-summary"
        >
          {summary.join(" · ")}
        </div>

        <div className="evener-composer__row">
          <button
            type="button"
            className="evener-composer__action evener-composer__action--attach"
            data-testid="composer-attach"
            aria-label="Attach image"
            disabled={!connected}
          >
            +
          </button>

          <textarea
            className="evener-composer__field"
            data-testid="composer-draft"
            placeholder="Message…"
            value={draft}
            onChange={handleDraftChange}
            disabled={fieldDisabled}
            rows={1}
          />

          <button
            type="button"
            className="evener-composer__action evener-composer__action--voice"
            data-testid="composer-voice"
            aria-label="Voice input"
            disabled={!connected}
          >
            🎤
          </button>

          <button
            type="button"
            className="evener-composer__action evener-composer__action--primary"
            data-testid={actionTestId}
            disabled={actionDisabled}
            onClick={handleSubmit}
          >
            {actionLabel}
          </button>
        </div>

        {canInterrupt && (
          <button
            type="button"
            className="evener-composer__stop"
            data-testid="composer-stop"
            onClick={handleStop}
          >
            Stop
          </button>
        )}

        {!actionCap && (
          <div className="evener-composer__unavailable">
            Unavailable for this source
          </div>
        )}

        {attachments.length > 0 && (
          <div className="evener-composer__attachments">
            {attachments.map((a) => (
              <div
                key={a.id}
                className="evener-composer__attachment-chip"
                data-testid={`composer-attachment-${a.id}`}
              >
                <span className="evener-composer__attachment-name">
                  {a.name ?? a.mediaType}
                </span>
                <button
                  type="button"
                  className="evener-composer__attachment-remove"
                  data-testid={`composer-remove-attachment-${a.id}`}
                  aria-label={`Remove ${a.name ?? "attachment"}`}
                  onClick={() => handleRemoveAttachment(a.id)}
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
