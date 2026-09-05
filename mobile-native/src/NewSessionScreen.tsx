import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useMemo, useRef, useState } from "react";
import {
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  Text,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useStore } from "zustand";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { useConnection } from "./ConnectionProvider";
import { createNewSessionStore } from "./newSession";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

function Choice({
  label,
  selected,
  disabled,
  onPress,
}: {
  label: string;
  selected: boolean;
  disabled: boolean;
  onPress(): void;
}) {
  const colors = useColors();
  return (
    <Pressable
      accessibilityRole="radio"
      accessibilityLabel={label}
      accessibilityState={{ checked: selected, disabled }}
      disabled={disabled}
      onPress={onPress}
      style={[styles.action, { opacity: disabled ? 0.4 : 1 }]}
    >
      <Text
        style={{ color: selected ? colors.accent : colors.text, fontSize: 16 }}
      >
        {selected ? "● " : "○ "}
        {label}
      </Text>
    </Pressable>
  );
}

export function NewSessionScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "NewSession">) {
  const { activeProfile, client, state, retry } = useConnection();
  const colors = useColors();
  const headerHeight = useHeaderHeight();
  const hubId = route.params.hubId;
  const ready = activeProfile?.id === hubId && state === "ready" && !!client;
  const latest = useRef({ ready, client });
  latest.current = { ready, client };
  const store = useMemo(() => createNewSessionStore(hubId), [hubId]);
  const form = useStore(store);
  const [modelSearch, setModelSearch] = useState("");
  const emptyPromptReason = form.harnesses.find(
    (h) => h.id === form.harness,
  )?.emptyTaskUnsupportedReason;
  const service = useMemo(
    () => (ready && client ? createNewSessionService(client) : null),
    [ready, client],
  );
  useFocusEffect(
    useCallback(() => {
      store.getState().bind(service);
      if (service) {
        void store.getState().loadMetadata();
        void store.getState().loadModels();
      }
      return () => store.getState().bind(null);
    }, [store, service]),
  );
  const disabled = !ready || form.submitting;
  const inputStyle = [
    styles.input,
    {
      color: colors.text,
      backgroundColor: colors.surface,
      borderColor: colors.border,
    },
  ];
  async function submit() {
    if (!latest.current.ready) return;
    const submittedClient = latest.current.client;
    const outcome = await store.getState().submit();
    if (
      outcome.status !== "created" ||
      !navigation.isFocused() ||
      !latest.current.ready ||
      latest.current.client !== submittedClient
    )
      return;
    navigation.replace("Conversation", {
      hubId: outcome.hubId,
      ref: outcome.thread.evener.ref,
      title: outcome.thread.name || "Conversation",
    });
  }
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <KeyboardAvoidingView
        style={styles.fill}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
        keyboardVerticalOffset={headerHeight}
      >
        <ScrollView
          contentContainerStyle={styles.padded}
          keyboardShouldPersistTaps="handled"
        >
          <Copy muted>{route.params.hubName}</Copy>
          {!ready ? (
            <View>
              <Copy muted>
                Reconnect to this hub to create a session. Your input is kept.
              </Copy>
              <Action onPress={retry}>Reconnect</Action>
            </View>
          ) : null}
          <ErrorMessage message={form.error} />
          <Copy>Project directory</Copy>
          <TextInput
            accessibilityLabel="Project directory"
            value={form.cwd}
            editable={!form.submitting}
            onChangeText={(value) => {
              void form.setCwd(value, false);
            }}
            onBlur={() => {
              void form.loadModels();
            }}
            autoCapitalize="none"
            autoCorrect={false}
            placeholder="/path/on/hub"
            placeholderTextColor={colors.secondary}
            style={inputStyle}
          />
          {form.projects.length ? (
            <ScrollView horizontal keyboardShouldPersistTaps="handled">
              {form.projects.map((project) => (
                <Action
                  key={project}
                  disabled={disabled}
                  onPress={() => {
                    void form.setCwd(project);
                  }}
                >
                  {project}
                </Action>
              ))}
            </ScrollView>
          ) : null}
          <Copy>Harness</Copy>
          <View>
            <Choice
              label="Hub default harness"
              selected={!form.harness}
              disabled={disabled}
              onPress={() => {
                void form.setHarness("");
              }}
            />
            {form.harnesses.map((harness) => (
              <Choice
                key={harness.id}
                label={harness.label}
                selected={form.harness === harness.id}
                disabled={disabled}
                onPress={() => {
                  void form.setHarness(harness.id);
                }}
              />
            ))}
          </View>
          <Copy>Model</Copy>
          <Choice
            label="Hub default model"
            selected={!form.model}
            disabled={disabled}
            onPress={() => form.selectModel(null)}
          />
          {form.loadingModels ? <Copy muted>Loading models…</Copy> : null}
          <TextInput
            accessibilityLabel="Search models"
            value={modelSearch}
            onChangeText={setModelSearch}
            placeholder="Search models"
            placeholderTextColor={colors.secondary}
            style={inputStyle}
          />
          <ScrollView
            style={{ maxHeight: 220 }}
            nestedScrollEnabled
            keyboardShouldPersistTaps="handled"
          >
            {form.models
              .filter((model) =>
                `${model.displayName ?? ""} ${model.model} ${model.provider}`
                  .toLowerCase()
                  .includes(modelSearch.toLowerCase()),
              )
              .map((model) => (
                <Choice
                  key={JSON.stringify([model.provider, model.model])}
                  label={`${model.displayName || model.model} · ${model.provider}`}
                  selected={
                    form.model?.provider === model.provider &&
                    form.model.model === model.model
                  }
                  disabled={disabled}
                  onPress={() => form.selectModel(model)}
                />
              ))}
          </ScrollView>
          {form.model?.reasoningEffortLevels?.length ? (
            <View>
              <Copy>Reasoning effort</Copy>
              <Choice
                label="Default reasoning effort"
                selected={!form.reasoning}
                disabled={disabled}
                onPress={() => form.setReasoning("")}
              />
              {form.model.reasoningEffortLevels.map((effort) => (
                <Choice
                  key={effort}
                  label={effort}
                  selected={form.reasoning === effort}
                  disabled={disabled}
                  onPress={() => form.setReasoning(effort)}
                />
              ))}
            </View>
          ) : null}
          <ErrorMessage message={form.metadataError} />
          <ErrorMessage message={form.modelError} />
          {form.metadataError || form.modelError ? (
            <Action
              disabled={disabled}
              onPress={() => {
                void form.loadMetadata();
                void form.loadModels();
              }}
            >
              Retry options
            </Action>
          ) : null}
          <Copy>Opening prompt (optional)</Copy>
          <TextInput
            accessibilityLabel="Opening prompt"
            multiline
            value={form.prompt}
            editable={!form.submitting}
            onChangeText={form.setPrompt}
            placeholder="What would you like to work on?"
            placeholderTextColor={colors.secondary}
            style={[
              ...inputStyle,
              { minHeight: 120, textAlignVertical: "top" },
            ]}
          />
          {emptyPromptReason && !form.prompt.trim() ? (
            <Copy muted>{emptyPromptReason}</Copy>
          ) : null}
          <Action
            disabled={
              disabled ||
              !form.cwd.trim() ||
              (!!emptyPromptReason && !form.prompt.trim())
            }
            onPress={() => {
              void submit();
            }}
          >
            {form.submitting ? "Creating…" : "Create session"}
          </Action>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
