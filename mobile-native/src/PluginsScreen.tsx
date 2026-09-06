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
  ActivityIndicator,
  Alert,
  FlatList,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  Switch,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { PluginRefParams } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { InstalledPlugins } from "./installedPlugins";
import { MarketplaceBrowser } from "./MarketplaceBrowser";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function PluginsScreen({
  route,
}: NativeStackScreenProps<Routes, "Plugins">) {
  const { activeProfile, client, state, retry } = useConnection();
  if (activeProfile?.id !== route.params.hubId)
    return (
      <Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
    );
  if (!client || state !== "ready")
    return (
      <View style={{ padding: 20 }}>
        <Copy>Connect to {activeProfile.name} to manage plugins.</Copy>
        <Action onPress={retry}>Reconnect</Action>
      </View>
    );
  return (
    <Plugins
      key={activeProfile.id}
      client={client}
      hubName={activeProfile.name}
    />
  );
}

function Plugins({
  client,
  hubName,
}: {
  client: ConversationClientLike;
  hubName: string;
}) {
  const colors = useColors();
  const model = useMemo(() => new InstalledPlugins(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const [panel, setPanel] = useState<"installed" | "browse">("installed");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<PluginRefParams | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [details, setDetails] = useState(false);
  const editorVersion = useRef(0);
  const entry = state.plugins?.find(
    (item) =>
      item.plugin === selected?.plugin &&
      item.marketplace === selected?.marketplace,
  );
  const needle = query.trim().toLowerCase();
  const visible = (state.plugins ?? []).filter(
    (item) =>
      item.plugin.toLowerCase().includes(needle) ||
      item.marketplace.toLowerCase().includes(needle),
  );
  useEffect(() => {
    model.start();
    return () => {
      editorVersion.current += 1;
      model.dispose();
    };
  }, [model]);
  const close = useCallback(() => {
    editorVersion.current += 1;
    setSelected(null);
    setActionError(null);
    setNotice(null);
    setDetails(false);
  }, []);
  useEffect(() => {
    if (selected && state.plugins && !entry) close();
  }, [selected, state.plugins, entry, close]);
  async function act(action: () => Promise<void>, success?: string) {
    const version = editorVersion.current;
    setActionError(null);
    setNotice(null);
    try {
      await action();
      if (version === editorVersion.current && success) setNotice(success);
    } catch {
      if (version === editorVersion.current)
        setActionError(
          "Could not confirm the change. Check this plugin’s status before trying again.",
        );
    }
  }
  function remove() {
    if (!selected || state.busy) return;
    const target = selected;
    const version = editorVersion.current;
    Alert.alert(
      "Remove plugin?",
      `${target.plugin} from ${target.marketplace} on ${hubName}`,
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Remove",
          style: "destructive",
          onPress: () => {
            if (version === editorVersion.current)
              void act(() => model.remove(target));
          },
        },
      ],
    );
  }
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <View style={[styles.row, { paddingHorizontal: 12 }]}>
        <Action
          tone={panel === "installed" ? "accent" : "quiet"}
          onPress={() => setPanel("installed")}
        >
          Installed
        </Action>
        <Action
          tone={panel === "browse" ? "accent" : "quiet"}
          onPress={() => setPanel("browse")}
        >
          Browse
        </Action>
      </View>
      {panel === "browse" ? (
        <MarketplaceBrowser
          client={client}
          hubName={hubName}
          installed={model}
          onOpenPlugin={(target) => {
            close();
            setSelected(target);
          }}
        />
      ) : (
        <FlatList
          data={visible}
          keyExtractor={(item) =>
            JSON.stringify([item.marketplace, item.plugin])
          }
          contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
          keyboardShouldPersistTaps="handled"
          refreshing={state.loading}
          onRefresh={() => {
            void model.refresh();
          }}
          ListHeaderComponent={
            <View style={{ gap: 8, paddingBottom: 12 }}>
              <Copy muted>{hubName}</Copy>
              <TextInput
                accessibilityLabel="Filter installed plugins"
                placeholder="Filter plugins or marketplaces"
                placeholderTextColor={colors.secondary}
                value={query}
                onChangeText={setQuery}
                autoCorrect={false}
                autoCapitalize="none"
                style={[
                  styles.input,
                  { color: colors.text, borderColor: colors.border },
                ]}
              />
              <ErrorMessage message={state.error} />
              {state.error && (
                <Action
                  onPress={() => {
                    void model.refresh();
                  }}
                >
                  Retry
                </Action>
              )}
            </View>
          }
          ListEmptyComponent={
            state.loading ? (
              <ActivityIndicator accessibilityLabel="Loading installed plugins" />
            ) : state.plugins !== null ? (
              <Copy muted>
                {needle
                  ? "No matching plugins."
                  : "No plugins installed on this hub."}
              </Copy>
            ) : null
          }
          renderItem={({ item }) => (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`${item.plugin}, ${item.marketplace}, ${item.broken ? "broken" : item.enabled ? "enabled" : "disabled"}`}
              onPress={() => {
                close();
                setSelected({
                  plugin: item.plugin,
                  marketplace: item.marketplace,
                });
              }}
              style={({ pressed }) => ({
                minHeight: 56,
                paddingVertical: 9,
                borderBottomWidth: 0.5,
                borderColor: colors.border,
                opacity: pressed ? 0.65 : 1,
              })}
            >
              <Copy numberOfLines={2}>{item.plugin}</Copy>
              <Copy muted numberOfLines={2}>
                {item.marketplace} · {item.version || "Unknown version"}
                {item.broken ? " · Broken" : !item.enabled ? " · Disabled" : ""}
                {item.autoUpgrade ? " · Auto-upgrade" : ""}
              </Copy>
            </Pressable>
          )}
        />
      )}
      {entry && selected && (
        <Modal
          visible
          animationType="slide"
          presentationStyle="pageSheet"
          onRequestClose={close}
        >
          <SafeAreaView
            style={[styles.fill, { backgroundColor: colors.background }]}
          >
            <View style={[styles.row, { paddingHorizontal: 16 }]}>
              <View style={styles.fill}>
                <Copy muted>{hubName}</Copy>
              </View>
              <Action onPress={close}>Done</Action>
            </View>
            <ScrollView
              automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
              contentContainerStyle={{ padding: 20, gap: 12 }}
            >
              <Copy>{entry.plugin}</Copy>
              <Copy muted>
                {entry.marketplace} · {entry.version || "Unknown version"}
              </Copy>
              {entry.broken && (
                <ErrorMessage message="This plugin is broken. Check its source or try upgrading it." />
              )}
              <ErrorMessage message={actionError} />
              {notice && <Copy>{notice}</Copy>}
              {state.busy && (
                <ActivityIndicator accessibilityLabel="Updating plugin" />
              )}
              <View style={[styles.row, { minHeight: 48, gap: 16 }]}>
                <View style={styles.fill}>
                  <Copy>Enabled</Copy>
                </View>
                <Switch
                  accessibilityLabel="Plugin enabled"
                  value={entry.enabled}
                  disabled={state.busy}
                  onValueChange={(enabled) => {
                    const target = selected;
                    void act(() =>
                      enabled ? model.enable(target) : model.disable(target),
                    );
                  }}
                />
              </View>
              <View style={[styles.row, { minHeight: 48, gap: 16 }]}>
                <View style={styles.fill}>
                  <Copy>Automatic upgrades</Copy>
                </View>
                <Switch
                  accessibilityLabel="Automatic plugin upgrades"
                  value={entry.autoUpgrade}
                  disabled={state.busy}
                  onValueChange={(value) => {
                    const target = selected;
                    void act(() => model.setAutoUpgrade(target, value));
                  }}
                />
              </View>
              <Action
                disabled={state.busy}
                onPress={() => {
                  const target = selected;
                  void act(
                    () => model.upgrade(target),
                    "Checked for upgrades.",
                  );
                }}
              >
                Upgrade
              </Action>
              <Action
                expanded={details}
                onPress={() => setDetails((value) => !value)}
              >
                Installation details
              </Action>
              {details && (
                <>
                  <Copy muted>Path on {hubName}</Copy>
                  <Copy>{entry.installPath}</Copy>
                  {entry.gitCommitSha && (
                    <Copy muted>{entry.gitCommitSha}</Copy>
                  )}
                </>
              )}
              <Action disabled={state.busy} onPress={remove}>
                Remove plugin
              </Action>
            </ScrollView>
          </SafeAreaView>
        </Modal>
      )}
    </SafeAreaView>
  );
}
