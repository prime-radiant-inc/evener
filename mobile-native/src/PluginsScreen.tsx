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
import type { PluginRefParams } from "@evener/appwire-client";
import { createPluginsStore } from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import {
  INSTALLED_PLUGINS_FAILED,
  MarketplaceBrowser,
} from "./MarketplaceBrowser";
import { createPluginMutationGate, PLUGIN_MUTATION_BUSY } from "./pluginMutationGate";
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
  const model = useMemo(() => createPluginsStore(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getState);
  const [panel, setPanel] = useState<"installed" | "browse">("installed");
  // One plugin mutation at a time, across this list AND the browser: switching
  // tabs unmounts whichever one started it, so the gate lives here, above
  // both. It belongs to this screen rather than to the client - a mutation
  // outlives the store it was issued on, so a gate rebuilt per client would
  // let the next one start beside it - and is held the way the credential
  // store is (credentialStore.ts), as committed state a discarded render
  // cannot leave behind.
  const [gate] = useState(createPluginMutationGate);
  const busy = useSyncExternalStore(gate.subscribe, gate.isBusy);
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
    void model.getState().fetchPlugins();
    return () => {
      editorVersion.current += 1;
      model.dispose();
    };
  }, [model]);
  const listError = state.pluginsError === null ? null : INSTALLED_PLUGINS_FAILED;
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
      const ran = await gate.run(action);
      if (version !== editorVersion.current) return;
      if (!ran) setActionError(PLUGIN_MUTATION_BUSY);
      else if (success) setNotice(success);
    } catch {
      if (version === editorVersion.current)
        setActionError(
          "Could not confirm the change. Check this plugin’s status before trying again.",
        );
    }
  }
  function remove() {
    if (!selected || busy) return;
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
              void act(() =>
                state.removePlugin(target.plugin, target.marketplace),
              );
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
          gate={gate}
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
          refreshing={state.pluginsLoading}
          onRefresh={() => {
            void state.fetchPlugins();
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
              <ErrorMessage message={listError} />
              {listError && (
                <Action
                  onPress={() => {
                    void state.fetchPlugins();
                  }}
                >
                  Retry
                </Action>
              )}
            </View>
          }
          ListEmptyComponent={
            state.pluginsLoading ? (
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
              accessibilityLabel={`${item.plugin}, ${item.marketplace}, ${item.broken ? "broken" : item.enabled ? "enabled by default" : "off by default"}`}
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
                {item.broken ? " · Broken" : !item.enabled ? " · Off by default" : ""}
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
              {busy && (
                <ActivityIndicator accessibilityLabel="Updating plugin" />
              )}
              <View style={[styles.row, { minHeight: 48, gap: 16 }]}>
                <View style={styles.fill}>
                  <Copy>Enabled by default</Copy>
                </View>
                <Switch
                  accessibilityLabel="Plugin enabled by default"
                  value={entry.enabled}
                  disabled={busy}
                  onValueChange={(enabled) => {
                    const target = selected;
                    void act(() =>
                      enabled
                        ? state.enablePlugin(target.plugin, target.marketplace)
                        : state.disablePlugin(target.plugin, target.marketplace),
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
                  disabled={busy}
                  onValueChange={(value) => {
                    const target = selected;
                    void act(() =>
                      state.setPluginAutoUpgrade(
                        target.plugin,
                        target.marketplace,
                        value,
                      ),
                    );
                  }}
                />
              </View>
              <Action
                disabled={busy}
                onPress={() => {
                  const target = selected;
                  void act(
                    () => state.upgradePlugin(target.plugin, target.marketplace),
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
              <Action disabled={busy} onPress={remove}>
                Remove plugin
              </Action>
            </ScrollView>
          </SafeAreaView>
        </Modal>
      )}
    </SafeAreaView>
  );
}
