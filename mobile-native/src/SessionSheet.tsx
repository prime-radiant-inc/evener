import { useEffect, useState, useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type { SessionControls } from "./sessionControls";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function SessionSheet({
  conversation,
  hubName,
  controls,
  ready,
  close,
}: {
  conversation: MobileConversation;
  hubName: string;
  controls: SessionControls;
  ready: boolean;
  close: () => void;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
  const [name, setName] = useState(conversation.name ?? "");
  const [editingName, setEditingName] = useState(false);
  useEffect(() => {
    if (!editingName) setName(conversation.name ?? "");
    else if (name.trim() === conversation.name) setEditingName(false);
  }, [conversation.name, editingName, name]);
  const disabled = !ready || state.pending !== null;
  const runtimeStopped = conversation.status === "notLoaded";
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
            <Copy>Session</Copy>
            <Copy muted>{hubName}</Copy>
          </View>
          <Action onPress={close}>Done</Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "ios" ? "padding" : "height"}
        >
          <ScrollView
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 20, gap: 24 }}
          >
            <View style={{ gap: 8 }}>
              <Copy muted>Name</Copy>
              <TextInput
                accessibilityLabel="Session name"
                value={name}
                onChangeText={(value) => {
                  setEditingName(true);
                  setName(value);
                }}
                editable={!disabled && conversation.capabilities.rename}
                returnKeyType="done"
                style={[
                  styles.input,
                  {
                    color: colors.text,
                    borderColor: colors.border,
                    backgroundColor: colors.surface,
                  },
                ]}
              />
              <Action
                disabled={
                  disabled ||
                  !conversation.capabilities.rename ||
                  !name.trim() ||
                  name.trim() === conversation.name
                }
                onPress={() => {
                  void controls.rename(name);
                }}
              >
                Save name
              </Action>
            </View>
            <View style={{ gap: 8 }}>
              <Copy muted>{`Model · ${conversation.modelProvider}`}</Copy>
              <Copy muted>
                {runtimeStopped
                  ? "Runtime stopped"
                  : `Status · ${conversation.status}`}
              </Copy>
            </View>
            {conversation.supportsReasoning &&
            conversation.reasoningEffortLevels?.length ? (
              <View style={{ gap: 4 }}>
                <Copy>Reasoning effort</Copy>
                <Copy muted>
                  Choose how much reasoning this session requests from its
                  model.
                </Copy>
                {conversation.reasoningEffortLevels.map((effort) => (
                  <Choice
                    key={effort}
                    label={effort}
                    selected={conversation.reasoningEffort === effort}
                    disabled={disabled}
                    onPress={() => {
                      void controls.setReasoningEffort(effort);
                    }}
                  />
                ))}
              </View>
            ) : null}
            <ErrorMessage message={state.error} />
            {state.notice ? <Copy>{state.notice}</Copy> : null}
            {state.pending ? (
              <ActivityIndicator
                accessibilityLabel="Updating session"
                color={colors.accent}
              />
            ) : null}
            {!ready ? (
              <Copy muted>Reconnect to change this session.</Copy>
            ) : null}
            {conversation.capabilities.compact ? (
              <View
                style={{
                  gap: 8,
                  borderTopWidth: 1,
                  borderColor: colors.border,
                  paddingTop: 16,
                }}
              >
                <Copy>Context</Copy>
                <Copy muted>
                  Summarize earlier context to free space for the model. Saved
                  conversation history remains available.
                </Copy>
                <Action
                  disabled={disabled}
                  onPress={() => {
                    void controls.compact();
                  }}
                >
                  Compact context
                </Action>
              </View>
            ) : null}
            {conversation.capabilities.shutdown && !runtimeStopped ? (
              <View
                style={{
                  gap: 8,
                  borderTopWidth: 1,
                  borderColor: colors.border,
                  paddingTop: 16,
                }}
              >
                <Copy>Runtime</Copy>
                <Copy muted>
                  Stop this session’s process. Its saved conversation can be
                  reopened later.
                </Copy>
                <Action
                  disabled={disabled}
                  onPress={() =>
                    Alert.alert(
                      "Stop this runtime?",
                      "Running work will be interrupted. The saved conversation and your draft are kept. Opening the session later can resume its runtime.",
                      [
                        { text: "Cancel", style: "cancel" },
                        {
                          text: "Stop runtime",
                          style: "destructive",
                          onPress: () => {
                            void controls.shutdown();
                          },
                        },
                      ],
                    )
                  }
                >
                  Stop runtime
                </Action>
              </View>
            ) : null}
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
