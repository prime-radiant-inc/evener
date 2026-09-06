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
import { sourceLabel } from "../../cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/sourceLabel";
import type {
  MarketplaceAddParams,
  PluginRefParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubPathField } from "./HubPathField";
import type { InstalledPlugins } from "./installedPlugins";
import { Marketplaces } from "./marketplaces";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function MarketplaceBrowser({
  client,
  hubName,
  installed,
  onOpenPlugin,
}: {
  client: ConversationClientLike;
  hubName: string;
  installed: InstalledPlugins;
  onOpenPlugin(target: PluginRefParams): void;
}) {
  const colors = useColors();
  const model = useMemo(() => new Marketplaces(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const plugins = useSyncExternalStore(
    installed.subscribe,
    installed.getSnapshot,
  );
  const [adding, setAdding] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const revision = useRef(0);
  const previousSelection = useRef<string | null>(null);
  useEffect(() => {
    model.start();
    return () => {
      revision.current += 1;
      model.dispose();
    };
  }, [model]);
  useEffect(() => {
    if (previousSelection.current === state.selected) return;
    previousSelection.current = state.selected;
    revision.current += 1;
    setError(null);
    setQuery("");
  }, [state.selected]);
  const busy = state.busy || plugins.busy;
  async function act(action: () => Promise<void>) {
    const version = revision.current;
    setError(null);
    try {
      await action();
    } catch {
      if (revision.current === version)
        setError(
          "Could not confirm the change. Refresh and check its status before trying again.",
        );
    }
  }
  const marketplace = state.marketplaces?.find(
    (item) => item.name === state.selected,
  );
  function remove() {
    if (!marketplace || busy) return;
    const name = marketplace.name;
    const version = revision.current;
    Alert.alert("Remove marketplace?", `${name} on ${hubName}`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Remove",
        style: "destructive",
        onPress: () => {
          if (revision.current === version) void act(() => model.remove(name));
        },
      },
    ]);
  }
  const needle = query.trim().toLowerCase();
  const catalog = (state.catalog?.plugins ?? []).filter((item) =>
    `${item.name} ${item.description ?? ""}`.toLowerCase().includes(needle),
  );
  const header = (
    <View style={{ gap: 8, paddingBottom: 12 }}>
      <Copy muted>{hubName}</Copy>
      <ErrorMessage message={error || state.listError} />
      {state.listError && (
        <Action
          onPress={() => {
            void model.refresh();
          }}
        >
          Retry marketplaces
        </Action>
      )}
      {state.selected ? (
        <>
          <Action
            onPress={() => {
              void model.select(null);
            }}
          >
            All marketplaces
          </Action>
          <Copy>{state.selected}</Copy>
          {marketplace && <Copy muted>{sourceLabel(marketplace.source)}</Copy>}
          {state.catalog?.description && (
            <Copy>{state.catalog.description}</Copy>
          )}
          <View style={[styles.row, { flexWrap: "wrap" }]}>
            <Action
              disabled={busy}
              onPress={() => {
                if (state.selected)
                  void act(() => model.refreshSource(state.selected as string));
              }}
            >
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
          <ErrorMessage message={state.catalogError || plugins.error} />
          {state.catalogError && (
            <Action
              onPress={() => {
                void model.browse();
              }}
            >
              Retry catalog
            </Action>
          )}
          {plugins.error && (
            <Action
              onPress={() => {
                void installed.refresh();
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
      {state.selected ? (
        <FlatList
          data={catalog}
          keyExtractor={(item) => item.name}
          contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
          keyboardShouldPersistTaps="handled"
          ListHeaderComponent={header}
          refreshing={state.browsing}
          onRefresh={() => {
            void model.browse();
          }}
          ListEmptyComponent={
            state.browsing ? (
              <ActivityIndicator accessibilityLabel="Loading marketplace catalog" />
            ) : state.catalog ? (
              <Copy muted>
                {needle
                  ? "No matching plugins."
                  : "No plugins in this catalog."}
              </Copy>
            ) : null
          }
          renderItem={({ item }) => {
            const target = {
              plugin: item.name,
              marketplace: state.selected as string,
            };
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
                  disabled={busy || !plugins.plugins || !!plugins.error}
                  label={`${existing ? "Open" : "Install"} ${item.name} from ${target.marketplace}`}
                  onPress={() => {
                    if (existing) onOpenPlugin(target);
                    else void act(() => installed.install(target));
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
          refreshing={state.loading}
          onRefresh={() => {
            void model.refresh();
          }}
          ListEmptyComponent={
            state.loading ? (
              <ActivityIndicator accessibilityLabel="Loading marketplaces" />
            ) : state.marketplaces ? (
              <Copy muted>No marketplaces on this hub.</Copy>
            ) : null
          }
          renderItem={({ item }) => (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Browse ${item.name}`}
              onPress={() => {
                void model.select(item.name);
              }}
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
                {sourceLabel(item.source)}
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
          onAdd={model.add}
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
}: {
  hubName: string;
  onClose(): void;
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
    try {
      await onAdd({
        name: name.trim(),
        source:
          kind === "github"
            ? { kind, repo: value }
            : kind === "directory"
              ? { kind, path: value }
              : { kind, url: value },
      });
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
