import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { ActivityIndicator, FlatList, TextInput, View } from "react-native";
import type { ModelDescriptor } from "../../appwire-client/typescript/types.gen";
import { modelPickerEntries } from "./modelPickerEntries";
import type { SessionControls } from "./sessionControls";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ModelPicker({
  controls,
  currentModel,
  ready,
  done,
  setting = "model",
}: {
  controls: SessionControls;
  currentModel: string;
  ready: boolean;
  done: () => void;
  setting?: "model" | "vision";
}) {
  const colors = useColors();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
  const [search, setSearch] = useState("");
  const [showDiagnostics, setShowDiagnostics] = useState(false);
  const [selected, setSelected] = useState<ModelDescriptor | null>(null);
  useEffect(() => {
    setSelected(null);
    void controls.loadModels();
  }, [controls, setting]);
  const models = useMemo(() => {
    const query = search.trim().toLowerCase();
    return (state.catalog?.data ?? []).filter(
      (model) =>
        (setting !== "vision" || model.supportsVision !== false) &&
        `${model.displayName ?? ""} ${model.provider} ${model.model}`
          .toLowerCase()
          .includes(query),
    );
  }, [state.catalog, search, setting]);
  const disabled = !ready || state.pending !== null || state.loadingModels;
  const entries = useMemo(() => modelPickerEntries(models), [models]);
  const available =
    selected &&
    state.catalog?.data.some(
      (entry) =>
        entry.provider === selected.provider &&
        entry.model === selected.model &&
        (setting !== "vision" || entry.supportsVision !== false),
    );
  return (
    <View
      style={[styles.fill, { paddingHorizontal: 20, gap: 12, paddingTop: 16 }]}
    >
      <Copy
        muted
      >{`${setting === "vision" ? "Vision model" : "Current"} · ${currentModel || (setting === "vision" ? "Session model" : "")}`}</Copy>
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
        message={
          state.lastAction === "changeModel" ||
          state.lastAction === "setVisionModel"
            ? state.error
            : null
        }
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
        data={entries}
        keyboardShouldPersistTaps="handled"
        keyExtractor={(entry) =>
          JSON.stringify([entry.model.provider, entry.model.model])
        }
        ListHeaderComponent={
          setting === "vision" ? (
            <View style={{ gap: 8, paddingBottom: 12 }}>
              <Choice
                label="Use session model"
                selected={currentModel === ""}
                disabled={disabled}
                onPress={() =>
                  void controls.setVisionModel("").then((changed) => {
                    if (changed) done();
                  })
                }
              />
              <Choice
                label="Disable vision"
                selected={currentModel === "off"}
                disabled={disabled}
                onPress={() =>
                  void controls.setVisionModel("off").then((changed) => {
                    if (changed) done();
                  })
                }
              />
            </View>
          ) : state.catalog?.diagnostics?.length ? (
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
          <View>
            <Choice
              label={item.label}
              selected={
                selected?.provider === item.model.provider &&
                selected.model === item.model.model
              }
              disabled={disabled}
              onPress={() => setSelected(item.model)}
            />
            {item.warnings.map((warning, warningIndex) => (
              <View
                key={`${warning}-${warningIndex}`}
                style={{ paddingLeft: 16, paddingRight: 8, paddingBottom: 4 }}
              >
                <Copy muted>{`⚠ ${warning}`}</Copy>
              </View>
            ))}
          </View>
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
        {state.pending === "changeModel" ||
        state.pending === "setVisionModel" ? (
          <ActivityIndicator
            accessibilityLabel="Changing model"
            color={colors.accent}
          />
        ) : null}
        <Action
          disabled={disabled || !available}
          onPress={() => {
            if (!selected) return;
            const change =
              setting === "vision"
                ? controls.changeVisionModel(selected.provider, selected.model)
                : controls.changeModel(selected.provider, selected.model);
            void change.then((changed) => {
              if (changed) done();
            });
          }}
        >
          {setting === "vision" ? "Use vision model" : "Use model"}
        </Action>
      </View>
    </View>
  );
}
