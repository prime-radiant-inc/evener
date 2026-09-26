import { useEffect, useRef, useState } from "react";
import {
  type QueueAction,
  queueActionRefusal,
  queueSheetPresentation,
} from "./conversationControls";
import {
  ActivityIndicator,
  Modal,
  Platform,
  ScrollView,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { WireError } from "@evener/appwire-client";
import type { MobileConversation } from "./projectedRows";
import type { QueueConversationService } from "../../mobile/src/services/conversation";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function QueueSheet({
  conversation,
  latest,
  service,
  ready,
  refresh,
  close,
}: {
  conversation: MobileConversation;
  /** The live conversation at press time; the render's `conversation` may be
   * a status behind it. */
  latest: () => MobileConversation | null;
  service: QueueConversationService;
  ready: boolean;
  refresh: () => Promise<void>;
  close: () => void;
}) {
  const colors = useColors();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const busy = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const queue = conversation.queue;
  const depth = queue?.depth ?? 0;
  const instanceId = conversation.instanceId;
  // Promote and drain follow the conversation's controls
  // (conversationControls.ts): the harness steers, and a turn is running or the
  // queue is one a Stop parked.
  const { canRun, runLabel, runAllLabel, explanation } =
    queueSheetPresentation(conversation);
  const disabled = !ready || pending || !!error || !instanceId;
  async function act(action: QueueAction, operation: () => Promise<unknown>) {
    if (disabled || busy.current) return;
    // Re-check the action's control against the live conversation: the
    // status may have flipped since the render that offered it.
    const refusal = queueActionRefusal(latest() ?? conversation, action);
    if (refusal !== null) {
      setError(refusal);
      return;
    }
    busy.current = true;
    setPending(true);
    let failure: string | null = null;
    try {
      await operation();
    } catch (cause) {
      failure =
        cause instanceof WireError
          ? `Could not confirm this action: ${cause.message}. Check the queue and conversation before trying again; it may have been applied.`
          : "Delivery unconfirmed. Check the queue and conversation before trying again. This action may have reached the hub.";
    }
    if (!mounted.current) return;
    try {
      await refresh();
    } catch {
      failure ??=
        "The action was acknowledged, but the queue could not be refreshed. Reconnect and check the conversation.";
    }
    if (!mounted.current) return;
    setError(failure);
    setPending(false);
    busy.current = false;
  }
  return (
    <Modal
      animationType="slide"
      presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
      onRequestClose={close}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View
          style={[
            styles.row,
            {
              paddingHorizontal: 20,
              paddingVertical: 8,
              borderBottomWidth: 1,
              borderColor: colors.border,
            },
          ]}
        >
          <View style={styles.fill}>
            <Copy>Queued messages</Copy>
            <Copy muted>{depth} waiting</Copy>
          </View>
          <Action onPress={close}>Done</Action>
        </View>
        <ScrollView contentContainerStyle={{ padding: 20, gap: 16 }}>
          <Copy muted>{explanation}</Copy>
          <ErrorMessage message={error} />
          {error ? (
            <Action
              disabled={!ready || pending}
              onPress={() => {
                void refresh()
                  .then(() => setError(null))
                  .catch(() =>
                    setError("Could not refresh. Reconnect and try again."),
                  );
              }}
            >
              Refresh queue
            </Action>
          ) : null}
          {pending ? (
            <ActivityIndicator
              accessibilityLabel="Updating queue"
              color={colors.accent}
            />
          ) : null}
          {!ready ? <Copy muted>Reconnect to change this queue.</Copy> : null}
          {depth === 0 ? <Copy muted>No queued messages.</Copy> : null}
          {Array.from({ length: depth }, (_, index) => {
            const id = queue?.ids?.[index];
            const fullText = queue?.texts?.[index];
            return (
              <View
                key={id ?? `preview-${index}`}
                style={{
                  paddingBottom: 16,
                  gap: 8,
                  borderBottomWidth: 1,
                  borderColor: colors.border,
                }}
              >
                <Copy muted>
                  {index + 1}
                  {fullText === undefined ? " · Preview" : ""}
                </Copy>
                <Copy>
                  {fullText ??
                    queue?.preview?.[index] ??
                    "Message content is unavailable."}
                </Copy>
                <View
                  style={[
                    styles.row,
                    { flexWrap: "wrap", justifyContent: "flex-end" },
                  ]}
                >
                  <Action
                    label={`Cancel queued message ${index + 1}`}
                    tone="quiet"
                    disabled={disabled || !id}
                    onPress={() => {
                      if (id && instanceId)
                        void act("cancel", () =>
                          service.cancelQueued(index, id, instanceId),
                        );
                    }}
                  >
                    Cancel
                  </Action>
                  {canRun ? (
                    <Action
                      label={`${runLabel}: queued message ${index + 1}`}
                      disabled={disabled || !id}
                      onPress={() => {
                        if (id && instanceId)
                          void act("promote", () =>
                            service.promoteQueuedAsSteer(index, id, instanceId),
                          );
                      }}
                    >
                      {runLabel}
                    </Action>
                  ) : null}
                </View>
              </View>
            );
          })}
          {queue && depth > 1 && canRun ? (
            <Action
              disabled={disabled}
              onPress={() => {
                if (instanceId)
                  void act("drainAll", () =>
                    service.drainAsSteer(queue.revision, instanceId),
                  );
              }}
            >
              {runAllLabel}
            </Action>
          ) : null}
        </ScrollView>
      </SafeAreaView>
    </Modal>
  );
}
