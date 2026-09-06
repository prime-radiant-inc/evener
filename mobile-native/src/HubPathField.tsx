import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { ActivityIndicator, ScrollView, TextInput, View } from "react-native";
import {
  basename,
  childrenPrefix,
  isDirEntry,
  parentOf,
} from "../../cmd/evener-hub/frontend/src/widgets/pathfield/pathRows";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubPaths } from "./hubPaths";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function HubPathField({
  client,
  value,
  onChange,
  disabled,
  label,
  kind,
}: {
  client: ConversationClientLike;
  value: string;
  onChange(value: string): void;
  disabled: boolean;
  label: string;
  kind: "dir" | "file" | "outputFile";
}) {
  const colors = useColors();
  const model = useMemo(
    () => new HubPaths(client, kind !== "dir"),
    [client, kind],
  );
  const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
  const [open, setOpen] = useState(false);
  useEffect(() => () => model.dispose(), [model]);
  function edit(path: string) {
    model.clear();
    onChange(path);
  }
  return (
    <>
      <TextInput
        accessibilityLabel={label}
        value={value}
        onChangeText={edit}
        editable={!disabled}
        autoCapitalize="none"
        autoCorrect={false}
        style={[
          styles.input,
          { color: colors.text, borderColor: colors.border },
        ]}
      />
      <View style={[styles.row, { flexWrap: "wrap" }]}>
        <Action
          disabled={disabled || state.loading}
          onPress={() => {
            setOpen(true);
            void model.load(value);
          }}
        >
          {kind === "dir" ? "Find directories" : "Find files"}
        </Action>
        {open && (
          <Action
            disabled={disabled}
            onPress={() => {
              setOpen(false);
              model.clear();
            }}
          >
            Done browsing
          </Action>
        )}
      </View>
      {open && (
        <View style={{ gap: 8 }}>
          <Copy muted>{value || "Hub home directory"}</Copy>
          {value.startsWith("/") && value !== "/" && (
            <Action
              disabled={disabled}
              onPress={() => {
                const parent = childrenPrefix(parentOf(value));
                edit(parent);
                void model.load(parent);
              }}
            >
              Parent directory
            </Action>
          )}
          {state.loading && (
            <ActivityIndicator accessibilityLabel="Loading hub paths" />
          )}
          <ErrorMessage message={state.error} />
          {state.paths?.length === 0 && <Copy muted>No matching paths.</Copy>}
          {state.paths && state.paths.length > 0 && (
            <ScrollView
              style={{ maxHeight: 220 }}
              nestedScrollEnabled
              keyboardShouldPersistTaps="handled"
            >
              {state.paths.map((path) => (
                <Action
                  key={path}
                  disabled={disabled}
                  label={`${kind === "dir" ? "Choose directory" : isDirEntry(path) ? "Open directory" : "Choose file"} ${path}`}
                  onPress={() => {
                    if (kind !== "dir" && isDirEntry(path)) {
                      edit(path);
                      void model.load(path);
                    } else {
                      edit(kind === "dir" ? childrenPrefix(path) : path);
                      setOpen(false);
                    }
                  }}
                >
                  {basename(path) || path}
                </Action>
              ))}
            </ScrollView>
          )}
          {state.paths && state.paths.length >= 100 && (
            <Copy muted>
              Showing up to 100 paths. Type more of the path to narrow the
              results.
            </Copy>
          )}
        </View>
      )}
    </>
  );
}
