import { usePreventRemove } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import {
  inactivePromptDependent,
  schemaPathKind,
} from "../../cmd/evener-hub/frontend/src/panes/settings/sections/launchShared/schema";
import { friendlyErrorMessage } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  LaunchConfigLayer,
  LaunchOption,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { HubDirectoryField } from "./HubDirectoryField";
import { parseLaunchScalar, scalarKinds } from "./launchScalar";
import { LaunchSettings } from "./launchSettings";
import type { Routes } from "./screens";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

type Props = NativeStackScreenProps<Routes, "LaunchSettings">;
export function LaunchSettingsScreen({ route, navigation }: Props) {
  const { activeProfile, client, state, retry } = useConnection();
  if (activeProfile?.id !== route.params.hubId)
    return <Copy>This hub is no longer selected.</Copy>;
  if (!client || state !== "ready")
    return (
      <View style={{ padding: 20 }}>
        <Copy>Reconnect to edit launch defaults.</Copy>
        <Action onPress={retry}>Reconnect</Action>
      </View>
    );
  return (
    <LaunchDefaults
      client={client}
      hubName={activeProfile.name}
      navigation={navigation}
    />
  );
}
function scalarValue(config: LaunchConfigLayer | null, field: string): string {
  const value = (config as Record<string, unknown> | null)?.[field];
  return value === undefined ? "" : String(value);
}
function LaunchDefaults({
  client,
  hubName,
  navigation,
}: {
  client: ConversationClientLike;
  hubName: string;
  navigation: Props["navigation"];
}) {
  const colors = useColors();
  const model = useMemo(
    () => new LaunchSettings(client, "/", "global"),
    [client],
  );
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const [selected, setSelected] = useState<LaunchOption | null>(null);
  const [query, setQuery] = useState("");
  const [notice, setNotice] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    model.start();
    void model.refresh();
    return () => {
      alive.current = false;
      model.dispose();
    };
  }, [model]);
  usePreventRemove(state.dirty || state.saving, ({ data }) => {
    if (state.saving) return;
    Alert.alert(
      "Discard launch changes?",
      "Your unsaved changes will be lost.",
      [
        { text: "Keep editing", style: "cancel" },
        {
          text: "Discard",
          style: "destructive",
          onPress: () => navigation.dispatch(data.action),
        },
      ],
    );
  });
  const options = (state.options ?? []).filter(
    (option) =>
      option.defaultableLayers?.includes("global") &&
      scalarKinds.has(option.kind) &&
      !inactivePromptDependent(option.wireField, state.draft ?? {}) &&
      `${option.label} ${option.group} ${option.description ?? ""}`
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <ScrollView
        keyboardShouldPersistTaps="handled"
        contentContainerStyle={{ padding: 20, gap: 10 }}
      >
        <Copy>{hubName}</Copy>
        <Copy muted>
          Defaults for new Evener sessions. Project and per-launch settings can
          override these values.
        </Copy>
        <View style={[styles.row, { flexWrap: "wrap" }]}>
          <Action
            disabled={
              !state.dirty ||
              state.loading ||
              state.saving ||
              state.changedElsewhere
            }
            onPress={() => {
              setNotice(null);
              void model.save().then((saved) => {
                if (saved && alive.current) setNotice("Launch defaults saved.");
              });
            }}
          >
            Save defaults
          </Action>
          <Action
            disabled={state.loading || state.saving}
            onPress={() => {
              const reload = () => {
                setNotice(null);
                void model.refresh(true);
              };
              if (state.dirty)
                Alert.alert(
                  "Reload launch defaults?",
                  "This discards your unsaved changes.",
                  [
                    { text: "Cancel", style: "cancel" },
                    { text: "Reload", style: "destructive", onPress: reload },
                  ],
                );
              else reload();
            }}
          >
            Reload
          </Action>
        </View>
        {state.loading && (
          <ActivityIndicator accessibilityLabel="Loading launch defaults" />
        )}
        {state.saving && (
          <ActivityIndicator accessibilityLabel="Saving launch defaults" />
        )}
        <ErrorMessage message={state.error} />
        <ErrorMessage message={state.resolveError} />
        {state.changedElsewhere && (
          <Copy>Launch settings changed elsewhere. Reload before saving.</Copy>
        )}
        {notice && <Copy>{notice}</Copy>}
        {state.resolved?.diagnostics?.map((d) => (
          <Copy muted key={JSON.stringify(d)}>
            {d.field ? `${d.field}: ${d.message}` : d.message}
          </Copy>
        ))}
        <TextInput
          accessibilityLabel="Search launch settings"
          value={query}
          onChangeText={setQuery}
          placeholder="Find a setting"
          placeholderTextColor={colors.secondary}
          style={[
            styles.input,
            { color: colors.text, borderColor: colors.border },
          ]}
        />
        {options.map((option, index) => (
          <View key={option.wireField}>
            {(index === 0 || options[index - 1]?.group !== option.group) && (
              <View style={{ paddingTop: 14, paddingBottom: 4 }}>
                <Copy muted>{option.group}</Copy>
              </View>
            )}
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Edit ${option.label}`}
              disabled={state.loading || state.saving}
              onPress={() => setSelected(option)}
              style={{
                minHeight: 48,
                paddingVertical: 7,
                borderBottomWidth: 0.5,
                borderColor: colors.border,
              }}
            >
              <Copy>{option.label}</Copy>
              <Copy muted numberOfLines={2}>
                {scalarValue(state.draft, option.wireField) ||
                  `Inherited · ${scalarValue(state.resolved?.effective ?? null, option.wireField) || "Default"}`}
              </Copy>
            </Pressable>
          </View>
        ))}
      </ScrollView>
      {selected && (
        <ScalarEditor
          key={selected.wireField}
          option={selected}
          value={scalarValue(state.draft, selected.wireField)}
          effective={scalarValue(
            state.resolved?.effective ?? null,
            selected.wireField,
          )}
          client={client}
          close={() => setSelected(null)}
          apply={(value) => {
            model.edit(selected.wireField as keyof LaunchConfigLayer, value);
            setNotice(null);
            setSelected(null);
          }}
        />
      )}
    </SafeAreaView>
  );
}
function ScalarEditor({
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
  client: ConversationClientLike;
  close(): void;
  apply(value: string | number | boolean | undefined): void;
}) {
  const colors = useColors();
  const [raw, setRaw] = useState(value);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  async function done() {
    if (busy) return;
    setError(null);
    setBusy(true);
    try {
      const parsed = parseLaunchScalar(option, raw);
      if (parsed !== undefined && option.kind === "path") {
        const validation = await client.request("evener/path/validate", {
          path: String(parsed),
          kind: schemaPathKind(option.pathKind),
        });
        if (!validation.valid) {
          if (alive.current)
            setError(validation.error || "This path is not valid on the hub.");
          return;
        }
      }
      if (alive.current) apply(parsed);
    } catch (err) {
      if (alive.current)
        setError(
          err instanceof Error &&
            [
              "Enter a whole number.",
              "Choose an available value.",
              "Choose an on or off value.",
            ].includes(err.message)
            ? err.message
            : friendlyErrorMessage(err),
        );
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  const choices =
    option.kind === "boolean"
      ? [
          { value: "true", label: "On" },
          { value: "false", label: "Off" },
        ]
      : option.choices?.filter((choice) => choice.value !== "");
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
          <ScrollView
            automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 20, gap: 12 }}
          >
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
            ) : option.kind === "path" && option.pathKind === "dir" ? (
              <HubDirectoryField
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
            <ErrorMessage message={error} />
            {busy && (
              <ActivityIndicator accessibilityLabel="Validating setting" />
            )}
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
