import { useState } from "react";
import {
  FlatList,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  Text,
  TextInput,
  useWindowDimensions,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type {
  LaunchConfigLayer,
  ModelDescriptor,
} from "../../appwire-client/typescript/types.gen";
import { creationModel } from "./newSession";
import { Action, Choice, Copy, styles, useColors } from "./ui";

export function CreationComposerSettings({
  models,
  model,
  reasoning,
  overrides,
  disabled,
  hubName,
  selectModel,
  setReasoning,
}: {
  models: ModelDescriptor[];
  model: ModelDescriptor | null;
  reasoning: string;
  overrides: LaunchConfigLayer;
  disabled: boolean;
  hubName: string;
  selectModel(model: ModelDescriptor | null): void;
  setReasoning(value: string): void;
}) {
  const colors = useColors();
  const { fontScale } = useWindowDimensions();
  const scale = Platform.OS === "ios" ? fontScale : 1;
  const [setting, setSetting] = useState<"model" | "reasoning" | null>(null);
  const [query, setQuery] = useState("");
  const selected = creationModel(models, model, overrides);
  const modelLabel =
    selected?.displayName ||
    overrides.model ||
    selected?.model ||
    "Hub default";
  const effort = overrides.reasoningEffort || reasoning;
  const levels = selected?.reasoningEffortLevels ?? [];
  function control(
    label: string,
    accessible: string,
    target: "model" | "reasoning",
  ) {
    return (
      <Pressable
        accessibilityRole="button"
        accessibilityLabel={accessible}
        accessibilityState={{ disabled }}
        disabled={disabled}
        onPress={() => {
          setQuery("");
          setSetting(target);
        }}
        style={({ pressed }) => ({
          minHeight: Platform.OS === "ios" ? 44 : 48,
          flexDirection: "row",
          alignItems: "center",
          flexShrink: 1,
          gap: 4,
          opacity: disabled ? 0.4 : pressed ? 0.6 : 1,
        })}
      >
        <Text
          numberOfLines={1}
          allowFontScaling={Platform.OS !== "ios"}
          style={{
            color: colors.secondary,
            fontSize: 13 * scale,
            flexShrink: 1,
          }}
        >
          {label}
        </Text>
        <Text allowFontScaling={false} style={{ color: colors.secondary }}>
          ⌄
        </Text>
      </Pressable>
    );
  }
  return (
    <>
      <View
        style={{
          flexDirection: "row",
          flexWrap: "wrap",
          alignItems: "center",
          flexShrink: 1,
          flex: 1,
          minWidth: fontScale > 1.4 ? "100%" : 100,
          gap: 8,
        }}
      >
        {control(
          modelLabel,
          `Launch model: ${modelLabel}. Change model`,
          "model",
        )}
        {(levels.length > 0 || !!effort) &&
          control(
            effort || "Default",
            `Launch reasoning: ${effort || "default"}. Change reasoning`,
            "reasoning",
          )}
      </View>
      <Modal
        visible={!!setting}
        animationType="slide"
        presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
        onRequestClose={() => setSetting(null)}
      >
        <SafeAreaView
          style={[styles.fill, { backgroundColor: colors.background }]}
        >
          <View style={[styles.row, { paddingHorizontal: 16 }]}>
            <Copy>
              {setting === "model" ? "Choose model" : "Reasoning effort"}
            </Copy>
            <Action onPress={() => setSetting(null)}>Cancel</Action>
          </View>
          <KeyboardAvoidingView
            style={styles.fill}
            behavior={Platform.OS === "ios" ? "padding" : "height"}
          >
            <View style={{ paddingHorizontal: 20, gap: 8, flex: 1 }}>
              <Copy muted>{hubName}</Copy>
              {setting === "model" ? (
                <>
                  <TextInput
                    accessibilityLabel="Search launch models"
                    value={query}
                    onChangeText={setQuery}
                    autoCapitalize="none"
                    autoCorrect={false}
                    placeholder="Search models or providers"
                    placeholderTextColor={colors.secondary}
                    style={[
                      styles.input,
                      { color: colors.text, borderColor: colors.border },
                    ]}
                  />
                  <FlatList
                    keyboardShouldPersistTaps="handled"
                    data={models.filter((m) =>
                      `${m.displayName ?? ""} ${m.model} ${m.provider}`
                        .toLowerCase()
                        .includes(query.toLowerCase()),
                    )}
                    keyExtractor={(m) => JSON.stringify([m.provider, m.model])}
                    ListHeaderComponent={
                      <Choice
                        label="Hub default model"
                        selected={!selected && !overrides.model}
                        disabled={disabled}
                        onPress={() => {
                          selectModel(null);
                          setSetting(null);
                        }}
                      />
                    }
                    ListEmptyComponent={<Copy muted>No matching models.</Copy>}
                    renderItem={({ item }) => (
                      <Choice
                        label={`${item.displayName || item.model} · ${item.provider}`}
                        selected={
                          selected?.provider === item.provider &&
                          selected.model === item.model
                        }
                        disabled={disabled}
                        onPress={() => {
                          selectModel(item);
                          setSetting(null);
                        }}
                      />
                    )}
                  />
                </>
              ) : (
                <FlatList
                  data={["", ...levels]}
                  keyExtractor={(level) => level || "default"}
                  renderItem={({ item }) => (
                    <Choice
                      label={item || "Default reasoning effort"}
                      selected={effort === item}
                      disabled={disabled}
                      onPress={() => {
                        setReasoning(item);
                        setSetting(null);
                      }}
                    />
                  )}
                />
              )}
            </View>
          </KeyboardAvoidingView>
        </SafeAreaView>
      </Modal>
    </>
  );
}
