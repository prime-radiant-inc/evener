import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  ActivityIndicator,
  Modal,
  Platform,
  Pressable,
  SectionList,
  View,
} from "react-native";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import type { TaskRow } from "@evener/appwire-client";
import {
  absoluteTime,
  createTasksPanelStore,
  EMPTY_TASKS_PANEL_ENTRY,
  groupTasks,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { MarkdownResponse } from "./MarkdownResponse";
import { tasksReadThroughCurrentClient } from "./tasksRead";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const statusLabel = {
  open: "Open",
  in_progress: "In progress",
  done: "Done",
  cancelled: "Cancelled",
};
const glyph = { open: "○", in_progress: "●", done: "✓", cancelled: "×" };

export function TasksSheet({
  client,
  sessionRef,
  threadId,
  hasTasks,
  connected,
  hubName,
  close,
}: {
  client: ConversationClientLike;
  sessionRef: string;
  threadId: string;
  hasTasks: boolean;
  connected: boolean;
  hubName: string;
  close: () => void;
}) {
  const colors = useColors();
  const wasConnected = useRef(connected);
  const aggregate = useRef(hasTasks);
  aggregate.current = hasTasks;
  const hasAggregate = () => aggregate.current;
  // One store for the sheet's lifetime, reading through whichever client is
  // current: a replacement connection after a reconnect refreshes the same
  // entry, so the last loaded list stays on screen when that refresh fails.
  const currentClient = useRef(client);
  currentClient.current = client;
  const store = useMemo(
    () =>
      createTasksPanelStore(
        tasksReadThroughCurrentClient(() => currentClient.current),
      ),
    [],
  );
  // A thrown port rejects after the store has already settled the entry as
  // a failure, which the header below shows with Try again; nothing to add.
  const refresh = () =>
    void store.refresh(sessionRef, hasAggregate).catch(() => undefined);
  // biome-ignore lint/correctness/useExhaustiveDependencies: hasAggregate reads a ref, so its identity does not matter
  useEffect(
    () => store.watch(client, sessionRef, threadId, hasAggregate),
    [store, client, sessionRef, threadId],
  );
  const state =
    useSyncExternalStore(store.subscribe, () =>
      store.getState().entries.get(sessionRef),
    ) ?? EMPTY_TASKS_PANEL_ENTRY;
  useEffect(() => {
    if (connected && !wasConnected.current) refresh();
    wasConnected.current = connected;
  }, [connected, store, sessionRef]);
  const error = state.failure?.sentence ?? null;
  const [settled, setSettled] = useState(false);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [prompts, setPrompts] = useState<Set<number>>(new Set());
  const groups = groupTasks(state.rows ?? []);
  const sections = [
    {
      title: "In progress",
      key: "active",
      data: groups.inProgress,
      count: groups.inProgress.length,
    },
    {
      title: "Open",
      key: "open",
      data: groups.open,
      count: groups.open.length,
    },
    {
      title: "Done · settled",
      key: "settled",
      data: settled ? groups.settled : [],
      count: groups.settled.length,
    },
  ].filter((section) => section.count > 0);
  function toggle(id: number, prompt = false) {
    const update = (values: Set<number>) => {
      const next = new Set(values);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    };
    if (prompt) setPrompts(update);
    else setExpanded(update);
  }
  function taskRow(task: TaskRow) {
    const open = expanded.has(task.id);
    const latest = task.notes?.at(-1);
    return (
      <View
        style={{
          borderBottomWidth: 0.5,
          borderColor: colors.border,
          paddingVertical: 8,
        }}
      >
        <Pressable
          accessibilityRole="button"
          accessibilityLabel={`${statusLabel[task.status] ?? task.status}: ${task.description}`}
          accessibilityState={{ expanded: open }}
          onPress={() => toggle(task.id)}
          style={{ minHeight: 44, justifyContent: "center", gap: 4 }}
        >
          <Copy>{`${glyph[task.status] ?? "○"} ${task.description}`}</Copy>
          {!open &&
          latest &&
          (task.status === "open" || task.status === "in_progress") ? (
            <Copy muted>{latest}</Copy>
          ) : null}
        </Pressable>
        {open ? (
          <View style={{ gap: 10, paddingVertical: 8 }}>
            <Copy
              muted
            >{`${statusLabel[task.status] ?? task.status} · ${task.type}${task.reasoningEffort ? ` · ${task.reasoningEffort}` : ""}`}</Copy>
            {task.dependsOn?.length ? (
              <Copy
                muted
              >{`Depends on ${task.dependsOn.map((id) => `#${id}`).join(", ")}`}</Copy>
            ) : null}
            {task.createdAt ? (
              <Copy muted>{`Created ${absoluteTime(task.createdAt)}`}</Copy>
            ) : null}
            {task.updatedAt && task.updatedAt !== task.createdAt ? (
              <Copy muted>{`Updated ${absoluteTime(task.updatedAt)}`}</Copy>
            ) : null}
            {/* The settle stamp now also rides cancelled tasks (the store
                stamps every terminal transition); the "Completed" line
                stays a done-row fact, matching the web pane's timestamps. */}
            {task.completedAt && task.status === "done" ? (
              <Copy muted>{`Completed ${absoluteTime(task.completedAt)}`}</Copy>
            ) : null}
            {task.prompt.trim() ? (
              <>
                <Action
                  expanded={prompts.has(task.id)}
                  onPress={() => toggle(task.id, true)}
                >{`Prompt for task #${task.id}`}</Action>
                {prompts.has(task.id) ? (
                  <MarkdownResponse markdown={task.prompt} />
                ) : null}
              </>
            ) : null}
            <Copy muted>
              {task.notes?.length
                ? `Updates · ${task.notes.length}`
                : "No updates yet."}
            </Copy>
            {task.notes?.map((note, index) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: Task notes are append-only; their position is their identity, matching the web task panel.
              <View key={`${task.id}:${index}`}>
                <MarkdownResponse markdown={note} />
              </View>
            ))}
          </View>
        ) : null}
      </View>
    );
  }
  return (
    <Modal
      animationType="slide"
      presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
      onRequestClose={close}
    >
      <SafeAreaProvider>
        <SafeAreaView
          style={[styles.fill, { backgroundColor: colors.background }]}
        >
          <View
            style={[
              styles.row,
              {
                paddingHorizontal: 20,
                paddingVertical: 8,
                borderBottomWidth: 0.5,
                borderColor: colors.border,
              },
            ]}
          >
            <View style={styles.fill}>
              <Copy>Tasks</Copy>
              <Copy muted>{hubName}</Copy>
            </View>
            <Action onPress={close}>Done</Action>
          </View>
          <SectionList
            sections={sections}
            keyExtractor={(task) => String(task.id)}
            stickySectionHeadersEnabled={false}
            contentContainerStyle={{ paddingHorizontal: 20, paddingBottom: 24 }}
            extraData={{ expanded, prompts, settled }}
            renderItem={({ item }) => taskRow(item)}
            renderSectionHeader={({ section }) =>
              section.key === "settled" ? (
                <Action
                  expanded={settled}
                  onPress={() => setSettled(!settled)}
                >{`${section.title} · ${section.count}`}</Action>
              ) : (
                <View style={{ paddingTop: 20, paddingBottom: 4 }}>
                  <Copy muted>{`${section.title} · ${section.count}`}</Copy>
                </View>
              )
            }
            ListHeaderComponent={
              <View style={{ gap: 8, paddingTop: 12 }}>
                {!connected ? (
                  <Copy muted>
                    Disconnected. The last loaded tasks are shown; reconnect to
                    update them.
                  </Copy>
                ) : null}
                {state.loading ? (
                  <ActivityIndicator
                    accessibilityLabel="Loading tasks"
                    color={colors.accent}
                  />
                ) : null}
                <ErrorMessage message={error} />
                {error && state.rows ? (
                  <Copy muted>Showing the last list that loaded.</Copy>
                ) : null}
                {error ? (
                  <Action
                    disabled={state.loading || !connected}
                    onPress={refresh}
                  >
                    Try again
                  </Action>
                ) : null}
                {state.daemonGone ? (
                  <Copy muted>
                    This session’s daemon has exited. Showing the last available
                    list.
                  </Copy>
                ) : null}
                {state.unsupported ? (
                  <Copy muted>Tasks are not available for this session.</Copy>
                ) : null}
                {!state.loading &&
                !error &&
                !state.unsupported &&
                !state.daemonGone &&
                state.rows?.length === 0 ? (
                  <Copy muted>No tasks yet.</Copy>
                ) : null}
              </View>
            }
          />
        </SafeAreaView>
      </SafeAreaProvider>
    </Modal>
  );
}
