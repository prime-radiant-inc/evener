import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  Keyboard,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useStore } from "zustand";
import { harnessSupportsPluginSelection } from "../../cmd/evener-hub/frontend/src/panes/spawn/harnessModels";
import { createNewSessionService } from "../../mobile/src/services/newSession";
import { useConnection } from "./ConnectionProvider";
import { CreationComposerSettings } from "./CreationComposerSettings";
import { CreationPlugins } from "./CreationPlugins";
import { creationImageDraft } from "./creationImageDraft";
import { HubPathField } from "./HubPathField";
import { ImageAttachments } from "./ImageAttachments";
import { ImageSelection } from "./imageSelection";
import { LaunchOverrides } from "./LaunchOverrides";
import { nativeImagePicker } from "./nativeImagePicker";
import { createNewSessionStore } from "./newSession";
import type { Routes } from "./screens";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function NewSessionScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "NewSession">) {
  const { activeProfile, client, state, retry } = useConnection();
  const colors = useColors();
  const headerHeight = useHeaderHeight();
  const formScroll = useRef<ScrollView>(null);
  const promptFocused = useRef(false);
  useEffect(() => {
    const shown = Keyboard.addListener("keyboardDidShow", () => {
      if (promptFocused.current)
        formScroll.current?.scrollToEnd({ animated: false });
    });
    return () => shown.remove();
  }, []);
  const hubId = route.params.hubId;
  const ready = activeProfile?.id === hubId && state === "ready" && !!client;
  const latest = useRef({ ready, client });
  latest.current = { ready, client };
  const store = useMemo(() => createNewSessionStore(hubId), [hubId]);
  const form = useStore(store);
  const imageDocument = useMemo(() => creationImageDraft(store), [store]);
  const imageSelection = useMemo(
    () => new ImageSelection(imageDocument, nativeImagePicker),
    [imageDocument],
  );
  const imageState = useSyncExternalStore(
    imageSelection.subscribe,
    imageSelection.getSnapshot,
  );
  useFocusEffect(
    useCallback(() => () => imageSelection.cancel(), [imageSelection]),
  );
  const [harnessOpen, setHarnessOpen] = useState(false);
  const emptyPromptReason = form.harnesses.find(
    (h) => h.id === form.harness,
  )?.emptyTaskUnsupportedReason;
  const service = useMemo(
    () => (ready && client ? createNewSessionService(client) : null),
    [ready, client],
  );
  useEffect(() => {
    store.getState().bind(service);
    return () => store.getState().bind(null);
  }, [store, service]);
  useFocusEffect(
    useCallback(() => {
      if (service) {
        void store.getState().loadMetadata();
        void store.getState().loadModels(true);
      }
    }, [store, service]),
  );
  const disabled =
    !ready || form.submitting || form.loadingModels || imageState.busy;
  const inputStyle = [
    styles.input,
    {
      color: colors.text,
      backgroundColor: colors.surface,
      borderColor: colors.border,
    },
  ];
  async function submit() {
    if (!latest.current.ready || imageSelection.getSnapshot().busy) return;
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
          ref={formScroll}
          onLayout={() => {
            if (promptFocused.current)
              formScroll.current?.scrollToEnd({ animated: false });
          }}
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
          {ready && client ? (
            <HubPathField
              client={client}
              kind="dir"
              label="Project directory"
              value={form.cwd}
              disabled={form.submitting}
              onChange={(value, selected) => {
                void form.setCwd(value, selected === true);
              }}
              onBlur={() => {
                void form.loadModels();
              }}
            />
          ) : (
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
          )}
          {form.projects.length ? (
            <ScrollView horizontal keyboardShouldPersistTaps="handled">
              {form.projects.map((project) => (
                <Action
                  key={project}
                  label={project}
                  disabled={disabled}
                  onPress={() => {
                    void form.setCwd(project);
                  }}
                >
                  {project.split("/").filter(Boolean).at(-1) || project}
                </Action>
              ))}
            </ScrollView>
          ) : null}
          <Action
            disabled={disabled || !form.cwd.trim()}
            onPress={() => {
              navigation.navigate("LaunchSettings", {
                hubId,
                projectCwd: form.cwd.trim(),
              });
            }}
          >
            Project launch settings
          </Action>
          <Action
            disabled={disabled}
            expanded={harnessOpen}
            tone="quiet"
            onPress={() => setHarnessOpen(!harnessOpen)}
          >
            {`Harness · ${form.harnesses.find((h) => h.id === form.harness)?.label || "Hub default"}`}
          </Action>
          {harnessOpen && (
            <View>
              <Choice
                label="Hub default harness"
                selected={!form.harness}
                disabled={disabled}
                onPress={() => {
                  void form.setHarness("");
                  setHarnessOpen(false);
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
                    setHarnessOpen(false);
                  }}
                />
              ))}
            </View>
          )}
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
          {ready &&
            client &&
            harnessSupportsPluginSelection(form.harness, form.harnesses) && (
              <CreationPlugins
                key={JSON.stringify([hubId, form.cwd.trim(), form.harness])}
                client={client}
                cwd={form.cwd.trim()}
                value={form.launchOverrides}
                onChange={form.setLaunchOverrides}
                disabled={disabled}
              />
            )}
          <LaunchOverrides
            key={JSON.stringify([hubId, form.cwd.trim()])}
            client={ready ? client : null}
            cwd={form.cwd.trim()}
            value={form.launchOverrides}
            onChange={form.setLaunchOverrides}
            disabled={disabled || !form.cwd.trim()}
          />
          <Copy>Opening prompt (optional)</Copy>
          <View
            style={{
              borderWidth: 1,
              borderColor: colors.border,
              borderRadius: 16,
              padding: 8,
              backgroundColor: colors.surface,
            }}
          >
            <TextInput
              accessibilityLabel="Opening prompt"
              onFocus={() => {
                promptFocused.current = true;
                formScroll.current?.scrollToEnd({ animated: false });
              }}
              onBlur={() => {
                promptFocused.current = false;
              }}
              multiline
              value={form.prompt}
              editable={!form.submitting}
              onChangeText={form.setPrompt}
              placeholder="What would you like to work on?"
              placeholderTextColor={colors.secondary}
              style={[
                ...inputStyle,
                { minHeight: 120, textAlignVertical: "top", borderWidth: 0 },
              ]}
            />
            <ImageAttachments
              document={imageDocument}
              selection={imageSelection}
              disabled={form.submitting}
            />
            <ErrorMessage message={imageState.error} />
            {emptyPromptReason && !form.prompt.trim() && !form.images.length ? (
              <Copy muted>{emptyPromptReason}</Copy>
            ) : null}
            <View style={[styles.row, { flexWrap: "wrap", gap: 8 }]}>
              <Action
                label="Attach images"
                disabled={form.submitting || imageState.busy}
                onPress={() => {
                  void imageSelection.choose();
                }}
              >
                {imageState.busy ? "Processing…" : "+"}
              </Action>
              <CreationComposerSettings
                key={hubId}
                models={form.models}
                model={form.model}
                reasoning={form.reasoning}
                overrides={form.launchOverrides}
                disabled={disabled}
                hubName={route.params.hubName}
                selectModel={form.selectModel}
                setReasoning={form.setReasoning}
              />
              <Action
                tone="primary"
                disabled={
                  disabled ||
                  !form.cwd.trim() ||
                  (!!emptyPromptReason &&
                    !form.prompt.trim() &&
                    !form.images.length)
                }
                onPress={() => {
                  void submit();
                }}
              >
                {form.submitting ? "Creating…" : "Create session"}
              </Action>
            </View>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
