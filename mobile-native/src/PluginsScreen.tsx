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
import type { ConnectionState, PluginRefParams } from "@evener/appwire-client";
import { createPluginsStore } from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { ConnectionStatus } from "./ConnectionStatus";
import {
  isReady,
  useConnectionDisplay,
  useLiveReadiness,
  useRenderClient,
  whenReady,
} from "./connectionDisplay";
import {
  INSTALLED_PLUGINS_FAILED,
  MarketplaceBrowser,
} from "./MarketplaceBrowser";
import {
  createPluginMutationGate,
  PLUGIN_MUTATION_BUSY,
  runGatedMutation,
  type PluginMutationGate,
} from "./pluginMutationGate";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function PluginsScreen({
  route,
}: NativeStackScreenProps<Routes, "Plugins">) {
  // One plugin mutation at a time, across this list AND the browser: switching
  // tabs unmounts whichever one started it, so the gate lives here, at the
  // screen's own top level - above the early returns below, which unmount and
  // remount the ready-only child on every connection transition. A gate held
  // inside that child would be destroyed mid-mutation by the same transition
  // a write outlives, and a reopened screen would permit a second write
  // beside the first. It belongs to this screen rather than to the client - a
  // mutation outlives the store it was issued on, so a gate rebuilt per
  // client would let the next one start beside it - and is held the way the
  // credential store is (credentialStore.ts), as committed state a discarded
  // render cannot leave behind.
  const [gate] = useState(createPluginMutationGate);
  const { activeProfile, client, state, fatal, retry } = useConnection();
  const display = useConnectionDisplay(route.params.hubId, state, fatal);
  const canUseConnection = useLiveReadiness(route.params.hubId, client, state);
  // A flap keeps `client` set (the connection layer's own generation guard -
  // hubConnection.ts), but a manual retry clears it, then reports a fresh
  // client while it is still dialing; the list keeps rendering the previous
  // one through the whole gap, never the not-yet-ready replacement, rather
  // than dropping to the wall for a moment the banner should cover just as
  // well as a passive reconnect does. Scoped to the hub: see
  // useRenderClient's own doc.
  const renderClient = useRenderClient(client, state, route.params.hubId);
  if (activeProfile?.id !== route.params.hubId)
    return (
      <Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
    );
  if (display === "wall" || !renderClient)
    return (
      <View style={{ padding: 20 }}>
        <Copy>Connect to {activeProfile.name} to manage plugins.</Copy>
        <Action onPress={retry}>Reconnect</Action>
      </View>
    );
  return (
    <>
      {display === "banner" ? <ConnectionStatus /> : null}
      <Plugins
        key={activeProfile.id}
        client={renderClient}
        connectionState={state}
        canUseConnection={canUseConnection}
        hubName={activeProfile.name}
        gate={gate}
      />
    </>
  );
}

function Plugins({
  client,
  hubName,
  gate,
  connectionState,
  canUseConnection,
}: {
  client: ConversationClientLike;
  hubName: string;
  gate: PluginMutationGate;
  connectionState: ConnectionState;
  canUseConnection: () => boolean;
}) {
  const colors = useColors();
  const model = useMemo(() => createPluginsStore(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getState);
  const ready = isReady(connectionState);
  const [panel, setPanel] = useState<"installed" | "browse">("installed");
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
  // Tells the store which connection its list belongs to, on every
  // transition that connection reports - a passive flap keeps `client`
  // itself unchanged (this effect's other dep), so the mount effect below is
  // never rebuilt for one, and only this call tells the store the flap
  // happened and to recover once ready again (storeLifecycle.ts's
  // connectionChanged). Declared BEFORE the mount effect: on mount, nothing
  // has asked for the list yet, so this call's own "does anything want the
  // list" check (wantsList) is answered honestly before fetchPlugins() below
  // says yes - reversed, this call would see fetchPlugins()'s read already
  // marked live and refetch a second time for the same first load.
  useEffect(() => {
    model.connectionChanged(client, connectionState);
  }, [model, client, connectionState]);
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
    const outcome = await runGatedMutation(gate, canUseConnection, action);
    if (version !== editorVersion.current) return;
    if (outcome === "refused") setActionError(PLUGIN_MUTATION_BUSY);
    else if (outcome === "failed")
      setActionError(
        "Could not confirm the change. Check this plugin’s status before trying again.",
      );
    else if (success) setNotice(success);
  }
  function remove() {
    if (!selected || busy || !canUseConnection()) return;
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
          connectionState={connectionState}
          canUseConnection={canUseConnection}
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
            if (canUseConnection()) void state.fetchPlugins();
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
                  disabled={!ready}
                  onPress={whenReady(canUseConnection, () => {
                    void state.fetchPlugins();
                  })}
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
                  disabled={busy || !ready}
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
                  disabled={busy || !ready}
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
                disabled={busy || !ready}
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
              <Action disabled={busy || !ready} onPress={remove}>
                Remove plugin
              </Action>
            </ScrollView>
          </SafeAreaView>
        </Modal>
      )}
    </SafeAreaView>
  );
}
