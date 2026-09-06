import { useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from "react";
import { ActivityIndicator, FlatList, Pressable, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type {
  NavigationProjectSummary,
  NavigationSessionSummary,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { useConnection } from "./ConnectionProvider";
import { NavigationPages } from "./navigationPages";
import { navigationTree } from "./navigationTree";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

function FilterTab({
  label,
  selected,
  onPress,
}: {
  label: string;
  selected: boolean;
  onPress: () => void;
}) {
  const colors = useColors();
  return (
    <Pressable
      accessibilityRole="tab"
      accessibilityLabel={label}
      accessibilityState={{ selected }}
      onPress={onPress}
      style={{
        minHeight: 48,
        paddingHorizontal: 12,
        paddingVertical: 10,
        borderRadius: 12,
        backgroundColor: selected ? colors.surface : "transparent",
        borderBottomWidth: selected ? 2 : 0,
        borderColor: colors.accent,
      }}
    >
      <Copy>{label}</Copy>
    </Pressable>
  );
}

function PageList<T>({
  pages,
  ready,
  rowKey,
  title,
  detail,
  open,
  empty,
  childRows,
  omitted,
}: {
  pages: NavigationPages<T>;
  ready: boolean;
  rowKey: (row: T) => string;
  title: (row: T) => string;
  detail: (row: T) => string;
  open: (row: T) => void;
  empty: string;
  childRows?: (row: T) => readonly T[];
  omitted?: (row: T) => number;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(pages.subscribe, pages.getSnapshot);
  const [expansion, setExpansion] = useState({
    owner: pages,
    keys: new Set<string>(),
  });
  const expanded =
    expansion.owner === pages ? expansion.keys : new Set<string>();
  const rows = navigationTree(state.rows, rowKey, childRows, expanded);
  function toggle(row: T) {
    const keys = new Set(expanded);
    const key = rowKey(row);
    if (keys.has(key)) keys.delete(key);
    else keys.add(key);
    setExpansion({ owner: pages, keys });
  }
  useEffect(() => pages.watch(), [pages]);
  useFocusEffect(
    useCallback(() => {
      if (ready && !pages.getSnapshot().loaded) void pages.refresh();
      return () => pages.cancel();
    }, [pages, ready]),
  );
  return (
    <>
      {state.error ? (
        <View style={{ paddingHorizontal: 16, paddingTop: 8 }}>
          <ErrorMessage message={state.stale ? null : state.error} />
          {state.stale ? (
            <Copy muted>
              Updates are available. Refresh to see the latest list.
            </Copy>
          ) : null}
          {state.error ? (
            <Action
              disabled={!ready || state.loading}
              onPress={() => {
                void pages.refresh();
              }}
            >
              Refresh list
            </Action>
          ) : null}
        </View>
      ) : null}
      <FlatList
        data={rows}
        keyExtractor={({ item }) => rowKey(item)}
        refreshing={state.loading}
        onRefresh={() => {
          if (ready) void pages.refresh();
        }}
        contentContainerStyle={styles.padded}
        ListEmptyComponent={
          state.loading ? (
            <ActivityIndicator accessibilityLabel="Loading list" />
          ) : (
            <Copy muted>
              {ready ? empty : "Connect to this hub to load the list."}
            </Copy>
          )
        }
        ListFooterComponent={
          <View style={{ gap: 8 }}>
            {state.truncated ? (
              <Copy muted>
                The hub returned a partial session tree. Some related sessions
                may be missing.
              </Copy>
            ) : null}
            {state.remaining > 0 ? (
              <Action
                disabled={!ready || state.loading || state.stale}
                onPress={() => {
                  void pages.more();
                }}
              >
                {state.loading
                  ? "Loading…"
                  : `Load more · ${state.remaining} remaining`}
              </Action>
            ) : null}
          </View>
        }
        renderItem={({ item: { item, depth } }) => (
          <View
            style={{
              paddingLeft: Math.min(depth, 2) * 12,
              borderBottomWidth: 0.5,
              borderColor: colors.border,
            }}
          >
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Open ${title(item)}`}
              disabled={!ready}
              onPress={() => open(item)}
              style={{ paddingVertical: 13, minHeight: 68, gap: 4 }}
            >
              <Copy>{title(item)}</Copy>
              <Copy muted>{detail(item)}</Copy>
            </Pressable>
            {childRows?.(item).length ? (
              <Action
                tone="quiet"
                label={`${expanded.has(rowKey(item)) ? "Hide" : "Show"} related sessions for ${title(item)}`}
                expanded={expanded.has(rowKey(item))}
                onPress={() => toggle(item)}
              >{`${expanded.has(rowKey(item)) ? "▾" : "▸"} ${childRows(item).length} related session${childRows(item).length === 1 ? "" : "s"}`}</Action>
            ) : null}
            {(omitted?.(item) ?? 0) > 0 ? (
              <Copy
                muted
              >{`${omitted?.(item)} related sessions were omitted by the hub.`}</Copy>
            ) : null}
          </View>
        )}
      />
    </>
  );
}
const projectKey = (row: NavigationProjectSummary) => row.key;
const sessionRef = (row: NavigationSessionSummary) => row.ref;

export function ProjectsScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "Projects">) {
  const { client, activeProfile, state } = useConnection();
  const colors = useColors();
  const [archived, setArchived] = useState(false);
  const belongs = activeProfile?.id === route.params.hubId;
  const pages = useMemo(
    () =>
      client && belongs
        ? new NavigationPages<NavigationProjectSummary>(
            client,
            {
              resource: "catalog",
              catalog: archived ? "archived_projects" : "projects",
            },
            "projects",
            projectKey,
          )
        : null,
    [client, belongs, archived],
  );
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <View style={{ paddingHorizontal: 20, gap: 8 }}>
        <Copy muted>{belongs ? activeProfile?.name : "Disconnected hub"}</Copy>
        <View style={[styles.row, { flexWrap: "wrap" }]}>
          <FilterTab
            label="Projects"
            selected={!archived}
            onPress={() => setArchived(false)}
          />
          <FilterTab
            label="Archived projects"
            selected={archived}
            onPress={() => setArchived(true)}
          />
        </View>
      </View>
      {pages ? (
        <PageList
          pages={pages}
          ready={state === "ready"}
          rowKey={projectKey}
          title={(row) => row.name || row.working_dir || "Untitled project"}
          detail={(row) =>
            `${row.session_count} sessions${row.working_dir ? ` · ${row.working_dir}` : ""}`
          }
          empty={
            archived ? "No archived projects." : "No projects on this hub yet."
          }
          open={(row) =>
            navigation.navigate("Project", {
              hubId: route.params.hubId,
              projectKey: row.key,
              title: row.name,
            })
          }
        />
      ) : (
        <Copy muted>Connect to this hub to browse projects.</Copy>
      )}
    </SafeAreaView>
  );
}
export function ProjectScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "Project">) {
  const { client, activeProfile, state } = useConnection();
  const colors = useColors();
  const [tier, setTier] = useState<"current" | "recent" | "archived">(
    "current",
  );
  const belongs = activeProfile?.id === route.params.hubId;
  const pages = useMemo(
    () =>
      client && belongs
        ? new NavigationPages<NavigationSessionSummary>(
            client,
            {
              resource: "project_page",
              projectKey: route.params.projectKey,
              tier,
            },
            "sessions",
            sessionRef,
          )
        : null,
    [client, belongs, route.params.projectKey, tier],
  );
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <View style={{ paddingHorizontal: 20, gap: 8 }}>
        <Copy muted>{belongs ? activeProfile?.name : "Disconnected hub"}</Copy>
        <View style={[styles.row, { flexWrap: "wrap" }]}>
          {(["current", "recent", "archived"] as const).map((value) => (
            <FilterTab
              key={value}
              selected={tier === value}
              onPress={() => setTier(value)}
              label={
                value === "current"
                  ? "Current"
                  : value === "recent"
                    ? "Recent"
                    : "Archived"
              }
            />
          ))}
        </View>
      </View>
      {pages ? (
        <PageList
          pages={pages}
          ready={state === "ready"}
          rowKey={sessionRef}
          childRows={(row) => row.children ?? []}
          omitted={(row) =>
            (row.omitted_descendants ?? 0) + (row.more_subagents ?? 0)
          }
          title={(row) => row.title || "Untitled session"}
          detail={(row) =>
            `${row.ask_pending || row.state === "awaiting" ? "Needs you" : row.state}${row.branch ? ` · ${row.branch}` : ""}`
          }
          empty={`No ${tier} sessions in this project.`}
          open={(row) =>
            navigation.navigate("Conversation", {
              hubId: route.params.hubId,
              ref: row.ref,
              title: row.title,
            })
          }
        />
      ) : (
        <Copy muted>Connect to this hub to browse sessions.</Copy>
      )}
    </SafeAreaView>
  );
}
