import { useRef, useState } from "react";
import {
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  addEnvironmentVariable,
  assertEnvironmentCurrent,
  collectEnvironment,
} from "./launchEnvironment";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchEnvironmentEditor({
  option,
  value,
  effective,
  close,
  apply,
}: {
  option: LaunchOption;
  value: Record<string, string> | undefined;
  effective: Record<string, string> | undefined;
  close(): void;
  apply(value: Record<string, string> | undefined): void;
}) {
  const colors = useColors();
  const original = useRef(value);
  const [draft, setDraft] = useState(value ?? {});
  const [name, setName] = useState("");
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const inputStyle = [
    styles.input,
    { color: colors.text, borderColor: colors.border },
  ];
  function done() {
    try {
      // Include a pending pair so Done cannot silently discard typed input.
      const next = collectEnvironment(
        name || text ? addEnvironmentVariable(draft, name, text) : draft,
      );
      assertEnvironmentCurrent(original.current, value, next);
      apply(next);
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "Unable to apply environment variables.",
      );
    }
  }
  return (
    <Modal
      visible
      presentationStyle="pageSheet"
      animationType="slide"
      onRequestClose={close}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View style={[styles.row, { paddingHorizontal: 16 }]}>
          <Action onPress={close}>Cancel</Action>
          <Action onPress={done}>Done</Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "android" ? "height" : undefined}
          enabled={Platform.OS === "android"}
        >
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
              selected={Object.keys(draft).length === 0 && !name && !text}
              onPress={() => {
                setDraft({});
                setName("");
                setText("");
                setError(null);
              }}
            />
            <Copy muted>
              Effective variables:{" "}
              {Object.keys(effective ?? {}).join(", ") || "None"}
            </Copy>
            <ErrorMessage message={error} />
            {Object.entries(draft).map(([key, val]) => (
              <View
                key={key}
                style={{
                  borderBottomWidth: 0.5,
                  borderColor: colors.border,
                  paddingVertical: 6,
                  flexDirection: "row",
                  alignItems: "center",
                  gap: 8,
                }}
              >
                <View style={{ flex: 1, minWidth: 0 }}>
                  <Copy>{key}</Copy>
                  <Copy muted>{val || "Empty value"}</Copy>
                </View>
                <Action
                  label={`Remove variable ${key}`}
                  onPress={() => {
                    const next = { ...draft };
                    delete next[key];
                    setDraft(next);
                  }}
                >
                  Remove
                </Action>
              </View>
            ))}
            <Copy muted>
              Adding an existing name replaces its value. Removing every entry
              restores inheritance.
            </Copy>
            <TextInput
              accessibilityLabel="Variable name"
              placeholder="NAME"
              placeholderTextColor={colors.secondary}
              autoCapitalize="none"
              autoCorrect={false}
              value={name}
              onChangeText={setName}
              style={inputStyle}
            />
            <TextInput
              accessibilityLabel="Variable value"
              placeholder="Value (may be empty)"
              placeholderTextColor={colors.secondary}
              autoCapitalize="none"
              autoCorrect={false}
              value={text}
              onChangeText={setText}
              style={inputStyle}
            />
            <Action
              disabled={!name.trim()}
              onPress={() => {
                try {
                  setDraft(addEnvironmentVariable(draft, name, text));
                  setName("");
                  setText("");
                  setError(null);
                } catch (err) {
                  setError(
                    err instanceof Error
                      ? err.message
                      : "Unable to add variable.",
                  );
                }
              }}
            >
              Add variable
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
