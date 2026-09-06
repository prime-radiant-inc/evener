import { useEffect, useMemo, useSyncExternalStore } from "react";
import { ActivityIndicator, FlatList, Pressable, View } from "react-native";
import {
  filterSlashMenuItems,
  type SlashMenuItem,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/slashCompletion";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { CommandCatalog } from "./commandCatalog";
import { builtinComposerItems } from "./composerCommand";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function CommandCompletion({
  client,
  maxHeight,
  sessionRef,
  capabilities,
  query,
  choose,
  close,
}: {
  client: ConversationClientLike;
  maxHeight: number;
  sessionRef: string;
  capabilities: MobileConversation["capabilities"];
  query: string;
  choose: (item: SlashMenuItem) => void;
  close: () => void;
}) {
  const colors = useColors();
  const catalog = useMemo(
    () => new CommandCatalog(client, sessionRef),
    [client, sessionRef],
  );
  const state = useSyncExternalStore(catalog.subscribe, catalog.getSnapshot);
  useEffect(() => {
    catalog.start();
    return () => catalog.dispose();
  }, [catalog]);
  const items = filterSlashMenuItems(
    [...builtinComposerItems(capabilities), ...state.items],
    query,
  );
  if (!state.loading && !state.error && items.length === 0) return null;
  return (
    <View
      style={{
        borderBottomWidth: 0.5,
        borderColor: colors.border,
        paddingBottom: 4,
        maxHeight,
        flexShrink: 0,
      }}
    >
      <FlatList
        data={items}
        style={{ maxHeight, flexShrink: 1 }}
        keyboardShouldPersistTaps="always"
        ListHeaderComponent={
          <>
            <View style={styles.row}>
              <View style={styles.fill}>
                <Copy muted numberOfLines={1}>
                  Commands and skills
                </Copy>
              </View>
              <Action onPress={close}>Dismiss</Action>
            </View>
            {state.loading ? (
              <ActivityIndicator accessibilityLabel="Loading commands and skills" />
            ) : null}
            <ErrorMessage message={state.error} />
            {state.error ? (
              <Action
                disabled={state.loading}
                onPress={() => void catalog.refresh()}
              >
                Retry commands
              </Action>
            ) : null}
          </>
        }
        keyExtractor={(item) => item.key}
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`Insert ${item.invocation}`}
            accessibilityHint={item.hint}
            disabled={
              item.kind !== "builtin" && (state.loading || !!state.error)
            }
            onPress={() => {
              if (
                item.kind === "builtin" ||
                (!catalog.getSnapshot().loading && !catalog.getSnapshot().error)
              )
                choose(item);
            }}
            style={{ minHeight: 48, paddingVertical: 6, gap: 2 }}
          >
            <Copy>{item.invocation}</Copy>
            <Copy muted numberOfLines={2}>
              {item.hint || (item.kind === "skill" ? "Skill" : "Command")}
            </Copy>
          </Pressable>
        )}
      />
    </View>
  );
}
