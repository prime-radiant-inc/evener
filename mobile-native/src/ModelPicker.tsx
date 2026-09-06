import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { ActivityIndicator, FlatList, TextInput, View } from "react-native";
import type { ModelDescriptor } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { SessionControls } from "./sessionControls";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ModelPicker({
  controls,
  currentModel,
  ready,
  done,
}: {
  controls: SessionControls;
  currentModel: string;
  ready: boolean;
  done: () => void;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
  const [search, setSearch] = useState("");
  const [showDiagnostics, setShowDiagnostics] = useState(false);
  const [selected, setSelected] = useState<ModelDescriptor | null>(null);
  useEffect(() => {
    setSelected(null);
    void controls.loadModels();
  }, [controls]);
  const models = useMemo(() => {
    const query = search.trim().toLowerCase();
    return (state.catalog?.data ?? []).filter((model) =>
      `${model.displayName ?? ""} ${model.provider} ${model.model}`
        .toLowerCase()
        .includes(query),
    );
  }, [state.catalog, search]);
  const disabled = !ready || state.pending !== null || state.loadingModels;
  const available =
    selected &&
    state.catalog?.data.some(
      (entry) =>
        entry.provider === selected.provider && entry.model === selected.model,
    );
  return (
    <View
      style={[styles.fill, { paddingHorizontal: 20, gap: 12, paddingTop: 16 }]}
    >
      <Copy muted>{`Current · ${currentModel}`}</Copy>
      <TextInput
        accessibilityLabel="Search models"
        value={search}
        onChangeText={setSearch}
        placeholder="Search models or providers"
        placeholderTextColor={colors.secondary}
        autoCapitalize="none"
        autoCorrect={false}
        style={[
          styles.input,
          {
            color: colors.text,
            borderColor: colors.border,
            backgroundColor: colors.surface,
          },
        ]}
      />
      <ErrorMessage message={state.modelError} />
      <ErrorMessage
        message={state.lastAction === "changeModel" ? state.error : null}
      />
      {state.loadingModels ? (
        <ActivityIndicator
          accessibilityLabel="Loading models"
          color={colors.accent}
        />
      ) : null}
      {state.modelError ? (
        <Action
          disabled={disabled}
          onPress={() => {
            void controls.loadModels();
          }}
        >
          Retry model list
        </Action>
      ) : null}
      <FlatList
        style={styles.fill}
        data={models}
        keyboardShouldPersistTaps="handled"
        keyExtractor={(model) => JSON.stringify([model.provider, model.model])}
        ListHeaderComponent={
          state.catalog?.diagnostics?.length ? (
            <View style={{ gap: 8, paddingBottom: 12 }}>
              <Action
                tone="quiet"
                expanded={showDiagnostics}
                onPress={() => setShowDiagnostics((shown) => !shown)}
              >
                {`Catalog notices (${state.catalog.diagnostics.length})`}
              </Action>
              {showDiagnostics &&
                state.catalog.diagnostics.map((diagnostic) => (
                  <Copy muted key={JSON.stringify(diagnostic)}>
                    {[diagnostic.provider, diagnostic.message, diagnostic.hint]
                      .filter(Boolean)
                      .join(" · ")}
                  </Copy>
                ))}
            </View>
          ) : null
        }
        ListEmptyComponent={
          !state.loadingModels && !state.modelError ? (
            <Copy muted>
              {search.trim()
                ? "No models match your search."
                : "No models are available for this session."}
            </Copy>
          ) : null
        }
        renderItem={({ item }) => (
          <Choice
            label={`${item.displayName || item.model} · ${item.provider}`}
            selected={
              selected?.provider === item.provider &&
              selected.model === item.model
            }
            disabled={disabled}
            onPress={() => setSelected(item)}
          />
        )}
      />
      <View
        style={{
          borderTopWidth: 1,
          borderColor: colors.border,
          paddingVertical: 12,
          gap: 8,
        }}
      >
        {selected ? (
          <Copy
            muted
          >{`Selected · ${selected.provider}/${selected.model}`}</Copy>
        ) : null}
        {state.pending === "changeModel" ? (
          <ActivityIndicator
            accessibilityLabel="Changing model"
            color={colors.accent}
          />
        ) : null}
        <Action
          disabled={disabled || !available}
          onPress={() => {
            if (selected)
              void controls
                .changeModel(selected.provider, selected.model)
                .then((changed) => {
                  if (changed) done();
                });
          }}
        >
          Use model
        </Action>
      </View>
    </View>
  );
}
