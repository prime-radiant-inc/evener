import {
  type ReactNode,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from "react";
import {
  ActivityIndicator,
  FlatList,
  Platform,
  Pressable,
  TextInput,
  View,
} from "react-native";
import { buildPickerRows } from "../../cmd/evener-hub/frontend/src/widgets/modelCatalog/pickerRows";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubModels } from "./hubModels";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchModelPicker({
  client,
  value,
  onChange,
  header,
}: {
  client: ConversationClientLike | null;
  value: string;
  onChange(value: string): void;
  header: ReactNode;
}) {
  const colors = useColors();
  const model = useMemo(() => new HubModels(client), [client]);
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const [query, setQuery] = useState("");
  useEffect(() => {
    void model.refresh();
    return () => model.dispose();
  }, [model]);
  const rows = useMemo(
    () => buildPickerRows(state.catalog, query),
    [state.catalog, query],
  );
  return (
    <FlatList
      style={styles.fill}
      contentContainerStyle={{ padding: 20, paddingBottom: 28 }}
      automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
      keyboardShouldPersistTaps="handled"
      data={rows}
      keyExtractor={(row) => row.key}
      ListHeaderComponent={
        <View style={{ gap: 12, paddingBottom: 12 }}>
          {header}
          {value ? <Copy>{`Selected · ${value}`}</Copy> : null}
          <TextInput
            accessibilityLabel="Search launch models"
            placeholder="Search models or providers"
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
          {!client && (
            <Copy>
              Reconnect to browse this hub's models. Your selection is kept.
            </Copy>
          )}
          <ErrorMessage message={state.error} />
          {state.loading && (
            <ActivityIndicator accessibilityLabel="Loading launch models" />
          )}
          {state.error && (
            <Action
              disabled={!client || state.loading}
              onPress={() => {
                void model.refresh();
              }}
            >
              Retry model list
            </Action>
          )}
        </View>
      }
      ListEmptyComponent={
        client && !state.loading && !state.error ? (
          <Copy muted>
            {query.trim()
              ? "No models match your search."
              : "No models are available from this hub."}
          </Copy>
        ) : null
      }
      renderItem={({ item }) => {
        if (item.kind === "group")
          return (
            <View style={{ paddingTop: 16, paddingBottom: 6 }}>
              <Copy muted>{item.label}</Copy>
            </View>
          );
        if (item.kind === "unavailable")
          return (
            <View style={{ paddingVertical: 8 }}>
              <Copy muted>{item.text}</Copy>
            </View>
          );
        const selected = value === item.option.qualified;
        return (
          <Pressable
            accessibilityRole="radio"
            accessibilityState={{
              checked: selected,
              disabled: !client || state.loading,
            }}
            accessibilityLabel={`Choose ${item.option.label} · ${item.option.entry.provider}`}
            disabled={!client || state.loading}
            onPress={() => onChange(item.option.qualified)}
            style={{
              minHeight: 48,
              paddingVertical: 10,
              borderBottomWidth: 0.5,
              borderColor: colors.border,
            }}
          >
            <Copy>{`${selected ? "✓ " : ""}${item.option.label}`}</Copy>
            {item.meta ? <Copy muted>{item.meta}</Copy> : null}
          </Pressable>
        );
      }}
    />
  );
}
