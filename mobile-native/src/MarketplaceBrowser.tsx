import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  Alert,
  FlatList,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { marketplaceSourceLabel } from "@evener/appwire-client";
import type {
  MarketplaceAddParams,
  PluginRefParams,
} from "@evener/appwire-client";
import {
  createMarketplacesStore,
  type PluginsStore,
} from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { PLUGIN_MUTATION_BUSY, type PluginMutationGate } from "./pluginMutationGate";
import { HubPathField } from "./HubPathField";
import { catalogToBrowse, refusesMarketplaceWrite } from "./marketplaceBrowserModel";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

// The stores keep each failed request's own text; this screen shows the same
// copy for every failure, as the web's section translates its at render.
const MARKETPLACES_FAILED =
  "Could not load marketplaces. Try again when connected.";
const CATALOG_FAILED = "Could not load this catalog. Try again when connected.";
export const INSTALLED_PLUGINS_FAILED =
  "Could not load installed plugins. Try again when connected.";
const WRITE_FAILED =
  "Could not confirm the change. Refresh and check its status before trying again.";

export function MarketplaceBrowser({
  client,
  hubName,
  installed,
  gate,
  onOpenPlugin,
}: {
  client: ConversationClientLike;
  hubName: string;
  installed: PluginsStore;
  // The screen's plugin-mutation gate, shared with the installed list: an
  // install started here keeps running after this view is gone, so the lock
  // it takes has to outlive the view.
  gate: PluginMutationGate;
  onOpenPlugin(target: PluginRefParams): void;
}) {
  const colors = useColors();
  const model = useMemo(() => createMarketplacesStore(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getState);
  const plugins = useSyncExternalStore(installed.subscribe, installed.getState);
  const pluginBusy = useSyncExternalStore(gate.subscribe, gate.isBusy);
  const [selected, setSelected] = useState<string | null>(null);
  // Marketplace writes (add, remove, refresh) are this view's own and end with
  // it; the plugin install is the one that outlives it, so only that one takes
  // the screen's gate. They still wait on pluginBusy (see the three actions
  // below) without taking the gate themselves: the hub DOES serialize a
  // marketplace removal or refresh against a concurrent install under the
  // same store lock (internal/plugins/locks.go's lockStore - install.go's
  // Install/Upgrade/mutateEntry and marketplaces.go's RemoveMarketplace/
  // RefreshMarketplace all acquire it), so the write itself is safe either
  // way. What the lock does not do is refuse: a removal issued mid-install
  // blocks at the hub until the install's lock releases, which the request's
  // own 30s timeout (client.ts's DEFAULT_REQUEST_TIMEOUT_MS) matches almost
  // exactly - so without this gate the button would sit looking hung, or
  // occasionally time out outright, instead of refusing at once with copy
  // that says why.
  const [mutating, setMutating] = useState(false);
  // Mirrors the mutating state above for dispatchMutation's own live check:
  // a ref, because that check runs at the moment a write actually dispatches
  // - after a confirmation Alert or inside the add-marketplace modal - which
  // can land long after the render that set mutating, on a stale closure
  // that never sees a later render's value.
  const mutatingRef = useRef(false);
  // Any of the three marketplace actions plus the installed-plugin gate: what
  // every write-disabling site below shares.
  const busy = mutating || pluginBusy;
  const [adding, setAdding] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const revision = useRef(0);
  useEffect(() => {
    model.start();
    void model.getState().fetchMarketplaces();
    return () => {
      revision.current += 1;
      model.dispose();
    };
  }, [model]);
  // A new selection starts clean: the filter and the last action's error
  // belong to the marketplace they were typed against.
  function select(name: string | null) {
    revision.current += 1;
    setSelected(name);
    setError(null);
    setQuery("");
  }
  // The selected catalog is read from the store's cache. A mutation here or a
  // change from another client retires the entry, and an empty slot is this
  // view's cue to request it again - the web's expanded node does the same.
  const catalog = selected ? state.browseCatalogs.get(selected) : undefined;
  const loaded = catalog?.status === "loaded" ? catalog : undefined;
  const browseTarget = catalogToBrowse(
    selected,
    state.marketplaces,
    state.browseCatalogs,
  );
  useEffect(() => {
    if (browseTarget) void state.browseMarketplace(browseTarget);
  }, [browseTarget, state.browseMarketplace]);
  // A marketplace removed here or by another client leaves the list; its
  // selection goes with it.
  useEffect(() => {
    if (
      selected &&
      state.marketplaces &&
      !state.marketplaces.some((item) => item.name === selected)
    )
      setSelected(null);
  }, [selected, state.marketplaces]);
  // The one path every marketplace write (refresh, remove, add) dispatches
  // through, mirroring how gate.run() is installs' own single live-checked
  // guard: refuses, live at dispatch time, if this view's own write or the
  // screen's plugin-install gate is already running - a fresh read, not a
  // render-time snapshot, because the gap between a button press and the
  // actual dispatch (a confirmation Alert, an open modal) can outlive the
  // render that last checked busy. Resolves true if it ran.
  async function dispatchMutation(run: () => Promise<void>): Promise<boolean> {
    if (refusesMarketplaceWrite(mutatingRef.current, gate.isBusy())) return false;
    mutatingRef.current = true;
    setMutating(true);
    try {
      await run();
      return true;
    } finally {
      mutatingRef.current = false;
      setMutating(false);
    }
  }
  // A marketplace write is this view's own: it marks this view busy and ends
  // with it, and surfaces a refusal the same way install() below does.
  async function act(action: () => Promise<void>) {
    const version = revision.current;
    setError(null);
    try {
      const ran = await dispatchMutation(action);
      if (!ran && revision.current === version) setError(PLUGIN_MUTATION_BUSY);
    } catch {
      if (revision.current === version) setError(WRITE_FAILED);
    }
  }
  // An install is the write that outlives this view, so it takes the screen's
  // gate rather than the flag above - the installed list disables on the same
  // gate, and a tab switch cannot start a second one.
  function install(target: PluginRefParams) {
    const version = revision.current;
    setError(null);
    void gate
      .run(() => plugins.installPlugin(target.plugin, target.marketplace))
      .then((ran) => {
        if (!ran && revision.current === version) setError(PLUGIN_MUTATION_BUSY);
      })
      .catch(() => {
        if (revision.current === version) setError(WRITE_FAILED);
      });
  }
  const marketplace = state.marketplaces?.find(
    (item) => item.name === selected,
  );
  function refresh() {
    if (!marketplace) return;
    void act(() => state.refreshMarketplace(marketplace.name));
  }
  function remove() {
    if (!marketplace) return;
    const name = marketplace.name;
    const version = revision.current;
    Alert.alert("Remove marketplace?", `${name} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Remove",
        style: "destructive",
        onPress: () => {
          if (revision.current !== version) return;
          void act(() => state.removeMarketplace(name));
        },
      },
    ]);
  }
  const needle = query.trim().toLowerCase();
  const catalogPlugins = (loaded?.plugins ?? []).filter((item) =>
    `${item.name} ${item.description ?? ""}`.toLowerCase().includes(needle),
  );
  const listError = state.marketplacesError === null ? null : MARKETPLACES_FAILED;
  const catalogError = catalog?.status === "error" ? CATALOG_FAILED : null;
  const installedError =
    plugins.pluginsError === null ? null : INSTALLED_PLUGINS_FAILED;
  const browsing = catalog?.status === "loading";
  const header = (
    <View style={{ gap: 8, paddingBottom: 12 }}>
      <Copy muted>{hubName}</Copy>
      <ErrorMessage message={error || listError} />
      {listError && (
        <Action
          onPress={() => {
            void state.fetchMarketplaces();
          }}
        >
          Retry marketplaces
        </Action>
      )}
      {selected ? (
        <>
          <Action onPress={() => select(null)}>All marketplaces</Action>
          <Copy>{selected}</Copy>
          {marketplace && <Copy muted>{marketplaceSourceLabel(marketplace.source)}</Copy>}
          {loaded?.description && <Copy>{loaded.description}</Copy>}
          <View style={[styles.row, { flexWrap: "wrap" }]}>
            <Action disabled={busy} onPress={refresh}>
              Refresh source
            </Action>
            <Action disabled={busy} onPress={remove}>
              Remove marketplace
            </Action>
          </View>
          <TextInput
            accessibilityLabel="Filter marketplace plugins"
            placeholder="Filter this catalog"
            placeholderTextColor={colors.secondary}
            value={query}
            onChangeText={setQuery}
            autoCapitalize="none"
            autoCorrect={false}
            style={[
              styles.input,
              { color: colors.text, borderColor: colors.border },
            ]}
          />
          <ErrorMessage message={catalogError || installedError} />
          {catalogError && (
            <Action
              onPress={() => {
                void state.reloadCatalog(selected);
              }}
            >
              Retry catalog
            </Action>
          )}
          {installedError && (
            <Action
              onPress={() => {
                void plugins.fetchPlugins();
              }}
            >
              Retry installed status
            </Action>
          )}
        </>
      ) : (
        <Action disabled={busy} onPress={() => setAdding(true)}>
          Add marketplace
        </Action>
      )}
      {busy && (
        <ActivityIndicator accessibilityLabel="Updating marketplace or plugin" />
      )}
    </View>
  );
  return (
    <>
      {selected ? (
        <FlatList
          data={catalogPlugins}
          keyExtractor={(item) => item.name}
          contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
          keyboardShouldPersistTaps="handled"
          ListHeaderComponent={header}
          refreshing={browsing}
          onRefresh={() => {
            void state.reloadCatalog(selected);
          }}
          ListEmptyComponent={
            browsing ? (
              <ActivityIndicator accessibilityLabel="Loading marketplace catalog" />
            ) : loaded ? (
              <Copy muted>
                {needle
                  ? "No matching plugins."
                  : "No plugins in this catalog."}
              </Copy>
            ) : null
          }
          renderItem={({ item }) => {
            const target = { plugin: item.name, marketplace: selected };
            const existing = plugins.plugins?.some(
              (value) =>
                value.plugin === target.plugin &&
                value.marketplace === target.marketplace,
            );
            return (
              <View
                style={{
                  paddingVertical: 10,
                  borderBottomWidth: 0.5,
                  borderColor: colors.border,
                  gap: 4,
                }}
              >
                <Copy>{item.name}</Copy>
                {item.description && <Copy muted>{item.description}</Copy>}
                {item.author && <Copy muted>{item.author}</Copy>}
                <Action
                  disabled={busy || !plugins.plugins || !!installedError}
                  label={`${existing ? "Open" : "Install"} ${item.name} from ${target.marketplace}`}
                  onPress={() => {
                    if (existing) onOpenPlugin(target);
                    else install(target);
                  }}
                >
                  {existing ? "Installed · Open" : "Install"}
                </Action>
              </View>
            );
          }}
        />
      ) : (
        <FlatList
          data={state.marketplaces ?? []}
          keyExtractor={(item) => item.name}
          contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
          ListHeaderComponent={header}
          refreshing={state.marketplacesLoading}
          onRefresh={() => {
            void state.fetchMarketplaces();
          }}
          ListEmptyComponent={
            state.marketplacesLoading ? (
              <ActivityIndicator accessibilityLabel="Loading marketplaces" />
            ) : state.marketplaces ? (
              <Copy muted>No marketplaces on this hub.</Copy>
            ) : null
          }
          renderItem={({ item }) => (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Browse ${item.name}`}
              onPress={() => select(item.name)}
              style={({ pressed }) => ({
                minHeight: 56,
                paddingVertical: 9,
                borderBottomWidth: 0.5,
                borderColor: colors.border,
                opacity: pressed ? 0.65 : 1,
              })}
            >
              <Copy>{item.name}</Copy>
              <Copy muted numberOfLines={2}>
                {marketplaceSourceLabel(item.source)}
              </Copy>
            </Pressable>
          )}
        />
      )}
      {adding && (
        <AddMarketplace
          client={client}
          hubName={hubName}
          onClose={() => setAdding(false)}
          onAdd={state.addMarketplace}
          dispatch={dispatchMutation}
        />
      )}
    </>
  );
}

function AddMarketplace({
  client,
  hubName,
  onClose,
  onAdd,
  dispatch,
}: {
  hubName: string;
  onClose(): void;
  onAdd(params: MarketplaceAddParams): Promise<void>;
  // The same live-checked guard the parent's own refresh/remove dispatch
  // through: an install that started while this modal was open (it takes no
  // time of its own to open, unlike the remove confirm's Alert, but can stay
  // open for as long as the user takes to fill in the form) must still
  // refuse the add rather than run it beside the install.
  dispatch(run: () => Promise<void>): Promise<boolean>;
  client: ConversationClientLike;
}) {
  const colors = useColors();
  const [kind, setKind] = useState<"url" | "github" | "directory">("url");
  const [source, setSource] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  async function submit() {
    if (busy || !source.trim()) return;
    setBusy(true);
    setError(null);
    const value = source.trim();
    try {
      const ran = await dispatch(() =>
        onAdd({
          name: name.trim(),
          source:
            kind === "github"
              ? { kind, repo: value }
              : kind === "directory"
                ? { kind, path: value }
                : { kind, url: value },
        }),
      );
      if (!ran) {
        if (alive.current) setError(PLUGIN_MUTATION_BUSY);
        return;
      }
      if (alive.current) onClose();
    } catch {
      if (alive.current)
        setError(
          "Could not confirm the marketplace was added. Check the list and source before trying again.",
        );
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <Modal
      visible
      animationType="slide"
      presentationStyle="pageSheet"
      onRequestClose={onClose}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View style={[styles.row, { paddingHorizontal: 16 }]}>
          <View style={styles.fill}>
            <Copy muted>{hubName}</Copy>
          </View>
          <Action onPress={onClose}>Cancel</Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          enabled={Platform.OS === "android"}
          behavior="height"
        >
          <ScrollView
            automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 20, gap: 12 }}
          >
            <Copy>Add marketplace</Copy>
            <View style={[styles.row, { flexWrap: "wrap" }]}>
              {(["url", "github", "directory"] as const).map((value) => (
                <Choice
                  key={value}
                  label={
                    value === "url"
                      ? "Git URL"
                      : value === "github"
                        ? "GitHub repository"
                        : "Hub directory"
                  }
                  selected={kind === value}
                  disabled={busy}
                  onPress={() => {
                    if (value === kind) return;
                    setKind(value);
                    setSource("");
                  }}
                />
              ))}
            </View>
            <Copy>
              {kind === "url"
                ? "Git URL"
                : kind === "github"
                  ? "owner/repo"
                  : `Directory on ${hubName}`}
            </Copy>
            {kind === "directory" ? (
              <HubPathField
                kind="dir"
                client={client}
                label="Marketplace source"
                value={source}
                onChange={setSource}
                disabled={busy}
              />
            ) : (
              <TextInput
                accessibilityLabel="Marketplace source"
                value={source}
                onChangeText={setSource}
                editable={!busy}
                autoCapitalize="none"
                autoCorrect={false}
                style={[
                  styles.input,
                  { color: colors.text, borderColor: colors.border },
                ]}
              />
            )}
            {kind === "directory" && (
              <Copy muted>This path is on the hub, not this phone.</Copy>
            )}
            <Copy>Name (optional)</Copy>
            <TextInput
              accessibilityLabel="Marketplace name"
              value={name}
              onChangeText={setName}
              editable={!busy}
              autoCapitalize="none"
              autoCorrect={false}
              style={[
                styles.input,
                { color: colors.text, borderColor: colors.border },
              ]}
            />
            <ErrorMessage message={error} />
            {busy && (
              <ActivityIndicator accessibilityLabel="Adding marketplace" />
            )}
            <Action
              disabled={busy || !source.trim()}
              onPress={() => {
                void submit();
              }}
            >
              Add marketplace
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
