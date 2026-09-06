import { useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  View,
} from "react-native";
import {
  SafeAreaView,
  useSafeAreaInsets,
} from "react-native-safe-area-context";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type { ComposerSetting } from "./ComposerSettings";
import { ModelPicker } from "./ModelPicker";
import type { SessionControls } from "./sessionControls";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ComposerSettingsSheet({
  setting,
  conversation,
  controls,
  hubName,
  ready,
  close,
}: {
  setting: ComposerSetting;
  conversation: MobileConversation;
  controls: SessionControls;
  hubName: string;
  ready: boolean;
  close: () => void;
}) {
  const colors = useColors();
  const insets = useSafeAreaInsets();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
  const model = setting === "model";
  return (
    <Modal
      animationType="slide"
      transparent={!model}
      presentationStyle={
        !model
          ? "overFullScreen"
          : Platform.OS === "ios"
            ? "pageSheet"
            : "fullScreen"
      }
      onRequestClose={close}
    >
      <KeyboardAvoidingView
        style={styles.fill}
        enabled={model}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
      >
        <SafeAreaView
          edges={model ? ["top", "bottom", "left", "right"] : ["left", "right"]}
          style={[
            styles.fill,
            {
              justifyContent: "flex-end",
              backgroundColor: model ? colors.background : "#00000066",
            },
          ]}
        >
          {!model ? (
            <Pressable
              accessibilityLabel="Dismiss reasoning choices"
              accessibilityRole="button"
              onPress={close}
              style={{
                position: "absolute",
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
              }}
            />
          ) : null}
          <View
            style={[
              model
                ? styles.fill
                : {
                    maxHeight: "80%",
                    borderTopLeftRadius: 24,
                    borderTopRightRadius: 24,
                    overflow: "hidden",
                  },
              { backgroundColor: colors.background },
            ]}
          >
            <View
              style={[
                styles.row,
                {
                  paddingHorizontal: 20,
                  paddingVertical: 12,
                  borderBottomWidth: 1,
                  borderColor: colors.border,
                },
              ]}
            >
              <View style={styles.fill}>
                <Copy>{model ? "Model" : "Reasoning effort"}</Copy>
                <Copy muted>{hubName}</Copy>
              </View>
              <Action onPress={close}>Done</Action>
            </View>
            {model ? (
              <ModelPicker
                controls={controls}
                currentModel={conversation.modelProvider}
                ready={ready && conversation.capabilities.changeModel}
                done={close}
              />
            ) : (
              <ScrollView
                contentContainerStyle={{
                  padding: 20,
                  paddingBottom: 20 + insets.bottom,
                  gap: 8,
                }}
                keyboardShouldPersistTaps="handled"
              >
                <Copy muted>{conversation.modelProvider}</Copy>
                <Copy muted>Used by this session until you change it.</Copy>
                {conversation.supportsReasoning ? (
                  conversation.reasoningEffortLevels?.map((effort) => (
                    <Choice
                      key={effort}
                      label={effort}
                      selected={effort === conversation.reasoningEffort}
                      disabled={!ready || state.pending !== null}
                      onPress={() => {
                        if (effort === conversation.reasoningEffort) close();
                        else
                          void controls
                            .setReasoningEffort(effort)
                            .then((changed) => {
                              if (changed) close();
                            });
                      }}
                    />
                  ))
                ) : (
                  <Copy muted>
                    This model does not offer reasoning choices.
                  </Copy>
                )}
                {state.pending ? (
                  <ActivityIndicator
                    accessibilityLabel="Updating reasoning effort"
                    color={colors.accent}
                  />
                ) : null}
                <ErrorMessage
                  message={
                    state.lastAction === "setReasoningEffort"
                      ? state.error
                      : null
                  }
                />
              </ScrollView>
            )}
          </View>
        </SafeAreaView>
      </KeyboardAvoidingView>
    </Modal>
  );
}
