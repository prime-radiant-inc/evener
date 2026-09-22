import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  useCallback,
  useEffect,
	useLayoutEffect,
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
import type {
  ConnectionState,
  MarketplaceEntry,
  PluginRefParams,
} from "@evener/appwire-client";
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
import { sameRemovedRegistration } from "./marketplaceBrowserModel";
import {
  createPluginMutationGate,
  PLUGIN_MUTATION_BUSY,
  runGatedMutation,
  type PluginMutationGate,
} from "./pluginMutationGate";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

/** The registration an applied removal took out, as the wire names it: the
 * source the hub recorded for it and the whole-second `lastUpdated` stamp
 * it carries. A row carrying exactly this identity is the removed
 * registration itself; any other identity - or no row at all - is a
 * replacement or an absence. */
type RemovedRegistration = {
  source: MarketplaceEntry["source"];
  lastUpdated: MarketplaceEntry["lastUpdated"];
};

/** The applied marketplace removals one client's writes reported, keyed by
 * name to the registration the write removed: names a write said the hub
 * already removed, held with the client whose write said so because a fresh
 * browser must not offer Remove again for any of them. The fence covers the
 * name from the applied outcome until the authoritative reads establish
 * what the hub now carries: a list omitting the name (the removal
 * reconciled) or carrying a different registration (a re-add - a fresh
 * stamp, which the hub writes on every registration, or a different source
 * within the wire's whole-second stamp - internal/plugins/marketplaces.go)
 * clears it, and only a stale row still carrying the removed
 * registration's own identity keeps it. */
type AppliedRemovalGuard = {
  client: ConversationClientLike | null;
  entries: ReadonlyMap<string, RemovedRegistration>;
};

const EMPTY_APPLIED_REMOVALS: ReadonlySet<string> = new Set();

// A mounted screen re-keyed to another hub is a fresh screen: the
// reconnect-retention state below - the banner's everReady, the last
// client a retry's gap renders through - belongs to the hub it was built
// for, and none of it may survive a hub the route now names. React
// Navigation can update a mounted instance's params (setParams on a
// focused screen is this app's own idiom - see
// KeybindingPreferencesScreen), so the body is keyed to the hub id and a
// re-key remounts it whole.
export function PluginsScreen(props: NativeStackScreenProps<Routes, "Plugins">) {
  return <PluginsScreenBody key={props.route.params.hubId} {...props} />;
}

function PluginsScreenBody({
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
  const display = useConnectionDisplay(state, fatal);
  const canUseConnection = useLiveReadiness(route.params.hubId, client, state);
  // A flap keeps `client` set (the connection layer's own generation guard -
  // hubConnection.ts), but a manual retry briefly clears it while it opens a
  // fresh one; the last client this screen had keeps the list mounted
  // through that gap too, rather than dropping to the wall for a moment the
  // banner should cover just as well as a passive reconnect does.
  const renderClient = useRenderClient(client);
  // An applied marketplace removal's residue lives beside the gate, above the
  // early returns below, for the same reason it does: they unmount and remount
  // the ready-only child on every connection transition, and the browser that
  // asked for the write does not outlive even a tab switch - a remount
  // replaces its store, and its select() clears the slot an outcome rendered
  // into. The guard fences a name the hub already removed and the warning
  // survives every one of those remounts, both scoped to the client whose
  // write reported them, so a replaced client's late result changes nothing
  // (currentClient's checks below).
  const currentClient = useRef<ConversationClientLike | null>(null);
  currentClient.current = client ?? null;
  const [marketplaceWarning, setMarketplaceWarning] = useState<{
    client: ConversationClientLike;
    text: string;
  } | null>(null);
  const [appliedRemovalGuard, setAppliedRemovalGuard] =
    useState<AppliedRemovalGuard>(() => ({
      client: null,
      entries: new Map(),
    }));
  const appliedRemovalNames =
    appliedRemovalGuard.client === client
      ? new Set(appliedRemovalGuard.entries.keys())
      : EMPTY_APPLIED_REMOVALS;
  const visibleMarketplaceWarning =
    marketplaceWarning?.client === client ? marketplaceWarning.text : null;
  useEffect(() => {
    setAppliedRemovalGuard((current) =>
      current.client === (client ?? null)
        ? current
        : { client: client ?? null, entries: new Map() },
    );
    setMarketplaceWarning((current) =>
      current?.client === client ? current : null,
    );
  }, [client]);
  // A guard name the hub's own list no longer carries is fully reconciled -
  // its row is gone with it - and one it carries under a NEW registration
  // identity - a fresh stamp, or a different source the wire's whole-second
  // stamp cannot tell apart on its own - is a re-add, a write someone made
  // after the removal this fence guards. The guard forgets both, so neither
  // a reconciled name nor a re-added one is fenced forever.
  const reconcileAppliedRemovals = useCallback(
    (
      marketplaces: readonly MarketplaceEntry[],
      owner: ConversationClientLike,
    ): void => {
      if (currentClient.current !== owner) return;
      const currentEntries = new Map(
        marketplaces.map((item) => [item.name, item] as const),
      );
      setAppliedRemovalGuard((current) => {
        if (current.client !== owner) return current;
        let changed = false;
        const next = new Map(current.entries);
        for (const [name, removed] of current.entries) {
          const seen = currentEntries.get(name);
          if (seen && sameRemovedRegistration(seen, removed)) continue;
          next.delete(name);
          changed = true;
        }
        return changed ? { client: owner, entries: next } : current;
      });
    },
    [],
  );
  // The browser's recording path for an applied removal: fences the name
  // whatever the hub's truth currently carries, until an authoritative read
  // establishes it - a list omitting the name (the removal reconciled) or
  // carrying a newer registration (a re-add) clears the fence there, and
  // only a stale row still carrying the removed registration's own identity
  // keeps it - sets the warning to the outcome's own notice, a clean one
  // clearing whatever earlier outcome raised (the warning reports the
  // latest applied removal, never a residue an obsolete one left), and
  // answers whether `owner` was still current - false means the outcome
  // came from a client this screen has replaced.
  const markAppliedRemoval = useCallback(
    (
      name: string,
      notice: string | null,
      owner: ConversationClientLike,
      removed: MarketplaceEntry,
    ): boolean => {
      if (currentClient.current !== owner) return false;
      setAppliedRemovalGuard((current) => {
        if (current.client !== owner) return current;
        const entries = new Map(current.entries);
        entries.set(name, {
          source: removed.source,
          lastUpdated: removed.lastUpdated,
        });
        return { client: owner, entries };
      });
      setMarketplaceWarning(
        notice !== null ? { client: owner, text: notice } : null,
      );
      return true;
    },
    [],
  );
  // A name this screen's own add just registered: the write replaced the
  // registration the fence guards - which the wire's whole-second
  // timestamps can fail to distinguish - so the fence clears for it here.
  const clearAddedMarketplace = useCallback(
    (name: string, owner: ConversationClientLike): void => {
      if (currentClient.current !== owner) return;
      setAppliedRemovalGuard((current) => {
        if (current.client !== owner || !current.entries.has(name)) return current;
        const entries = new Map(current.entries);
        entries.delete(name);
        return { client: owner, entries };
      });
    },
    [],
  );
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
        appliedRemovalNames={appliedRemovalNames}
        marketplaceWarning={visibleMarketplaceWarning}
        onAppliedRemoval={markAppliedRemoval}
        onAuthoritativeMarketplaces={reconcileAppliedRemovals}
        onMarketplaceAdded={clearAddedMarketplace}
      />
    </>
  );
}

function Plugins({
  client,
  connectionState,
  hubName,
  gate,
  appliedRemovalNames,
  marketplaceWarning,
  onAppliedRemoval,
  onAuthoritativeMarketplaces,
  onMarketplaceAdded,
  canUseConnection,
}: {
  client: ConversationClientLike;
  connectionState: ConnectionState;
  hubName: string;
  gate: PluginMutationGate;
  appliedRemovalNames: ReadonlySet<string>;
  marketplaceWarning: string | null;
  onAppliedRemoval(
    name: string,
    notice: string | null,
    owner: ConversationClientLike,
    removed: MarketplaceEntry,
  ): boolean;
  onAuthoritativeMarketplaces(
    marketplaces: readonly MarketplaceEntry[],
    owner: ConversationClientLike,
  ): void;
  onMarketplaceAdded(name: string, owner: ConversationClientLike): void;
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
  // The store's own reconnect recovery is what a banner over a live screen
  // needs: the hub broadcasts a change only to clients connected when it
  // happens, so everything that moved while this one was away arrives as
  // nothing at all, and the store re-reads a list something wants once
  // connectionChanged says the connection is ready again (storeLifecycle.ts).
  // The wall this screen used to show remounted the store on every recovery,
  // so that read happened for free with the remount; keeping the screen
  // mounted behind a banner removes it, and this drives the store through
  // every transition itself, the way useCredentialStore drives the credential
  // store (credentialStore.ts). A layout effect, so the store knows its
  // connection before the mount effect's first read issues.
  useLayoutEffect(() => {
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
      <ErrorMessage message={marketplaceWarning} />
      {panel === "browse" ? (
        <MarketplaceBrowser
          client={client}
          connectionState={connectionState}
          hubName={hubName}
          installed={model}
          gate={gate}
          ready={ready}
          canUseConnection={canUseConnection}
          onOpenPlugin={(target) => {
            close();
            setSelected(target);
          }}
          appliedRemovalNames={appliedRemovalNames}
          onAppliedRemoval={onAppliedRemoval}
          onAuthoritativeMarketplaces={onAuthoritativeMarketplaces}
          onMarketplaceAdded={onMarketplaceAdded}
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
            {/* The native modal covers the banner the screen shows behind
             * it, so the status and the manual reconnect live here while
             * this detail is open. */}
            {connectionState !== "ready" ? <ConnectionStatus /> : null}
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
