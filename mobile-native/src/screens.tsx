import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect, useIsFocused } from "@react-navigation/native";
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
  Keyboard,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  Text,
  TextInput,
  useWindowDimensions,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { createConversationService } from "../../mobile/src/services/conversation";
import {
  createRosterService,
  type RosterEntry,
} from "../../mobile/src/services/roster";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { type ComposerSetting, ComposerSettings } from "./ComposerSettings";
import { ComposerSettingsSheet } from "./ComposerSettingsSheet";
import { useConnection } from "./ConnectionProvider";
import { drafts } from "./nativeDrafts";
import { QueueSheet } from "./QueueSheet";
import { SessionSheet } from "./SessionSheet";
import { SessionControls } from "./sessionControls";
import { TimelineItem } from "./TimelineItem";
import { groupTimeline } from "./timeline";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const NATIVE_ROSTER_PAGE_SIZE = 50;
const noControls = () => null;
const noControlSubscription = () => () => {};

export type Routes = {
  Hubs: undefined;
  Sessions: undefined;
  NewSession: { hubId: string; hubName: string };
  Conversation: { hubId: string; ref: string; title: string };
};

function ConnectionStatus() {
  const { state, error, retry, activeProfile } = useConnection();
  return (
    <View style={{ paddingHorizontal: 16 }}>
      <View style={styles.row}>
        <View style={styles.fill}>
          <Copy muted>
            {activeProfile?.name ?? "No hub selected"} ·{" "}
            {state === "ready" ? "Connected" : state}
          </Copy>
        </View>
        {state !== "ready" ? <Action onPress={retry}>Reconnect</Action> : null}
      </View>
      <ErrorMessage message={error} />
    </View>
  );
}

export function HubsScreen({
  navigation,
}: NativeStackScreenProps<Routes, "Hubs">) {
  const { profiles, activeProfile, saveHub, selectHub, removeHub, loading } =
    useConnection();
  const colors = useColors();
  const headerHeight = useHeaderHeight();
  const [name, setName] = useState("");
  const [origin, setOrigin] = useState("");
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputStyle = [
    styles.input,
    {
      color: colors.text,
      borderColor: colors.border,
      backgroundColor: colors.surface,
    },
  ];
  async function save() {
    if (saving) return;
    setSaving(true);
    setError(null);
    try {
      await saveHub({ name, origin, token });
      setName("");
      setOrigin("");
      setToken("");
      navigation.navigate("Sessions");
    } catch {
      setError(
        "Could not save this hub. Check the name and http(s) origin, and try again.",
      );
    } finally {
      setSaving(false);
    }
  }
  function remove(id: string, label: string) {
    Alert.alert(
      `Remove ${label}?`,
      "The saved hub, its credentials, and its local drafts will be removed from this device.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Remove",
          style: "destructive",
          onPress: () => {
            void removeHub(id).catch((error: unknown) =>
              setError(
                error instanceof Error
                  ? error.message
                  : "Could not remove this hub. Try again.",
              ),
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
      <KeyboardAvoidingView
        style={styles.fill}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
        keyboardVerticalOffset={headerHeight}
      >
        <ScrollView
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={styles.padded}
        >
          <Text style={[styles.title, { color: colors.text }]}>Saved hubs</Text>
          {loading ? (
            <ActivityIndicator accessibilityLabel="Loading saved hubs" />
          ) : profiles.length === 0 ? (
            <Copy muted>Add a hub to browse your sessions.</Copy>
          ) : null}
          {profiles.map((profile) => (
            <View
              key={profile.id}
              style={[
                styles.card,
                { borderColor: colors.border, backgroundColor: colors.surface },
              ]}
            >
              <Pressable
                accessibilityRole="button"
                accessibilityLabel={`Open ${profile.name}`}
                onPress={() => {
                  selectHub(profile.id);
                  navigation.navigate("Sessions");
                }}
                style={{ gap: 8, minHeight: 44 }}
              >
                <Copy>
                  {profile.name}
                  {activeProfile?.id === profile.id ? " · Selected" : ""}
                </Copy>
                <Copy muted>{profile.origin}</Copy>
              </Pressable>
              <Action
                onPress={() => remove(profile.id, profile.name)}
                label={`Remove ${profile.name}`}
              >
                Remove
              </Action>
            </View>
          ))}
          <Text style={[styles.title, { color: colors.text, marginTop: 16 }]}>
            Add hub
          </Text>
          <Copy muted>
            Enter the hub’s origin, such as https://hub.example.com:9180.
          </Copy>
          <TextInput
            accessibilityLabel="Hub name"
            placeholder="Hub name"
            placeholderTextColor={colors.secondary}
            value={name}
            onChangeText={setName}
            style={inputStyle}
          />
          <TextInput
            accessibilityLabel="Hub origin"
            placeholder="https://hub.example.com:9180"
            placeholderTextColor={colors.secondary}
            value={origin}
            onChangeText={setOrigin}
            autoCapitalize="none"
            autoCorrect={false}
            keyboardType="url"
            style={inputStyle}
          />
          <TextInput
            accessibilityLabel="Bearer token, optional"
            placeholder="Bearer token (optional)"
            placeholderTextColor={colors.secondary}
            value={token}
            onChangeText={setToken}
            autoCapitalize="none"
            autoCorrect={false}
            secureTextEntry
            style={inputStyle}
          />
          <ErrorMessage message={error} />
          <Action
            onPress={() => {
              void save();
            }}
            disabled={saving || loading || !name.trim() || !origin.trim()}
          >
            {saving ? "Saving…" : "Save and connect"}
          </Action>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

export function SessionsScreen({
  navigation,
}: NativeStackScreenProps<Routes, "Sessions">) {
  const { activeProfile, client, state } = useConnection();
  const colors = useColors();
  const [rows, setRows] = useState<RosterEntry[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const request = useRef(0);
  const service = useMemo(
    () =>
      client ? createRosterService(client, NATIVE_ROSTER_PAGE_SIZE) : null,
    [client],
  );
  const refresh = useCallback(async () => {
    const generation = ++request.current;
    if (!service || state !== "ready") {
      setRefreshing(false);
      return;
    }
    setRefreshing(true);
    setError(null);
    try {
      const result = await service.list();
      if (generation !== request.current) return;
      setRows(result.threads);
      setHasMore(result.hasMore);
    } catch {
      if (generation === request.current)
        setError("Could not load sessions. Pull down to retry.");
    } finally {
      if (generation === request.current) setRefreshing(false);
    }
  }, [service, state]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: Changing hubs must discard the previous hub roster.
  useEffect(() => {
    setRows([]);
    setHasMore(false);
  }, [activeProfile?.id]);
  useFocusEffect(
    useCallback(() => {
      void refresh();
      return () => {
        request.current += 1;
      };
    }, [refresh]),
  );
  useEffect(() => {
    navigation.setOptions({
      headerLeft: () => (
        <Action onPress={() => navigation.popToTop()}>Hubs</Action>
      ),
      headerRight: () => (
        <Action
          disabled={!activeProfile || state !== "ready"}
          onPress={() => {
            if (activeProfile)
              navigation.navigate("NewSession", {
                hubId: activeProfile.id,
                hubName: activeProfile.name,
              });
          }}
        >
          New session
        </Action>
      ),
    });
  }, [navigation, activeProfile, state]);
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <ConnectionStatus />
      <ErrorMessage message={error} />
      <FlatList
        data={rows}
        keyExtractor={(item) => item.ref}
        refreshing={refreshing}
        onRefresh={() => {
          void refresh();
        }}
        contentContainerStyle={styles.padded}
        ListEmptyComponent={
          <Copy muted>
            {refreshing
              ? "Loading sessions…"
              : state !== "ready"
                ? "Connect to the hub to load sessions."
                : error
                  ? "Pull down to retry loading sessions."
                  : "No sessions on this hub yet."}
          </Copy>
        }
        ListFooterComponent={
          hasMore ? (
            <Copy muted>
              Showing up to {NATIVE_ROSTER_PAGE_SIZE} sessions. More may be
              available.
            </Copy>
          ) : null
        }
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`Open ${item.title || "Untitled session"}`}
            disabled={state !== "ready" || !activeProfile}
            onPress={() => {
              if (activeProfile)
                navigation.navigate("Conversation", {
                  hubId: activeProfile.id,
                  ref: item.ref,
                  title: item.title,
                });
            }}
            style={[
              {
                paddingVertical: 13,
                minHeight: Platform.OS === "android" ? 72 : 68,
                borderBottomWidth: 0.5,
                borderColor: colors.border,
                gap: 4,
              },
            ]}
          >
            <Text
              numberOfLines={2}
              style={{ color: colors.text, fontSize: 17, fontWeight: "500" }}
            >
              {item.title || "Untitled session"}
            </Text>
            <Text
              numberOfLines={2}
              style={{
                color:
                  item.attention === "needsYou"
                    ? colors.accent
                    : colors.secondary,
                fontSize: 13,
                lineHeight: 19,
              }}
            >
              {item.attention === "needsYou" ? "Needs you" : item.status}
              {item.project ? ` · ${item.project}` : ""}
            </Text>
          </Pressable>
        )}
      />
    </SafeAreaView>
  );
}

export function ConversationScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "Conversation">) {
  const { activeProfile, client, state: connectionState } = useConnection();
  const colors = useColors();
  const { fontScale } = useWindowDimensions();
  const headerHeight = useHeaderHeight();
  // biome-ignore lint/correctness/useExhaustiveDependencies: Each route destination owns an independent conversation binding.
  const store = useMemo(
    () => createConversationStore(),
    [route.params.hubId, route.params.ref],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: Activity lifetime follows its conversation binding.
  const activity = useMemo(() => createActivityStore(), [store]);
  // The conversation store validates the exact bound sink object on refresh.
  const activitySink = useMemo(() => activity.getState(), [activity]);
  const service = useMemo(
    () => (client ? createConversationService(client) : null),
    [client],
  );
  const snapshot = store();
  const timeline = useRef<FlatList>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [queueOpen, setQueueOpen] = useState(false);
  const [sessionOpen, setSessionOpen] = useState(false);
  const [composerSetting, setComposerSetting] =
    useState<ComposerSetting | null>(null);
  useFocusEffect(
    useCallback(
      () => () => {
        setQueueOpen(false);
        setSessionOpen(false);
        setComposerSetting(null);
      },
      [],
    ),
  );
  const [actionError, setActionError] = useState<string | null>(null);
  const document = useMemo(
    () =>
      drafts.open({ hubId: route.params.hubId, sessionRef: route.params.ref }),
    [route.params.hubId, route.params.ref],
  );
  const draft = useSyncExternalStore(document.subscribe, document.getSnapshot);
  const unconfirmedSend = draft.submitting ? null : draft.record.unconfirmed;
  const connected =
    connectionState === "ready" && activeProfile?.id === route.params.hubId;
  useEffect(() => {
    if (!service || !connected) return;
    void store
      .getState()
      .openProjected(service, activitySink, route.params.ref);
    return () => {
      store.getState().close();
      service.close();
    };
  }, [service, store, activitySink, connected, route.params.ref]);
  async function refresh() {
    if (!service || !connected || refreshing) return;
    setRefreshing(true);
    try {
      if (store.getState().status === "open")
        await store.getState().rehydrate(service, activitySink);
      else {
        await store
          .getState()
          .openProjected(service, activitySink, route.params.ref);
      }
    } finally {
      setRefreshing(false);
    }
  }
  const focused = useIsFocused();
  const connectionReady = useRef(connected);
  connectionReady.current = connected;
  const bindingGeneration = snapshot.conversationGeneration;
  const bindingInstance = snapshot.conversation?.instanceId;
  const controls = useMemo(
    () =>
      service && connected && focused
        ? new SessionControls(
            service,
            async () => {
              await store.getState().rehydrate(service, activitySink);
              const current = store.getState();
              if (current.status !== "open" || current.error)
                throw new Error("Session refresh failed");
            },
            () => {
              store.getState().close();
              service.close();
              setSessionOpen(false);
              if (navigation.isFocused()) navigation.goBack();
            },
            () => {
              const current = store.getState();
              return (
                connectionReady.current &&
                navigation.isFocused() &&
                current.status === "open" &&
                current.conversationGeneration === bindingGeneration &&
                current.conversation?.instanceId === bindingInstance
              );
            },
            () => store.getState().conversation,
            () =>
              !document.getSnapshot().submitting &&
              store.getState().pendingMutation?.status !== "pending",
          )
        : null,
    [
      service,
      store,
      activitySink,
      navigation,
      connected,
      focused,
      bindingGeneration,
      bindingInstance,
      document,
    ],
  );
  useEffect(() => () => controls?.dispose(), [controls]);
  const hasConversation = snapshot.conversation !== null;
  useEffect(() => {
    navigation.setOptions({
      headerRight: () => (
        <Action
          disabled={!hasConversation}
          onPress={() => {
            Keyboard.dismiss();
            setSessionOpen(true);
          }}
        >
          Session
        </Action>
      ),
    });
  }, [navigation, hasConversation]);
  const currentName = snapshot.conversation?.name;
  useEffect(() => {
    if (currentName && currentName !== route.params.title)
      navigation.setParams({ title: currentName });
  }, [navigation, currentName, route.params.title]);
  const conversation = snapshot.conversation;
  const timelineRows = useMemo(
    () => groupTimeline(conversation?.items ?? []),
    [conversation?.items],
  );
  const controlsState = useSyncExternalStore(
    controls?.subscribe ?? noControlSubscription,
    controls?.getSnapshot ?? noControls,
  );
  const settingsPending = controlsState?.pending != null;
  const pending = snapshot.pendingMutation?.status === "pending";
  const ready =
    connected &&
    snapshot.status === "open" &&
    !refreshing &&
    !pending &&
    !settingsPending;
  async function mutate(kind: "send" | "steer" | "queue" | "interrupt") {
    if (
      !service ||
      !ready ||
      controls?.getSnapshot().pending != null ||
      (kind !== "interrupt" && unconfirmedSend !== null) ||
      store.getState().pendingMutation?.status === "pending"
    )
      return;
    setActionError(null);
    try {
      if (kind !== "interrupt")
        await document.submit(async (text) => {
          store.getState().setDraft(text);
          const previous = store.getState().lastAcceptedMutation;
          await store.getState()[kind](service, [{ type: "text", text }]);
          const accepted = store.getState().lastAcceptedMutation;
          return (
            accepted != null && accepted !== previous && accepted.kind === kind
          );
        });
      else await store.getState().interrupt(service);
    } catch {
      setActionError(
        "This action is unavailable. Refresh the conversation and try again.",
      );
    }
  }
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      {composerSetting && conversation && controls ? (
        <ComposerSettingsSheet
          setting={composerSetting}
          conversation={conversation}
          controls={controls}
          hubName={
            activeProfile?.id === route.params.hubId
              ? activeProfile.name
              : "Disconnected hub"
          }
          ready={ready}
          close={() => setComposerSetting(null)}
        />
      ) : null}
      {sessionOpen && conversation && controls ? (
        <SessionSheet
          conversation={conversation}
          controls={controls}
          hubName={
            activeProfile?.id === route.params.hubId
              ? activeProfile.name
              : "Disconnected hub"
          }
          ready={ready}
          close={() => setSessionOpen(false)}
        />
      ) : null}
      {queueOpen && conversation && service ? (
        <QueueSheet
          conversation={conversation}
          service={service}
          ready={ready}
          refresh={async () => {
            await refresh();
            const current = store.getState();
            if (current.status !== "open" || current.error)
              throw new Error("Queue refresh failed");
          }}
          close={() => setQueueOpen(false)}
        />
      ) : null}
      <KeyboardAvoidingView
        style={styles.fill}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
        keyboardVerticalOffset={headerHeight}
      >
        <FlatList
          ref={timeline}
          data={timelineRows}
          keyExtractor={(item) => item.id}
          renderItem={({ item }) => <TimelineItem item={item} />}
          contentContainerStyle={{ padding: 16 }}
          ItemSeparatorComponent={() => <View style={{ height: 24 }} />}
          keyboardShouldPersistTaps="handled"
          refreshing={refreshing}
          onRefresh={() => {
            void refresh();
          }}
          ListHeaderComponent={
            <View style={{ gap: 12, paddingBottom: 16 }}>
              <ConnectionStatus />
              <ErrorMessage message={snapshot.error || actionError} />
              <ErrorMessage message={draft.error} />
              {unconfirmedSend !== null ? (
                <View
                  style={[
                    styles.card,
                    { marginHorizontal: 16, borderColor: colors.border },
                  ]}
                >
                  <Copy>Delivery unconfirmed</Copy>
                  <Copy muted>
                    Check the transcript before sending again. This message may
                    have reached the hub.
                  </Copy>
                  <ScrollView style={{ maxHeight: 100 }}>
                    <Copy>{unconfirmedSend}</Copy>
                  </ScrollView>
                  <View style={styles.row}>
                    <Action
                      disabled={draft.record.draft !== ""}
                      onPress={() => document.restore()}
                    >
                      Restore to draft
                    </Action>
                    <Action onPress={() => document.dismiss()}>Dismiss</Action>
                  </View>
                  {draft.record.draft !== "" ? (
                    <Copy muted>
                      Your current draft is kept. Clear it to restore this
                      message.
                    </Copy>
                  ) : null}
                </View>
              ) : null}
              <View style={{ flexShrink: 1 }}>
                {connected &&
                conversation &&
                !conversation.capabilities.send &&
                !conversation.capabilities.steer &&
                !conversation.capabilities.queue ? (
                  <Copy muted>Sending is unavailable for this session.</Copy>
                ) : null}
              </View>
              {snapshot.olderCursor ? (
                <Action
                  disabled={!ready || snapshot.loadingOlder}
                  onPress={() => {
                    if (service) void store.getState().loadOlder(service);
                  }}
                >
                  {snapshot.loadingOlder ? "Loading…" : "Load older messages"}
                </Action>
              ) : null}
            </View>
          }
          ListEmptyComponent={
            <Copy muted>
              {snapshot.status === "opening"
                ? "Loading conversation…"
                : !connected
                  ? "Reconnect to load the conversation. Your draft is kept."
                  : snapshot.status === "error"
                    ? "Pull down to retry."
                    : "No messages yet."}
            </Copy>
          }
        />
        <View
          style={[
            {
              marginHorizontal: 12,
              marginTop: 8,
              marginBottom: 8,
              padding: 12,
              gap: 8,
              borderWidth: 1,
              borderRadius: 22,
              borderColor: colors.border,
              backgroundColor: colors.surface,
            },
          ]}
        >
          {conversation?.queue.depth ? (
            <Action
              tone="quiet"
              expanded={queueOpen}
              onPress={() => {
                Keyboard.dismiss();
                setQueueOpen(true);
              }}
            >
              {`${conversation.queue.depth} queued`}
            </Action>
          ) : null}
          <TextInput
            accessibilityLabel="Message"
            multiline
            value={draft.record.draft}
            onChangeText={(text) => document.edit(text)}
            editable={draft.loaded}
            placeholder="Message"
            placeholderTextColor={colors.secondary}
            style={[
              styles.input,
              {
                color: colors.text,
                backgroundColor: colors.surface,
                borderWidth: 0,
                padding: 2,
                minHeight: Platform.OS === "android" ? 48 : 44,
                maxHeight: fontScale > 1.6 ? 96 : 160,
                textAlignVertical: "top",
              },
            ]}
          />
          <View
            style={[
              styles.row,
              { flexWrap: "wrap", justifyContent: "flex-end", gap: 4 },
            ]}
          >
            {conversation ? (
              <ComposerSettings
                conversation={conversation}
                disabled={!ready || !controls || draft.submitting}
                pending={settingsPending}
                open={(setting) => {
                  Keyboard.dismiss();
                  setComposerSetting(setting);
                }}
              />
            ) : null}
            {controlsState?.error &&
            (controlsState.lastAction === "changeModel" ||
              controlsState.lastAction === "setReasoningEffort") ? (
              <Action
                tone="quiet"
                onPress={() => {
                  Keyboard.dismiss();
                  setComposerSetting(
                    controlsState.lastAction === "changeModel"
                      ? "model"
                      : "reasoning",
                  );
                }}
              >
                Review settings error
              </Action>
            ) : null}
            {draft.error ? (
              <Action tone="accent" onPress={document.retry}>
                {draft.loaded ? "Retry saving" : "Retry loading draft"}
              </Action>
            ) : null}
            {!connected ||
            snapshot.error ||
            actionError ||
            unconfirmedSend !== null ? (
              <Action
                tone="quiet"
                onPress={() => {
                  Keyboard.dismiss();
                  timeline.current?.scrollToOffset({
                    offset: 0,
                    animated: true,
                  });
                }}
              >
                {unconfirmedSend !== null
                  ? "Check delivery"
                  : !connected
                    ? "Connection"
                    : "Review error"}
              </Action>
            ) : conversation?.items.length ? (
              <Action
                tone="quiet"
                onPress={() =>
                  timeline.current?.scrollToEnd({ animated: true })
                }
              >
                Latest
              </Action>
            ) : null}
            {conversation?.capabilities.interrupt ? (
              <Action
                tone="quiet"
                disabled={!ready}
                onPress={() => {
                  void mutate("interrupt");
                }}
              >
                Stop
              </Action>
            ) : null}
            {(["send", "steer", "queue"] as const).map((kind) =>
              conversation?.capabilities[kind] ? (
                <Action
                  key={kind}
                  tone={
                    kind === "send" ||
                    (kind === "steer" && !conversation.capabilities.send)
                      ? "primary"
                      : "quiet"
                  }
                  disabled={
                    !ready ||
                    !draft.loaded ||
                    !!draft.error ||
                    draft.submitting ||
                    unconfirmedSend !== null ||
                    !draft.record.draft.trim()
                  }
                  onPress={() => {
                    void mutate(kind);
                  }}
                >
                  {kind === "send"
                    ? "Send"
                    : kind === "steer"
                      ? "Steer"
                      : "Queue"}
                </Action>
              ) : null,
            )}
          </View>
        </View>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
