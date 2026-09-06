import { useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  Modal,
  Platform,
  ScrollView,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type { QueueConversationService } from "../../mobile/src/services/conversation";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function QueueSheet({
  conversation,
  service,
  ready,
  refresh,
  close,
}: {
  conversation: MobileConversation;
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
  const instanceId = conversation.instanceId;
  const disabled = !ready || pending || !!error || !instanceId;
  async function act(operation: () => Promise<unknown>) {
    if (disabled || busy.current) return;
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
            <Copy muted>{queue.depth} waiting</Copy>
          </View>
          <Action onPress={close}>Done</Action>
        </View>
        <ScrollView contentContainerStyle={{ padding: 20, gap: 16 }}>
          <Copy muted>
            Messages run in order. Use steering to bring one into the current
            turn.
          </Copy>
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
          {queue.depth === 0 ? <Copy muted>No queued messages.</Copy> : null}
          {Array.from({ length: queue.depth }, (_, index) => {
            const id = queue.ids?.[index];
            const fullText = queue.texts?.[index];
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
                    queue.preview[index] ??
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
                        void act(() =>
                          service.cancelQueued(index, id, instanceId),
                        );
                    }}
                  >
                    Cancel
                  </Action>
                  {conversation.capabilities.steer ? (
                    <Action
                      label={`Use queued message ${index + 1} as steering`}
                      disabled={disabled || !id}
                      onPress={() => {
                        if (id && instanceId)
                          void act(() =>
                            service.promoteQueuedAsSteer(index, id, instanceId),
                          );
                      }}
                    >
                      Use as steering
                    </Action>
                  ) : null}
                </View>
              </View>
            );
          })}
          {queue.depth > 1 && conversation.capabilities.steer ? (
            <Action
              disabled={disabled}
              onPress={() => {
                if (instanceId)
                  void act(() =>
                    service.drainAsSteer(queue.revision, instanceId),
                  );
              }}
            >
              Use all as steering
            </Action>
          ) : null}
        </ScrollView>
      </SafeAreaView>
    </Modal>
  );
}
