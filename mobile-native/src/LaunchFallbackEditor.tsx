import { useRef, useState } from "react";
import {
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchModelPicker } from "./LaunchModelPicker";
import {
  addFallback,
  assertLaunchListCurrent,
  collectFallbacks,
} from "./launchLists";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchFallbackEditor({
  option,
  value,
  effective,
  client,
  close,
  apply,
}: {
  option: LaunchOption;
  value: string[] | undefined;
  effective: string[] | undefined;
  client: ConversationClientLike | null;
  close(): void;
  apply(value: string[] | undefined): void;
}) {
  const colors = useColors();
  const original = useRef(value);
  const [items, setItems] = useState(value ?? []);
  const [explicitEmpty, setExplicitEmpty] = useState(value?.length === 0);
  const [choosing, setChoosing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  function done() {
    try {
      const next = collectFallbacks(items, explicitEmpty);
      assertLaunchListCurrent(original.current, value, next);
      apply(next);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Unable to apply fallbacks.",
      );
    }
  }
  return (
    <Modal
      visible
      presentationStyle="pageSheet"
      animationType="slide"
      onRequestClose={() => (choosing ? setChoosing(false) : close())}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View style={[styles.row, { paddingHorizontal: 16 }]}>
          <Action onPress={() => (choosing ? setChoosing(false) : close())}>
            {choosing ? "Back" : "Cancel"}
          </Action>
          {!choosing && <Action onPress={done}>Done</Action>}
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "android" ? "height" : undefined}
          enabled={Platform.OS === "android"}
        >
          {choosing ? (
            <LaunchModelPicker
              client={client}
              value=""
              header={
                <>
                  <Copy>Add a fallback model</Copy>
                  <ErrorMessage message={error} />
                </>
              }
              onChange={(model) => {
                try {
                  setItems(addFallback(items, model));
                  setExplicitEmpty(false);
                  setChoosing(false);
                  setError(null);
                } catch (err) {
                  setError(
                    err instanceof Error
                      ? err.message
                      : "Unable to add fallback.",
                  );
                }
              }}
            />
          ) : (
            <ScrollView
              automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
              keyboardShouldPersistTaps="handled"
              contentContainerStyle={{ padding: 20, gap: 12 }}
            >
              <Copy>{option.label}</Copy>
              {option.description && <Copy muted>{option.description}</Copy>}
              <Choice
                label="Use inherited value"
                disabled={false}
                selected={!items.length && !explicitEmpty}
                onPress={() => {
                  setItems([]);
                  setExplicitEmpty(false);
                  setError(null);
                }}
              />
              <Choice
                label="No model fallbacks"
                disabled={false}
                selected={!items.length && explicitEmpty}
                onPress={() => {
                  setItems([]);
                  setExplicitEmpty(true);
                  setError(null);
                }}
              />
              <Copy muted>
                Effective fallbacks · {effective?.join(" → ") || "None"}
              </Copy>
              <ErrorMessage message={error} />
              {items.map((model, index) => (
                <View
                  key={model}
                  style={{
                    flexDirection: "row",
                    alignItems: "center",
                    gap: 8,
                    paddingVertical: 6,
                    borderBottomWidth: 0.5,
                    borderColor: colors.border,
                  }}
                >
                  <View style={{ flex: 1, minWidth: 0 }}>
                    <Copy>
                      {index + 1}. {model}
                    </Copy>
                  </View>
                  <Action
                    label={`Remove fallback ${model}`}
                    onPress={() =>
                      setItems(items.filter((item) => item !== model))
                    }
                  >
                    Remove
                  </Action>
                </View>
              ))}
              <Action
                disabled={!client}
                onPress={() => {
                  setError(null);
                  setChoosing(true);
                }}
              >
                Add model
              </Action>
              {!client && (
                <Copy muted>
                  Reconnect to browse this hub's models. Your fallbacks are kept
                  here.
                </Copy>
              )}
            </ScrollView>
          )}
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
