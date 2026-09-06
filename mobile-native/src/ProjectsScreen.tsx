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
  Pressable,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type {
  ArchiveParams,
  NavigationProjectSummary,
  NavigationSessionSummary,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { useConnection } from "./ConnectionProvider";
import { NavigationActions } from "./navigationActions";
import { NavigationPages } from "./navigationPages";
import { revealNavigationRow } from "./navigationReveal";
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
  organization,
  revealRef,
}: {
  revealRef?: string;
  pages: NavigationPages<T>;
  ready: boolean;
  rowKey: (row: T) => string;
  title: (row: T) => string;
  detail: (row: T) => string;
  open: (row: T) => void;
  empty: string;
  childRows?: (row: T) => readonly T[];
  omitted?: (row: T) => number;
  organization: (
    row: T,
    depth: number,
  ) => {
    target: Omit<ArchiveParams, "archived">;
    archived: boolean;
    favorite?: boolean;
  } | null;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(pages.subscribe, pages.getSnapshot);
  const { client, activeProfile } = useConnection();
  const focused = useIsFocused();
  const binding = useMemo(
    () => ({ client, pages, ready, focused }),
    [client, pages, ready, focused],
  );
  const current = useRef(binding);
  current.current = binding;
  const actions = useMemo(
    () =>
      client && ready && focused
        ? new NavigationActions(
            client,
            (receipt) => pages.refreshAfter(receipt),
            () => current.current === binding,
          )
        : null,
    [client, pages, ready, focused, binding],
  );
  useEffect(() => () => actions?.dispose(), [actions]);

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
  const list = useRef<FlatList<{ item: T; depth: number }>>(null);
  const access = useRef({ rowKey, childRows });
  access.current = { rowKey, childRows };
  const [revealError, setRevealError] = useState<string | null>(null);
  const [revealRequest, setRevealRequest] = useState(0);
  const revealEpoch = useRef(revealRequest);
  revealEpoch.current = revealRequest;
  function refreshList() {
    if (revealRef) setRevealRequest((value) => value + 1);
    else void pages.refresh();
  }
  const [revealed, setRevealed] = useState<NavigationPages<T> | null>(null);
  const scrollAttempt = useRef(0);
  const scrollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (scrollTimer.current) clearTimeout(scrollTimer.current);
    },
    [],
  );
  useFocusEffect(
    useCallback(() => {
      let active = true;
      if (ready && revealRef) {
        setRevealError(null);
        setRevealed(null);
        void revealNavigationRow(
          pages,
          revealRef,
          access.current.rowKey,
          access.current.childRows,
          () => active && revealEpoch.current === revealRequest,
        )
          .then((path) => {
            if (!active || !path) return;
            setExpansion({ owner: pages, keys: new Set(path.slice(0, -1)) });
            scrollAttempt.current = 0;
            setRevealed(pages);
          })
          .catch((error) => {
            if (active)
              setRevealError(
                error instanceof Error
                  ? error.message
                  : "Could not locate this session.",
              );
          });
      } else if (ready && !pages.getSnapshot().loaded) void pages.refresh();
      return () => {
        active = false;
        if (scrollTimer.current) clearTimeout(scrollTimer.current);
        pages.cancel();
      };
    }, [pages, ready, revealRef, revealRequest]),
  );
  const targetIndex = revealRef
    ? rows.findIndex((row) => rowKey(row.item) === revealRef)
    : -1;
  useEffect(() => {
    if (revealed === pages && targetIndex >= 0)
      list.current?.scrollToIndex({
        index: targetIndex,
        animated: false,
        viewPosition: 0.3,
      });
  }, [revealed, pages, targetIndex]);
  return (
    <>
      {revealError ? (
        <View style={{ paddingHorizontal: 16, gap: 8 }}>
          <ErrorMessage message={revealError} />
          <Action disabled={!ready || state.loading} onPress={refreshList}>
            Locate again
          </Action>
        </View>
      ) : null}
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
                refreshList();
              }}
            >
              Refresh list
            </Action>
          ) : null}
        </View>
      ) : null}
      {actions ? <OrganizationStatus actions={actions} /> : null}
      <FlatList
        ref={list}
        onScrollToIndexFailed={({ index, averageItemLength }) => {
          list.current?.scrollToOffset({
            offset: averageItemLength * index,
            animated: false,
          });
          if (scrollAttempt.current++ < 3) {
            if (scrollTimer.current) clearTimeout(scrollTimer.current);
            scrollTimer.current = setTimeout(
              () =>
                list.current?.scrollToIndex({
                  index,
                  animated: false,
                  viewPosition: 0.3,
                }),
              100,
            );
          }
        }}
        data={rows}
        keyExtractor={({ item }) => rowKey(item)}
        refreshing={state.loading}
        onRefresh={() => {
          if (ready) refreshList();
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
              backgroundColor:
                rowKey(item) === revealRef ? colors.surface : "transparent",
              paddingLeft: Math.min(depth, 2) * 12,
              borderBottomWidth: 0.5,
              borderColor: colors.border,
            }}
          >
            <View style={styles.row}>
              <Pressable
                accessibilityRole="button"
                accessibilityLabel={`Open ${title(item)}`}
                accessibilityState={{ selected: rowKey(item) === revealRef }}
                disabled={!ready}
                onPress={() => open(item)}
                style={{ flex: 1, paddingVertical: 13, minHeight: 68, gap: 4 }}
              >
                <Copy>{title(item)}</Copy>
                <Copy muted>{detail(item)}</Copy>
              </Pressable>
              {actions && organization(item, depth) ? (
                <Action
                  tone="quiet"
                  label={`More actions for ${title(item)}`}
                  disabled={!ready || state.loading || state.stale}
                  onPress={() => {
                    const value = organization(item, depth);
                    if (!value || actions.getSnapshot().pending) return;
                    const invoke = (operation: () => void) => {
                      if (!pages.getSnapshot().stale) operation();
                    };
                    Alert.alert(
                      title(item),
                      `${activeProfile?.name ?? "Hub"} · Organize without deleting history or stopping work.`,
                      [
                        ...(value.favorite === undefined
                          ? []
                          : [
                              {
                                text: value.favorite
                                  ? "Remove from pinned"
                                  : "Add to pinned",
                                onPress: () =>
                                  invoke(() => {
                                    void actions.favorite(
                                      value.target.id,
                                      !value.favorite,
                                    );
                                  }),
                              },
                            ]),
                        {
                          text: value.archived ? "Unarchive" : "Archive",
                          onPress: () =>
                            invoke(() => {
                              void actions.archive(
                                value.target,
                                !value.archived,
                              );
                            }),
                        },
                        { text: "Cancel", style: "cancel" },
                      ],
                    );
                  }}
                >
                  ···
                </Action>
              ) : null}
            </View>
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
function OrganizationStatus({ actions }: { actions: NavigationActions }) {
  const state = useSyncExternalStore(actions.subscribe, actions.getSnapshot);
  if (!state.pending && !state.error) return null;
  return (
    <View style={{ paddingHorizontal: 16, paddingVertical: 8 }}>
      {state.pending ? (
        <ActivityIndicator accessibilityLabel="Updating organization" />
      ) : null}
      <ErrorMessage message={state.error} />
    </View>
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
          organization={(row) =>
            row.key === "no-project"
              ? null
              : {
                  target: {
                    kind: "project",
                    id: row.key,
                    workingDir: row.working_dir,
                  },
                  archived: row.is_archived ?? archived,
                  favorite: row.favorite ?? false,
                }
          }
          title={(row) => row.name || row.working_dir || "Untitled project"}
          detail={(row) =>
            `${row.favorite ? "Pinned · " : ""}${row.session_count} sessions${row.working_dir ? ` · ${row.working_dir}` : ""}`
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
          organization={(row, depth) =>
            depth > 0 || ["subagent", "fork", "cluster"].includes(row.kind)
              ? null
              : {
                  target: { kind: "session", id: row.session_id },
                  archived: tier === "archived",
                }
          }
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

const sessionChildren = (row: NavigationSessionSummary) => row.children ?? [];
export function SessionLocationScreen({
  route,
  navigation,
}: NativeStackScreenProps<Routes, "SessionLocation">) {
  const { client, activeProfile, state } = useConnection();
  const colors = useColors();
  const belongs = activeProfile?.id === route.params.hubId;
  const { location } = route.params;
  const pages = useMemo(
    () =>
      client && belongs
        ? new NavigationPages<NavigationSessionSummary>(
            client,
            location.params,
            "sessions",
            sessionRef,
          )
        : null,
    [client, belongs, location],
  );
  return (
    <SafeAreaView
      edges={["bottom", "left", "right"]}
      style={[styles.fill, { backgroundColor: colors.background }]}
    >
      <View style={{ paddingHorizontal: 20, gap: 8 }}>
        <Copy muted>
          {belongs ? activeProfile?.name : "Disconnected hub"}
          {location.params.tier ? ` · ${location.params.tier}` : ""}
        </Copy>
      </View>
      {pages ? (
        <PageList
          pages={pages}
          ready={state === "ready"}
          revealRef={location.ref}
          rowKey={sessionRef}
          childRows={sessionChildren}
          omitted={(row) =>
            (row.omitted_descendants ?? 0) + (row.more_subagents ?? 0)
          }
          title={(row) => row.title || "Untitled session"}
          detail={(row) =>
            row.ask_pending || row.state === "awaiting"
              ? "Needs you"
              : row.state
          }
          empty="No sessions in this location."
          organization={() => null}
          open={(row) =>
            navigation.navigate("Conversation", {
              hubId: route.params.hubId,
              ref: row.ref,
              title: row.title,
            })
          }
        />
      ) : (
        <Copy muted>Connect to this hub to locate the session.</Copy>
      )}
    </SafeAreaView>
  );
}
