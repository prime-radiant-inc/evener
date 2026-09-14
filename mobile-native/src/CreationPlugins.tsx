import { useEffect, useState } from "react";
import {
  ActivityIndicator,
  FlatList,
  Modal,
  Platform,
  Pressable,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import {
  type PluginSelectionState,
  pluginSelectionIssues,
  selectAllPlugins,
  selectedPluginNames,
  selectNoPlugins,
  setPluginSelected,
  withPluginSelection,
} from "../../cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState";
import { usePluginPreview } from "../../cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview";
import type { AppwireClient } from "../../appwire-client/typescript/client";
import type { LaunchConfigLayer } from "../../appwire-client/typescript/types.gen";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function CreationPlugins({
  client,
  cwd,
  value,
  onChange,
  disabled,
}: {
  client: AppwireClient;
  cwd: string;
  value: LaunchConfigLayer;
  onChange(value: LaunchConfigLayer): void;
  disabled: boolean;
}) {
  const colors = useColors();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(
    () =>
      client.onNotification((n) => {
        if (
          n.method === "evener/plugin/updated" ||
          n.method === "evener/launch/updated"
        )
          setRevision((r) => r + 1);
      }),
    [client],
  );
  const preview = usePluginPreview({
    client,
    cwd,
    launchOverrides: value,
    pluginRevision: revision,
    enabled: !!cwd,
  });
  const response = preview.state.response;
  const selection: PluginSelectionState =
    value.enabledPlugins === undefined
      ? { mode: "default" }
      : { mode: "explicit", names: value.enabledPlugins };
  const names = response ? selectedPluginNames(selection, response) : [];
  const issues = response ? pluginSelectionIssues(selection, response) : [];
  const change = (next: PluginSelectionState) =>
    onChange(withPluginSelection(value, next));
  const summary =
    selection.mode === "default"
      ? "Launch defaults"
      : selection.names.length
        ? `${selection.names.length} selected`
        : "None";
  const rows = (response?.plugins ?? []).filter((p) =>
    `${p.name} ${p.description ?? ""} ${p.marketplace ?? ""} ${p.source}`
      .toLowerCase()
      .includes(query.trim().toLowerCase()),
  );
  return (
    <>
      <Action
        disabled={disabled || !cwd}
        onPress={() => setOpen(true)}
        tone="quiet"
      >{`Plugins · ${summary}`}</Action>
      <Modal
        visible={open}
        animationType="slide"
        presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
        onRequestClose={() => setOpen(false)}
      >
        <SafeAreaView
          style={[styles.fill, { backgroundColor: colors.background }]}
        >
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              paddingHorizontal: 16,
            }}
          >
            <Copy>Plugins for this session</Copy>
            <Action onPress={() => setOpen(false)}>Done</Action>
          </View>
          <FlatList
            data={rows}
            keyExtractor={(item) => item.name}
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 16, paddingTop: 0 }}
            ListHeaderComponent={
              <View style={{ gap: 8 }}>
                <Copy
                  muted
                >{`${summary}${response ? ` · ${names.length} of ${response.plugins.length} available` : ""}`}</Copy>
                <View
                  style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}
                >
                  <Action
                    disabled={disabled}
                    onPress={() => change({ mode: "default" })}
                  >
                    Use launch defaults
                  </Action>
                  <Action
                    disabled={
                      disabled || !response || preview.state.status !== "ready"
                    }
                    onPress={() =>
                      response && change(selectAllPlugins(response))
                    }
                  >
                    All
                  </Action>
                  <Action
                    disabled={disabled}
                    onPress={() => change(selectNoPlugins())}
                  >
                    None
                  </Action>
                </View>
                <TextInput
                  accessibilityLabel="Search plugins for this session"
                  value={query}
                  onChangeText={setQuery}
                  placeholder="Find a plugin"
                  placeholderTextColor={colors.secondary}
                  autoCorrect={false}
                  style={[
                    styles.input,
                    { color: colors.text, borderColor: colors.border },
                  ]}
                />
                {preview.state.status === "loading" && (
                  <ActivityIndicator accessibilityLabel="Inspecting session plugins" />
                )}
                <ErrorMessage
                  message={
                    preview.state.status === "error"
                      ? preview.state.message
                      : null
                  }
                />
                {preview.state.status === "error" && (
                  <Action onPress={preview.retry}>Retry plugin preview</Action>
                )}
                {issues.map((issue) => (
                  <View key={`${issue.name}:${issue.reason}`}>
                    <ErrorMessage message={`${issue.name}: ${issue.reason}`} />
                    {selection.mode === "explicit" &&
                      selection.names.includes(issue.name) && (
                        <Action
                          disabled={disabled}
                          onPress={() =>
                            change({
                              mode: "explicit",
                              names: selection.names.filter(
                                (name) => name !== issue.name,
                              ),
                            })
                          }
                        >{`Remove ${issue.name} from selection`}</Action>
                      )}
                  </View>
                ))}
                {(response?.diagnostics ?? []).map((diagnostic) => (
                  <Copy muted key={`${diagnostic.path}:${diagnostic.message}`}>
                    {diagnostic.message}
                  </Copy>
                ))}
              </View>
            }
            ListEmptyComponent={
              <Copy muted>
                {preview.state.status === "ready"
                  ? response?.plugins.length
                    ? "No matching plugins."
                    : "No plugins available for this directory."
                  : "The plugin preview is not ready."}
              </Copy>
            }
            renderItem={({ item }) => {
              const checked = names.includes(item.name);
              const unavailable =
                disabled || (preview.state.status === "error" && !checked);
              return (
                <Pressable
                  accessibilityRole="checkbox"
                  accessibilityLabel={item.name}
                  accessibilityState={{ checked, disabled: unavailable }}
                  disabled={unavailable}
                  onPress={() =>
                    response &&
                    change(
                      setPluginSelected(
                        selection,
                        response,
                        item.name,
                        !checked,
                      ),
                    )
                  }
                  style={{
                    paddingVertical: 12,
                    borderBottomWidth: 0.5,
                    borderBottomColor: colors.border,
                    opacity: unavailable ? 0.4 : 1,
                  }}
                >
                  <View style={{ flexDirection: "row", gap: 12 }}>
                    <Copy>{checked ? "✓" : "○"}</Copy>
                    <View style={{ flex: 1, gap: 3 }}>
                      <Copy>
                        {item.name}
                        {item.version ? ` · ${item.version}` : ""}
                      </Copy>
                      {!!item.description && (
                        <Copy muted>{item.description}</Copy>
                      )}
                      <Copy muted>
                        {item.marketplace
                          ? `From ${item.marketplace}`
                          : item.path || item.source}
                      </Copy>
                      <Copy muted>
                        {[
                          [item.skillCount, "skills"],
                          [item.agentCount, "agents"],
                          [item.commandCount, "commands"],
                          [item.hookCount, "hooks"],
                          [item.mcpCount, "MCP servers"],
                        ]
                          .filter(([count]) => Number(count) > 0)
                          .map(([count, label]) => `${count} ${label}`)
                          .join(" · ") || "No components"}
                      </Copy>
                    </View>
                  </View>
                </Pressable>
              );
            }}
          />
        </SafeAreaView>
      </Modal>
    </>
  );
}
