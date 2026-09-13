import { useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { schemaPathKind } from "../../cmd/evener-hub/frontend/src/panes/settings/sections/launchShared/schema";
import { friendlyErrorMessage } from "../../appwire-client/typescript/errors";
import type { LaunchOption } from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubPathField } from "./HubPathField";
import { LaunchModelPicker } from "./LaunchModelPicker";
import {
  assertLaunchFieldCurrent,
  launchFieldConflictMessage,
  parseLaunchScalar,
} from "./launchScalar";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchScalarEditor({
  option,
  value,
  effective,
  client,
  close,
  apply,
}: {
  option: LaunchOption;
  value: string;
  effective: string;
  client: ConversationClientLike | null;
  close(): void;
  apply(value: string | number | boolean | undefined): void;
}) {
  const colors = useColors();
  const [raw, setRaw] = useState(value);
  const originalValue = useRef(value);
  const currentValue = useRef(value);
  currentValue.current = value;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  const validation = useRef(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: A new transport invalidates validation replies from the old connection.
  useEffect(() => {
    validation.current += 1;
    setBusy(false);
  }, [client]);
  async function done() {
    if (busy) return;
    const generation = ++validation.current;
    const active = () => alive.current && generation === validation.current;
    setError(null);
    setBusy(true);
    try {
      const parsed = parseLaunchScalar(option, raw);
      if (parsed !== undefined && option.kind === "path") {
        if (!client) {
          setError("Reconnect to validate this path on the hub.");
          return;
        }
        const validation = await client.request("evener/path/validate", {
          path: String(parsed),
          kind: schemaPathKind(option.pathKind),
        });
        if (!validation.valid) {
          if (active())
            setError(validation.error || "This path is not valid on the hub.");
          return;
        }
      }
      if (active()) {
        assertLaunchFieldCurrent(
          originalValue.current,
          currentValue.current,
          parsed,
        );
        apply(parsed);
      }
    } catch (err) {
      if (active())
        setError(
          err instanceof Error &&
            [
              "Enter a whole number.",
              "Choose an available value.",
              "Choose an on or off value.",
              launchFieldConflictMessage,
            ].includes(err.message)
            ? err.message
            : friendlyErrorMessage(err),
        );
    } finally {
      if (active()) setBusy(false);
    }
  }
  const choices =
    option.kind === "boolean"
      ? [
          { value: "true", label: "On" },
          { value: "false", label: "Off" },
        ]
      : option.choices?.filter((choice) => choice.value !== "");
  const fieldHeader = (
    <>
      <Copy>{option.label}</Copy>
      {option.description && <Copy muted>{option.description}</Copy>}
      <Copy muted>
        {effective
          ? `Effective value · ${effective}`
          : "Effective value unavailable"}
      </Copy>
      <Choice
        label="Use inherited value"
        selected={raw === ""}
        disabled={busy}
        onPress={() => setRaw("")}
      />
      <ErrorMessage message={error} />
    </>
  );
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
          <Action
            disabled={busy}
            onPress={() => {
              void done();
            }}
          >
            Done
          </Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "android" ? "height" : undefined}
          enabled={Platform.OS === "android"}
        >
          {option.kind === "modelPicker" ? (
            <LaunchModelPicker
              client={client}
              value={raw}
              onChange={setRaw}
              header={fieldHeader}
            />
          ) : (
            <ScrollView
              automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
              keyboardShouldPersistTaps="handled"
              contentContainerStyle={{ padding: 20, gap: 12 }}
            >
              {fieldHeader}
              {choices ? (
                choices.map((choice) => (
                  <Choice
                    key={choice.value}
                    label={choice.label}
                    selected={raw === choice.value}
                    disabled={busy || !!choice.disabled}
                    onPress={() => setRaw(choice.value)}
                  />
                ))
              ) : client && option.kind === "path" ? (
                <HubPathField
                  kind={
                    option.pathKind === "file" ||
                    option.pathKind === "outputFile"
                      ? option.pathKind
                      : "dir"
                  }
                  client={client}
                  label={option.label}
                  value={raw}
                  onChange={setRaw}
                  disabled={busy}
                />
              ) : (
                <TextInput
                  accessibilityLabel={option.label}
                  value={raw}
                  onChangeText={setRaw}
                  editable={!busy}
                  autoCorrect={false}
                  autoCapitalize="none"
                  multiline={option.kind === "multilineText"}
                  keyboardType={
                    option.kind === "integer"
                      ? "numbers-and-punctuation"
                      : "default"
                  }
                  style={[
                    styles.input,
                    {
                      color: colors.text,
                      borderColor: colors.border,
                      minHeight: option.kind === "multilineText" ? 160 : 44,
                    },
                  ]}
                />
              )}
              {busy && (
                <ActivityIndicator accessibilityLabel="Validating setting" />
              )}
            </ScrollView>
          )}
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
