import { useEffect, useRef, useState, useSyncExternalStore } from "react";
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
  ConnectionState,
  MarketplaceAddParams,
  MarketplaceEntry,
  PluginRefParams,
} from "@evener/appwire-client";
import {
  type MarketplacesStore,
  type PluginsStore,
} from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { whenReady, type LiveReadiness } from "./connectionDisplay";
import {
  PLUGIN_MUTATION_BUSY,
  runGatedMutation,
  type PluginMutationGate,
} from "./pluginMutationGate";
import { HubPathField } from "./HubPathField";
import {
  appliedRemovalNotice,
  addedMarketplaceNames,
  catalogToBrowse,
  refetchAfterRemoval,
} from "./marketplaceBrowserModel";
import { ModalConnectionStatus } from "./retainedScreen";
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
  connectionState,
  hubName,
  installed,
  marketplaces,
  lastAddMarketplaces,
  gate,
  ready,
  canUseConnection,
  onOpenPlugin,
  appliedRemovalNames,
  onAppliedRemoval,
  onAuthoritativeMarketplaces,
  onMarketplaceAdded,
  onRemovedMarketplace,
}: {
  client: ConversationClientLike;
  connectionState: ConnectionState;
  hubName: string;
  installed: PluginsStore;
  /** The screen's own marketplaces store, which outlives this view: a write
   * that outlives a tab switch keeps publishing into it, and the remount
   * that follows reads what it retained. The screen drives the store's
   * connection transitions and its start/dispose lifetime, the way it
   * drives the plugins store it hands down beside it. */
  marketplaces: MarketplacesStore;
  /** The screen's capture of the latest marketplace-add answer, read off the
   * client the store itself is built on: the add flow below names blank
   * registrations from it because a newer list read can hold the store's
   * publication of the add. */
  lastAddMarketplaces: { current: readonly MarketplaceEntry[] | null };
  // The screen's plugin-mutation gate, shared with the installed list: a
  // write started here keeps running after this view is gone, so the lock
  // it takes has to outlive the view - and living at the screen means the
  // installed list sees a marketplace write as busy too, and vice versa.
  gate: PluginMutationGate;
  ready: boolean;
  canUseConnection: LiveReadiness;
  onOpenPlugin(target: PluginRefParams): void;
  /** The applied removals this client already recorded: a name in it is one
   * the hub says is already gone, so the write that would remove it again
   * stays fenced. */
  appliedRemovalNames: ReadonlySet<string>;
  /** Records an applied removal with the screen. `notice` is
   * appliedRemovalNotice's answer - the cleanup warning, or null when only
   * the list read failed - and becomes the screen-level warning; the
   * `marketplaces` snapshot and its `publicationVersion`, read off the store
   * as the outcome landed, decide the fence: an accepted snapshot that
   * already omits the target is the outcome's own reconciliation and leaves
   * no fence, while a snapshot still carrying the target - or no list at
   * all - fences the name against that version as its baseline, for the
   * window between the outcome and the first authoritative read after it;
   * the return value says whether the outcome was accepted, false meaning a
   * replaced client's late result is dropped whole. */
  onAppliedRemoval(
    name: string,
    notice: string | null,
    owner: ConversationClientLike,
    marketplaces: readonly MarketplaceEntry[] | null,
    publicationVersion: number,
  ): boolean;
  /** Reports every authoritative list read with the publication version it
   * landed at, so the screen can prune guard names whose own baselines the
   * read outruns - per name, so a read one later outcome's recording would
   * have swallowed under a browser-wide watermark still reaches every
   * earlier fence. The store's own revision fence has already vouched for
   * the read; its contents never decide anything (the fallback ruling: a
   * row a trusted read vouches for, once the removal stood, can only be a
   * re-registration). */
  onAuthoritativeMarketplaces(
    marketplaces: readonly MarketplaceEntry[],
    owner: ConversationClientLike,
    publicationVersion: number,
  ): void;
  /** Reports every name this browser's own successful add just registered:
   * the write replaced whatever registration the screen had fenced, so the
   * fence clears for it. A submitted name is reported as-is; a blank one is
   * one the hub assigned, so the browser names it off the hub's own list -
   * read directly through the client, never off the store, whose
   * publication of the add a newer read can hold. Naming is
   * publication-only: a blank
   * re-registration no list can tell from the stale row it replaced - the
   * same name, source, and whole-second stamp - is not named here at all.
   * Its fence still retires under the fallback ruling
   * (onAuthoritativeMarketplaces): the first whole-list publication after
   * the outcome - the add's own answer when it publishes, otherwise the
   * read that holds or follows it - retires the fence whatever it
   * carries. */
  onMarketplaceAdded(name: string, owner: ConversationClientLike): void;
  /** Reports a marketplace removal the hub confirmed outright - the write
   * resolved, so the outcome is neither an applied residue nor a failure.
   * The screen owns the cleanup warning, and the latest removal outcome
   * decides what it shows: a clean success retires whatever earlier outcome
   * raised, the same way an applied outcome with nothing to say does
   * (onAppliedRemoval's null notice). */
  onRemovedMarketplace(name: string, owner: ConversationClientLike): void;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(marketplaces.subscribe, marketplaces.getState);
  const plugins = useSyncExternalStore(installed.subscribe, installed.getState);
  // Marketplace writes take the same gate an install does; see
  // pluginMutationGate.ts for why the gate exists.
  const busy = useSyncExternalStore(gate.subscribe, gate.isBusy);
  const [selected, setSelected] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const revision = useRef(0);
  // The removal confirmation is native and outlives the renders around it:
  // the screen's guard can fence or retire a name while its dialog is open,
  // and the callback the confirm fires holds only the set it captured when
  // the dialog opened. The ref keeps the latest set reachable from that
  // callback - assigned in an effect, never during render (the PluginsScreen
  // currentClient pattern).
  const appliedRemovalNamesRef = useRef(appliedRemovalNames);
  useEffect(() => {
    appliedRemovalNamesRef.current = appliedRemovalNames;
  }, [appliedRemovalNames]);
  useEffect(() => {
    if (state.marketplaces !== null)
      onAuthoritativeMarketplaces(
        state.marketplaces,
        client,
        state.marketplacesPublicationVersion,
      );
  }, [
    client,
    onAuthoritativeMarketplaces,
    state.marketplaces,
    state.marketplacesPublicationVersion,
  ]);
  useEffect(() => {
    void marketplaces.getState().fetchMarketplaces();
    return () => {
      revision.current += 1;
    };
  }, [marketplaces]);
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
  // failure. A not-ready press runs nothing and retires nothing - the copy
  // the user was reading survives the no-op. remove() runs its own copy of
  // this shape below, because an applied removal must reach the parent's
  // guard and warning rather than this view's error slot.
  async function act(action: () => Promise<void>) {
    const version = revision.current;
    const outcome = await runGatedMutation(gate, canUseConnection, action);
    if (revision.current !== version) return;
    if (outcome === "not-ready") return;
    setError(null);
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
    if (!marketplace || busy || !canUseConnection()) return;
    void act(() => state.refreshMarketplace(marketplace.name));
  }
  function remove() {
    if (
      !marketplace ||
      busy ||
      !canUseConnection() ||
      appliedRemovalNames.has(marketplace.name)
    )
      return;
    const name = marketplace.name;
    const version = revision.current;
    Alert.alert("Remove marketplace?", `${name} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Remove",
        style: "destructive",
        onPress: () => {
          // The dialog can stay open across another client's removal, which
          // a trusted read lands without the name, and across the screen
          // fencing it: re-read both the guard the screen holds now and the
          // store's own state, never the captured ones, because a removal
          // that already stood must not be issued again. A failed read
          // cannot vouch either way, so its retained rows never stop the
          // write - the hub's own applied answer is what speaks then.
          const current = marketplaces.getState();
          if (
            revision.current !== version ||
            !canUseConnection() ||
            appliedRemovalNamesRef.current.has(name) ||
            (current.marketplacesError === null &&
              current.marketplaces !== null &&
              !current.marketplaces.some((item) => item.name === name))
          )
            return;
          // Classify, record, and reconcile BEFORE the revision fence: the
          // guard and warning live at the screen, so they must survive this
          // view's selection changes and remounts; only the browser-local
          // busy and write-failed displays stay behind the fence.
          void (async () => {
            let caught: unknown;
            const outcome = await runGatedMutation(gate, canUseConnection, () =>
              state.removeMarketplace(name).catch((error: unknown) => {
                caught = error;
                throw error;
              }),
            );
            if (outcome === "not-ready") {
              // Nothing ran, so nothing reports: a not-ready removal is
              // not one the hub confirmed, and reporting it would retire a
              // cleanup warning that still stands. The status copy the
              // connection banner already shows covers the reason nothing
              // ran, and the write-failed copy an earlier outcome left
              // stays too - a press that ran nothing retires nothing.
              return;
            }
            setError(null);
            if (outcome === "refused") {
              if (revision.current !== version) return;
              setError(PLUGIN_MUTATION_BUSY);
              return;
            }
            if (outcome === "ran") {
              // Report before the revision fence, the way an applied outcome
              // records: the warning is the screen's, so it must survive this
              // view's selection changes and remounts.
              onRemovedMarketplace(name, client);
              return;
            }
            // An applied removal (appliedRemovalNotice's doc) never reads as
            // a failed write: record it with the parent's guard - the notice
            // becomes the screen-level warning, null shows nothing - and
            // reconcile a stale list, never a retry hint.
            const notice = appliedRemovalNotice(caught);
            if (notice !== undefined) {
              // The store as the outcome landed - the screen's own, which
              // survives this view: the snapshot decides whether a fence is
              // needed at all, and its publication version is the baseline
              // the screen records against that name. Everything the store
              // published at or below it predates this fence - including a
              // stale read's answer the rejection just passed ownership to -
              // so only a later publication retires it (the prop's doc).
              // The reconciliation read still publishes somewhere a mounted
              // browser reads: this view's own unmount must not stop it -
              // the remount whose first read fails is exactly the case the
              // retained model exists for.
              const current = marketplaces.getState();
              if (
                !onAppliedRemoval(
                  name,
                  notice,
                  client,
                  current.marketplaces,
                  current.marketplacesPublicationVersion,
                )
              )
                return;
              if (refetchAfterRemoval(marketplaces, name))
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
          disabled={!ready}
          onPress={whenReady(canUseConnection, () => {
            void state.fetchMarketplaces();
          })}
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
            <Action disabled={busy || !ready} onPress={refresh}>
              Refresh source
            </Action>
            <Action
              disabled={
                busy ||
                !ready ||
                appliedRemovalNames.has(marketplace?.name ?? "")
              }
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
              disabled={!ready}
              onPress={whenReady(canUseConnection, () => {
                void state.reloadCatalog(selected);
              })}
            >
              Retry catalog
            </Action>
          )}
          {installedError && (
            <Action
              disabled={!ready}
              onPress={whenReady(canUseConnection, () => {
                void plugins.fetchPlugins();
              })}
            >
              Retry installed status
            </Action>
          )}
        </>
      ) : (
        <Action disabled={busy || !ready} onPress={whenReady(canUseConnection, () => setAdding(true))}>
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
            if (canUseConnection()) void state.reloadCatalog(selected);
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
                  disabled={
                    busy ||
                    !plugins.plugins ||
                    !!installedError ||
                    (!existing && !ready)
                  }
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
          // A failed read keeps the last list in the store; rows a failed
          // read cannot vouch for stay hidden until a fresh read lands, and
          // the error copy and Retry above speak instead. The empty-state
          // copy below claims only what the retained list itself says: a
          // non-empty list a failed read hides must not also read as "no
          // marketplaces" beside that error, while a list the last trusted
          // read left genuinely empty may still say so.
          data={state.marketplacesError === null ? state.marketplaces ?? [] : []}
          keyExtractor={(item) => item.name}
          contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
          ListHeaderComponent={header}
          refreshing={state.marketplacesLoading}
          onRefresh={() => {
            if (canUseConnection()) void state.fetchMarketplaces();
          }}
          ListEmptyComponent={
            state.marketplacesLoading ? (
              <ActivityIndicator accessibilityLabel="Loading marketplaces" />
            ) : state.marketplaces?.length === 0 ? (
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
          connectionState={connectionState}
          hubName={hubName}
          gate={gate}
          ready={ready}
          canUseConnection={canUseConnection}
          onClose={() => setAdding(false)}
          onAdd={async (params) => {
            // The list the screen last carried, to tell the add's own
            // registration from the rows that were already there - null when
            // no read has ever landed, in which case nothing here can tell
            // new from old and the fence waits for the next authoritative
            // read instead.
            const before = marketplaces.getState().marketplaces;
            await state.addMarketplace(params);
            if (params.name) {
              onMarketplaceAdded(params.name, client);
              return;
            }
            // A blank submitted name is one the hub assigns from the
            // source's own catalog, and the store's publication of the add
            // cannot be trusted to name it: a newer list read holds it. Name
            // the registration off the add's own answer, captured as it
            // passed through the client - independent of every store.
            const after = lastAddMarketplaces.current;
            if (after === null || before === null) return;
            for (const name of addedMarketplaceNames(before, after))
              onMarketplaceAdded(name, client);
            // Nothing else names anything here. A re-registration the wire
            // cannot tell from the stale row it replaced - the same key in
            // the answer as in the list before the add - is invisible to
            // every diff, and the add's submitted source proves nothing
            // about a row the answer still carries unchanged. No name is
            // reported for it: its fence retires without one, under the
            // fallback ruling - the add's own answer publishes as a trusted
            // whole-list write, and its report, like any read that follows,
            // retires the fence whatever it carries. A publication the
            // answer itself cannot make - held behind a newer read -
            // leaves the fence to the next one: the read that holds it, or
            // the remount's own.
          }}
        />
      )}
    </>
  );
}

export function AddMarketplace({
  connectionState,
  client,
  hubName,
  gate,
  ready,
  canUseConnection,
  onClose,
  onAdd,
}: {
  connectionState: ConnectionState;
  hubName: string;
  gate: PluginMutationGate;
  ready: boolean;
  canUseConnection: LiveReadiness;
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
  // Everything below gates on this, not on `busy` alone: `busy` is only true
  // while a submission is actually in flight, and disables nothing while
  // disconnected on its own.
  const disabled = busy || !ready;
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  async function submit() {
    if (disabled || !source.trim()) return;
    setBusy(true);
    setError(null);
    const value = source.trim();
    const outcome = await runGatedMutation(gate, canUseConnection, () =>
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
    if (outcome === "not-ready") {
      // A readiness refusal means nothing ran: the modal keeps its draft
      // for the connection it was opened on, and the status it already
      // shows covers the reason nothing ran. Only a write that ran closes
      // it.
      return;
    }
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
        <ModalConnectionStatus connectionState={connectionState} />
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
                disabled={disabled}
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
              disabled={disabled || !source.trim()}
              onPress={whenReady(canUseConnection, () => {
                void submit();
              })}
            >
              Add marketplace
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
