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
  MarketplaceEntry,
  PluginRefParams,
} from "@evener/appwire-client";
import {
  createMarketplacesStore,
  type PluginsStore,
} from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
  PLUGIN_MUTATION_BUSY,
  runGatedMutation,
  type PluginMutationGate,
} from "./pluginMutationGate";
import { HubPathField } from "./HubPathField";
import {
  appliedRemovalNotice,
  catalogToBrowse,
  refetchAfterRemoval,
} from "./marketplaceBrowserModel";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

// The stores keep each failed request's own text; this screen shows the same
// copy for every failure, as the web's section translates its at render.
const MARKETPLACES_FAILED =
  "Could not load marketplaces. Try again when connected.";
const CATALOG_FAILED = "Could not load this catalog. Try again when connected.";
export const INSTALLED_PLUGINS_FAILED =
  "Could not load installed plugins. Try again when connected.";
const WRITE_FAILED =
  "Could not confirm the change. Check its status before trying again.";

export function MarketplaceBrowser({
  client,
  hubName,
  installed,
  gate,
  onOpenPlugin,
  appliedRemovalNames,
  onAppliedRemoval,
  onAuthoritativeMarketplaces,
  onMarketplaceAdded,
}: {
  client: ConversationClientLike;
  hubName: string;
  installed: PluginsStore;
  // The screen's plugin-mutation gate, shared with the installed list: a
  // write started here keeps running after this view is gone, so the lock
  // it takes has to outlive the view - and living at the screen means the
  // installed list sees a marketplace write as busy too, and vice versa.
  gate: PluginMutationGate;
  onOpenPlugin(target: PluginRefParams): void;
  /** The applied removals this client already recorded: a name in it is one
   * the hub says is already gone, so the write that would remove it again
   * stays fenced. */
  appliedRemovalNames: ReadonlySet<string>;
  /** Records an applied removal with the screen. `notice` is
   * appliedRemovalNotice's answer - the cleanup warning, or null when only
   * the list read failed - and becomes the screen-level warning; the name
   * joins the guard whatever the hub's truth currently carries: the fence
   * holds until an authoritative read establishes that truth - absence or
   * a replacement registration clears it, and only a stale row still
   * carrying `asOf` - the registration the write removed - keeps it; the
   * return value says whether `owner` was still the current client, false
   * meaning a replaced client's late result is dropped whole. */
  onAppliedRemoval(
    name: string,
    notice: string | null,
    owner: ConversationClientLike,
    asOf: MarketplaceEntry["lastUpdated"],
  ): boolean;
  /** Reports every authoritative list read, so the screen can prune guard
   * names the hub no longer carries. */
  onAuthoritativeMarketplaces(
    marketplaces: readonly MarketplaceEntry[],
    owner: ConversationClientLike,
  ): void;
  /** Reports a name this browser's own successful add just registered: the
   * write replaced whatever registration the screen had fenced, so the
   * fence clears for it. A blank name (the hub assigns one) is not reported
   * and reconciles through the authoritative lists instead. */
  onMarketplaceAdded(name: string, owner: ConversationClientLike): void;
}) {
  const colors = useColors();
  const model = useMemo(() => createMarketplacesStore(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getState);
  const plugins = useSyncExternalStore(installed.subscribe, installed.getState);
  // Marketplace writes take the same gate an install does; see
  // pluginMutationGate.ts for why the gate exists.
  const busy = useSyncExternalStore(gate.subscribe, gate.isBusy);
  const [selected, setSelected] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const revision = useRef(0);
  // Lowered by this browser's own unmount: a write it started must not then
  // refetch through its store, which died with it and drops everything that
  // read publishes - the mounted browser's own read is the reconciliation.
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  useEffect(() => {
    if (state.marketplaces !== null)
      onAuthoritativeMarketplaces(state.marketplaces, client);
  }, [client, onAuthoritativeMarketplaces, state.marketplaces]);
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
  // Every write goes through the gate; a refusal reads as busy, a throw as
  // failure. remove() runs its own copy of this shape below, because an
  // applied removal must reach the parent's guard and warning rather than
  // this view's error slot.
  async function act(action: () => Promise<void>) {
    const version = revision.current;
    setError(null);
    const outcome = await runGatedMutation(gate, action);
    if (revision.current !== version) return;
    if (outcome === "refused") setError(PLUGIN_MUTATION_BUSY);
    else if (outcome === "failed") setError(WRITE_FAILED);
  }
  function install(target: PluginRefParams) {
    void act(() => plugins.installPlugin(target.plugin, target.marketplace));
  }
  const marketplace = state.marketplaces?.find(
    (item) => item.name === selected,
  );
  function refresh() {
    if (!marketplace || busy) return;
    void act(() => state.refreshMarketplace(marketplace.name));
  }
  function remove() {
    if (!marketplace || busy || appliedRemovalNames.has(marketplace.name)) return;
    const name = marketplace.name;
    // The registration this write targets: the outcome fences the name until
    // an authoritative read establishes what the hub now carries - absence
    // or a replacement registration clears the fence, and only a stale row
    // still carrying this registration's own identity keeps it.
    const target = marketplace.lastUpdated;
    const version = revision.current;
    Alert.alert("Remove marketplace?", `${name} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Remove",
        style: "destructive",
        onPress: () => {
          if (revision.current !== version || appliedRemovalNames.has(name)) return;
          // Classify, record, and reconcile BEFORE the revision fence: the
          // guard and warning live at the screen, so they must survive this
          // view's selection changes and remounts; only the browser-local
          // busy and write-failed displays stay behind the fence.
          void (async () => {
            setError(null);
            let caught: unknown;
            const outcome = await runGatedMutation(gate, () =>
              state.removeMarketplace(name).catch((error: unknown) => {
                caught = error;
                throw error;
              }),
            );
            if (outcome === "refused") {
              if (revision.current !== version) return;
              setError(PLUGIN_MUTATION_BUSY);
              return;
            }
            if (outcome !== "failed") return;
            // An applied removal (appliedRemovalNotice's doc) never reads as
            // a failed write: record it with the parent's guard - the notice
            // becomes the screen-level warning, null shows nothing - and
            // reconcile a stale list, never a retry hint.
            const notice = appliedRemovalNotice(caught);
            if (notice !== undefined) {
              if (!onAppliedRemoval(name, notice, client, target)) return;
              if (alive.current && refetchAfterRemoval(model, name))
                void state.fetchMarketplaces();
              return;
            }
            if (revision.current !== version) return;
            setError(WRITE_FAILED);
          })();
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
            <Action
              disabled={busy || appliedRemovalNames.has(marketplace?.name ?? "")}
              onPress={remove}
            >
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
          gate={gate}
          onClose={() => setAdding(false)}
          onAdd={async (params) => {
            await state.addMarketplace(params);
            if (params.name) onMarketplaceAdded(params.name, client);
          }}
        />
      )}
    </>
  );
}

function AddMarketplace({
  client,
  hubName,
  gate,
  onClose,
  onAdd,
}: {
  hubName: string;
  gate: PluginMutationGate;
  onClose(): void;
  /** The write itself; the gate around it lives here, so a refusal keeps the
   * modal open on the busy copy just as it does everywhere else. */
  onAdd(params: MarketplaceAddParams): Promise<void>;
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
    const outcome = await runGatedMutation(gate, () =>
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
    if (!alive.current) return;
    setBusy(false);
    if (outcome === "refused") {
      setError(PLUGIN_MUTATION_BUSY);
      return;
    }
    if (outcome === "failed")
      setError(
        "Could not confirm the marketplace was added. Check the list and source before trying again.",
      );
    else onClose();
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
