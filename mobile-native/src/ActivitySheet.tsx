import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  ActivityIndicator,
  FlatList,
  Modal,
  Platform,
  Pressable,
  Text,
  View,
} from "react-native";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import type { ActivityTree } from "../../cmd/evener-hub/frontend/src/panes/session/chrome/activityData";
import {
  type ActivityRow,
  buildActivityRows,
} from "../../cmd/evener-hub/frontend/src/panes/session/chrome/activityRows";
import { stableDelegateDisplayStatus } from "../../cmd/evener-hub/frontend/src/protocol/stableDelegate";
import { parseAnsiLines } from "../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ActivityList } from "./activityList";
import { JobOutput } from "./jobOutput";
import { MarkdownResponse } from "./MarkdownResponse";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

function Output({
  client,
  ownerRef,
  jobId,
  connected,
}: {
  client: ConversationClientLike;
  ownerRef: string;
  jobId: string;
  connected: boolean;
}) {
  const colors = useColors();
  const retained = useRef<ReturnType<JobOutput["getSnapshot"]> | undefined>(
    undefined,
  );
  const log = useMemo(
    () => new JobOutput(client, ownerRef, jobId, retained.current),
    [client, ownerRef, jobId],
  );
  const state = useSyncExternalStore(log.subscribe, log.getSnapshot);
  useEffect(() => {
    retained.current = state;
  }, [state]);
  useEffect(() => () => log.dispose(), [log]);
  useEffect(() => {
    if (connected) void log.refresh();
  }, [connected, log]);
  const lines = useMemo(
    () => parseAnsiLines(state.content ?? ""),
    [state.content],
  );
  return (
    <FlatList
      data={lines}
      contentContainerStyle={{ padding: 20, gap: 2 }}
      renderItem={({ item }) => (
        <Text
          selectable
          style={{
            color: colors.text,
            fontFamily: Platform.OS === "ios" ? "Menlo" : "monospace",
            fontSize: 13,
          }}
        >
          {item
            .filter((run) => !run.hidden)
            .map((run) => run.text)
            .join("")}
        </Text>
      )}
      ListHeaderComponent={
        <View style={{ gap: 8, paddingBottom: 16 }}>
          <Action
            disabled={!connected || state.loading}
            onPress={() => void log.refresh()}
          >
            Refresh output
          </Action>
          {!connected ? (
            <Copy muted>Disconnected. Showing the last loaded output.</Copy>
          ) : null}
          <ErrorMessage message={state.error} />
          {state.loading ? (
            <ActivityIndicator accessibilityLabel="Loading output" />
          ) : null}
          {state.content !== null ? (
            <Copy
              muted
            >{`${state.totalBytes} bytes total${state.earliestStart > 0 ? ` · output starts at byte ${state.earliestStart}` : ""}`}</Copy>
          ) : null}
          {state.hasEarlier ? (
            <Action
              disabled={!connected || state.loading}
              onPress={() => void log.loadEarlier()}
            >
              Load earlier output
            </Action>
          ) : null}
          {state.content === "" ? <Copy muted>No output yet.</Copy> : null}
        </View>
      }
    />
  );
}

export function ActivitySheet({
  client,
  sessionRef,
  threadId,
  connected,
  hubName,
  close,
  openSession,
}: {
  client: ConversationClientLike;
  sessionRef: string;
  threadId: string;
  connected: boolean;
  hubName: string;
  close: () => void;
  openSession: (ref: string, title: string) => void;
}) {
  const colors = useColors();
  const retained = useRef<ActivityTree | null>(null);
  const list = useMemo(
    () => new ActivityList(client, sessionRef, threadId, retained.current),
    [client, sessionRef, threadId],
  );
  const state = useSyncExternalStore(list.subscribe, list.getSnapshot);
  useEffect(() => {
    retained.current = state.tree;
  }, [state.tree]);
  useEffect(() => {
    list.start();
    return () => list.dispose();
  }, [list]);
  const wasConnected = useRef(connected);
  useEffect(() => {
    if (connected && !wasConnected.current) void list.refresh();
    wasConnected.current = connected;
  }, [connected, list]);
  const [folds, setFolds] = useState(new Set<string>());
  const [details, setDetails] = useState<Record<string, boolean>>({});
  const [output, setOutput] = useState<{
    ownerRef: string;
    jobId: string;
  } | null>(null);
  const rows = state.tree ? buildActivityRows(state.tree, folds) : [];
  function renderRow(row: ActivityRow) {
    const indent = Math.min(row.level - 1, 4) * 12;
    if (row.kind === "fold")
      return (
        <View style={{ paddingLeft: indent }}>
          <Action
            expanded={folds.has(row.id)}
            onPress={() =>
              setFolds((current) => {
                const next = new Set(current);
                if (next.has(row.id)) next.delete(row.id);
                else next.add(row.id);
                return next;
              })
            }
          >{`${row.inactiveCount} inactive${row.failedCount ? ` · ${row.failedCount} failed` : ""}`}</Action>
        </View>
      );
    const open = details[row.id] ?? row.defaultDetailOpen;
    const title =
      row.kind === "job"
        ? row.job.description
        : (row.delegate.description ??
          row.delegate.child?.label ??
          row.delegate.childRef);
    const status =
      row.kind === "job"
        ? row.job.status
        : (stableDelegateDisplayStatus(row.delegate) ??
          row.delegate.child?.aggregate ??
          "unknown");
    const detail =
      row.kind === "job"
        ? (row.job.command ?? row.job.task)
        : (row.delegate.mandate ?? row.delegate.task);
    const target = row.transcriptRef?.trim();
    return (
      <View
        style={{
          marginLeft: indent,
          paddingVertical: 8,
          borderBottomWidth: 0.5,
          borderColor: colors.border,
        }}
      >
        <Pressable
          accessibilityRole="button"
          accessibilityLabel={`${title}, ${status}`}
          accessibilityState={{ expanded: open }}
          onPress={() =>
            setDetails((current) => ({ ...current, [row.id]: !open }))
          }
          style={{ minHeight: 48, justifyContent: "center", gap: 4 }}
        >
          <Copy>{title}</Copy>
          <Copy
            muted
          >{`${row.kind === "job" ? "Job" : "Session"} · ${status}`}</Copy>
        </Pressable>
        {open ? (
          <View style={{ gap: 8, paddingVertical: 8 }}>
            {detail ? (
              row.kind === "delegate" ? (
                <MarkdownResponse markdown={detail} />
              ) : (
                <Copy>{detail}</Copy>
              )
            ) : null}
            {row.kind === "job" ? (
              <Copy
                muted
              >{`${row.job.outputBytes} bytes${row.job.exitCode === undefined ? "" : ` · exit ${row.job.exitCode}`}`}</Copy>
            ) : null}
            {row.kind === "job" && row.job.reason ? (
              <Copy>{row.job.reason}</Copy>
            ) : null}
            {row.kind === "delegate" && row.delegate.reason ? (
              <Copy>{row.delegate.reason}</Copy>
            ) : null}
            {target ? (
              <Action
                onPress={() => {
                  if (target.startsWith("job:"))
                    setOutput({
                      ownerRef:
                        row.kind === "job" ? row.job.ownerRef : row.parentRef,
                      jobId: target.slice(4),
                    });
                  else openSession(target, title);
                }}
              >
                {target.startsWith("job:") ? "Open output" : "Open session"}
              </Action>
            ) : null}
          </View>
        ) : null}
      </View>
    );
  }
  return (
    <Modal
      animationType="slide"
      presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
      onRequestClose={() => (output ? setOutput(null) : close())}
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
            {output ? (
              <Action onPress={() => setOutput(null)}>Back</Action>
            ) : null}
            <View style={styles.fill}>
              <Copy>{output ? "Job output" : "Activity"}</Copy>
              <Copy muted>{hubName}</Copy>
            </View>
            <Action onPress={close}>Done</Action>
          </View>
          {output ? (
            <Output
              key={`${output.ownerRef}:${output.jobId}`}
              client={client}
              ownerRef={output.ownerRef}
              jobId={output.jobId}
              connected={connected}
            />
          ) : (
            <FlatList
              data={rows}
              keyExtractor={(row) => row.id}
              renderItem={({ item }) => renderRow(item)}
              extraData={{ folds, details }}
              contentContainerStyle={{
                paddingHorizontal: 20,
                paddingBottom: 24,
              }}
              ListHeaderComponent={
                <View style={{ gap: 8, paddingTop: 12 }}>
                  <Action
                    disabled={!connected || state.loading}
                    onPress={() => void list.refresh()}
                  >
                    Refresh activity
                  </Action>
                  {!connected ? (
                    <Copy muted>
                      Disconnected. Showing the last loaded activity.
                    </Copy>
                  ) : null}
                  {state.loading ? (
                    <ActivityIndicator accessibilityLabel="Loading activity" />
                  ) : null}
                  <ErrorMessage message={state.error} />
                  {state.error && state.tree ? (
                    <Copy muted>Showing the last activity that loaded.</Copy>
                  ) : null}
                  {state.unsupported ? (
                    <Copy muted>
                      Activity is not available for this session.
                    </Copy>
                  ) : null}
                  {state.ended ? (
                    <Copy muted>
                      This session’s daemon has exited. Showing the last
                      available activity.
                    </Copy>
                  ) : null}
                  {!state.loading &&
                  !state.error &&
                  !state.unsupported &&
                  !state.ended &&
                  rows.length === 0 ? (
                    <Copy muted>No activity yet.</Copy>
                  ) : null}
                </View>
              }
              ListFooterComponent={
                <View style={{ gap: 12, paddingTop: 12 }}>
                  {list.branches().map((branch) => (
                    <View key={branch.id}>
                      <Copy
                        muted
                      >{`${branch.label}: ${branch.error ?? "Some activity is not loaded."}`}</Copy>
                      {branch.continuation ? (
                        <Action
                          disabled={!connected || state.loading}
                          onPress={() => {
                            if (branch.continuation)
                              void list.loadMore(
                                branch.id,
                                branch.continuation,
                              );
                          }}
                        >
                          Load more activity
                        </Action>
                      ) : null}
                    </View>
                  ))}
                </View>
              }
            />
          )}
        </SafeAreaView>
      </SafeAreaProvider>
    </Modal>
  );
}
