import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useHeaderHeight } from "@react-navigation/elements";
import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  ActivityIndicator,
  Alert,
  FlatList,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  Text,
  TextInput,
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
import { useConnection } from "./ConnectionProvider";
import { TimelineItem } from "./TimelineItem";
import {
  captureUnconfirmedSend,
  restoreUnconfirmedDraft,
} from "./draftRecovery";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const NATIVE_ROSTER_PAGE_SIZE = 50;

export type Routes = {
  Hubs: undefined;
  Sessions: undefined;
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
      "The saved hub and its credentials will be removed from this device.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Remove",
          style: "destructive",
          onPress: () => {
            void removeHub(id).catch(() =>
              setError("Could not remove this hub. Try again."),
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
        behavior={Platform.OS === "ios" ? "padding" : undefined}
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
    });
  }, [navigation]);
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
              styles.card,
              {
                marginBottom: 12,
                backgroundColor: colors.surface,
                borderColor: colors.border,
              },
            ]}
          >
            <Text
              numberOfLines={2}
              style={{ color: colors.text, fontSize: 18, fontWeight: "600" }}
            >
              {item.title || "Untitled session"}
            </Text>
            <Copy muted>
              {item.status}
              {item.attention === "needsYou" ? " · Needs you" : ""}
            </Copy>
            {item.project ? (
              <Text numberOfLines={1} style={{ color: colors.secondary }}>
                {item.project}
              </Text>
            ) : null}
          </Pressable>
        )}
      />
    </SafeAreaView>
  );
}

export function ConversationScreen({
  route,
}: NativeStackScreenProps<Routes, "Conversation">) {
  const { activeProfile, client, state: connectionState } = useConnection();
  const colors = useColors();
  const headerHeight = useHeaderHeight();
  const store = useMemo(
    () => createConversationStore(),
    [route.params.hubId, route.params.ref],
  );
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
  const [actionError, setActionError] = useState<string | null>(null);
  const [unconfirmedSend, setUnconfirmedSend] = useState<string | null>(null);
  useEffect(() => {
    setUnconfirmedSend(null);
  }, [store]);
  const connected =
    connectionState === "ready" && activeProfile?.id === route.params.hubId;
  useEffect(() => {
    if (!service || !connected) return;
    const draft = store.getState().draft;
    void store
      .getState()
      .openProjected(service, activitySink, route.params.ref);
    store.getState().setDraft(draft);
    return () => {
      const closing = store.getState();
      const submitted = captureUnconfirmedSend(closing.pendingMutation);
      if (submitted !== null) setUnconfirmedSend(submitted);
      const savedDraft = closing.draft;
      store.getState().close();
      store.getState().setDraft(savedDraft);
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
        const draft = store.getState().draft;
        const opening = store
          .getState()
          .openProjected(service, activitySink, route.params.ref);
        store.getState().setDraft(draft);
        await opening;
      }
    } finally {
      setRefreshing(false);
    }
  }
  const conversation = snapshot.conversation;
  const pending = snapshot.pendingMutation?.status === "pending";
  const ready =
    connected && snapshot.status === "open" && !refreshing && !pending;
  async function mutate(kind: "send" | "interrupt") {
    if (
      !service ||
      !ready ||
      (kind === "send" && unconfirmedSend !== null) ||
      store.getState().pendingMutation?.status === "pending"
    )
      return;
    setActionError(null);
    try {
      if (kind === "send")
        await store
          .getState()
          .send(service, [{ type: "text", text: store.getState().draft }]);
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
      <KeyboardAvoidingView
        style={styles.fill}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
        keyboardVerticalOffset={headerHeight}
      >
        <ConnectionStatus />
        {conversation?.items.length ? (
          <View style={{ alignItems: "flex-end", paddingHorizontal: 16 }}>
            <Action
              onPress={() => timeline.current?.scrollToEnd({ animated: true })}
            >
              Latest messages
            </Action>
          </View>
        ) : null}
        <ErrorMessage message={snapshot.error || actionError} />
        {unconfirmedSend !== null ? (
          <View
            style={[
              styles.card,
              { marginHorizontal: 16, borderColor: colors.border },
            ]}
          >
            <Copy>Delivery unconfirmed</Copy>
            <Copy muted>
              Check the transcript before sending again. This message may have
              reached the hub.
            </Copy>
            <ScrollView style={{ maxHeight: 100 }}>
              <Copy>{unconfirmedSend}</Copy>
            </ScrollView>
            <View style={styles.row}>
              <Action
                disabled={snapshot.draft !== ""}
                onPress={() => {
                  const restored = restoreUnconfirmedDraft(
                    store.getState().draft,
                    unconfirmedSend,
                  );
                  if (restored === null) return;
                  store.getState().setDraft(restored);
                  setUnconfirmedSend(null);
                }}
              >
                Restore to draft
              </Action>
              <Action onPress={() => setUnconfirmedSend(null)}>Dismiss</Action>
            </View>
            {snapshot.draft !== "" ? (
              <Copy muted>
                Your current draft is kept. Clear it to restore this message.
              </Copy>
            ) : null}
          </View>
        ) : null}
        <FlatList
          ref={timeline}
          data={conversation?.items ?? []}
          keyExtractor={(item) => item.id}
          renderItem={({ item }) => <TimelineItem item={item} />}
          contentContainerStyle={styles.padded}
          ItemSeparatorComponent={() => <View style={{ height: 12 }} />}
          keyboardShouldPersistTaps="handled"
          refreshing={refreshing}
          onRefresh={() => {
            void refresh();
          }}
          ListHeaderComponent={
            snapshot.olderCursor ? (
              <Action
                disabled={!ready || snapshot.loadingOlder}
                onPress={() => {
                  if (service) void store.getState().loadOlder(service);
                }}
              >
                {snapshot.loadingOlder ? "Loading…" : "Load older messages"}
              </Action>
            ) : null
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
            styles.padded,
            { borderTopWidth: 1, borderColor: colors.border },
          ]}
        >
          {conversation ? (
            <Copy muted>
              {conversation.status}
              {conversation.queue.depth
                ? ` · ${conversation.queue.depth} queued`
                : ""}
            </Copy>
          ) : null}
          <TextInput
            accessibilityLabel="Message"
            multiline
            value={snapshot.draft}
            onChangeText={snapshot.setDraft}
            placeholder="Message"
            placeholderTextColor={colors.secondary}
            style={[
              styles.input,
              {
                color: colors.text,
                backgroundColor: colors.surface,
                borderColor: colors.border,
                maxHeight: 160,
                textAlignVertical: "top",
              },
            ]}
          />
          <View style={styles.row}>
            <View style={styles.fill}>
              {connected && conversation && !conversation.capabilities.send ? (
                <Copy muted>Sending is unavailable for this session.</Copy>
              ) : null}
            </View>
            {conversation?.capabilities.interrupt ? (
              <Action
                disabled={!ready}
                onPress={() => {
                  void mutate("interrupt");
                }}
              >
                Stop
              </Action>
            ) : null}
            <Action
              disabled={
                !ready ||
                !conversation?.capabilities.send ||
                unconfirmedSend !== null ||
                !snapshot.draft.trim()
              }
              onPress={() => {
                void mutate("send");
              }}
            >
              {pending ? "Sending…" : "Send"}
            </Action>
          </View>
        </View>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
