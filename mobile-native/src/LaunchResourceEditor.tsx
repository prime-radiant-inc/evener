import { useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type {
  LaunchOption,
  MCPServerSpec,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubPathField } from "./HubPathField";
import { assertLaunchListCurrent } from "./launchLists";
import { addLaunchMcp, resourceKey } from "./launchMcp";
import { addLaunchPath } from "./launchPaths";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchResourceEditor({
  option,
  value,
  effective,
  client,
  close,
  apply,
}: {
  option: LaunchOption;
  value: string[] | MCPServerSpec[] | undefined;
  effective: string[] | MCPServerSpec[] | undefined;
  client: ConversationClientLike | null;
  close(): void;
  apply(value: string[] | MCPServerSpec[] | undefined): void;
}) {
  const colors = useColors();
  const isMcp = option.kind === "mcpServerList";
  const original = useRef(value);
  const current = useRef(value);
  current.current = value;
  const [items, setItems] = useState(value ?? []);
  const [raw, setRaw] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: Connection changes invalidate validation replies while retaining the draft.
  useEffect(() => {
    generation.current++;
    setBusy(false);
    return () => {
      generation.current++;
    };
  }, [client]);
  async function submit(done: boolean) {
    if (busy) return;
    const version = ++generation.current;
    const active = () => version === generation.current;
    setBusy(true);
    setError(null);
    try {
      if (raw.trim() && !client)
        throw Error("Reconnect to validate this entry.");
      const next =
        raw.trim() && client
          ? isMcp
            ? await addLaunchMcp(client, items as MCPServerSpec[], raw)
            : await addLaunchPath(client, option, items as string[], raw)
          : items;
      if (!active()) return;
      if (done) {
        const collected = next.length ? next : undefined;
        assertLaunchListCurrent(
          original.current?.map(resourceKey),
          current.current?.map(resourceKey),
          collected?.map(resourceKey),
        );
        apply(collected);
      } else {
        setItems(next);
        setRaw("");
      }
    } catch (err) {
      if (active())
        setError(
          err instanceof Error ? err.message : "Unable to validate entry.",
        );
    } finally {
      if (active()) setBusy(false);
    }
  }
  const occurrences = new Map<string, number>();
  const rows = items.map((item) => {
    const identity = resourceKey(item);
    const occurrence = occurrences.get(identity) ?? 0;
    occurrences.set(identity, occurrence + 1);
    return { item, identity, key: `${identity}:${occurrence}` };
  });
  return (
    <Modal
      visible
      presentationStyle="pageSheet"
      animationType="slide"
      onRequestClose={close}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View style={[styles.row, { paddingHorizontal: 16 }]}>
          <Action onPress={close}>Cancel</Action>
          <Action
            disabled={busy}
            onPress={() => {
              void submit(true);
            }}
          >
            Done
          </Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "android" ? "height" : undefined}
          enabled={Platform.OS === "android"}
        >
          <ScrollView
            automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 20, gap: 12 }}
          >
            <Copy>{option.label}</Copy>
            {option.description && <Copy muted>{option.description}</Copy>}
            <Choice
              label="Use inherited value"
              disabled={busy}
              selected={!items.length && !raw}
              onPress={() => {
                setItems([]);
                setRaw("");
                setError(null);
              }}
            />
            <Copy muted>
              {isMcp ? "Effective servers" : "Effective paths"} ·{" "}
              {effective
                ?.map((item) => (typeof item === "string" ? item : item.name))
                .join("\n") || "None"}
            </Copy>
            <ErrorMessage message={error} />
            <View>
              {rows.map(({ item, identity, key }) => (
                <View
                  key={key}
                  style={{
                    flexDirection: "row",
                    alignItems: "center",
                    gap: 8,
                    paddingVertical: 4,
                    borderBottomWidth: 0.5,
                    borderColor: colors.border,
                  }}
                >
                  <View style={{ flex: 1, minWidth: 0 }}>
                    <Copy>{typeof item === "string" ? item : item.name}</Copy>
                    {typeof item !== "string" && (
                      <Copy muted>
                        {[item.command, ...(item.args ?? [])].join(" ")}
                      </Copy>
                    )}
                  </View>
                  <Action
                    disabled={busy}
                    label={`Remove ${isMcp ? "server" : "path"} ${typeof item === "string" ? item : item.name}`}
                    onPress={() =>
                      setItems(
                        items.filter(
                          (entry) => resourceKey(entry) !== identity,
                        ) as string[] | MCPServerSpec[],
                      )
                    }
                  >
                    Remove
                  </Action>
                </View>
              ))}
            </View>
            {client && !isMcp ? (
              <HubPathField
                client={client}
                kind={
                  option.pathKind === "file" || option.pathKind === "outputFile"
                    ? option.pathKind
                    : "dir"
                }
                value={raw}
                onChange={setRaw}
                disabled={busy}
                label="Path to add"
              />
            ) : (
              <TextInput
                accessibilityLabel={isMcp ? "Server to add" : "Path to add"}
                placeholder={isMcp ? "name command args..." : "Path to add"}
                placeholderTextColor={colors.secondary}
                value={raw}
                onChangeText={setRaw}
                editable={!busy}
                autoCapitalize="none"
                autoCorrect={false}
                style={[
                  styles.input,
                  { color: colors.text, borderColor: colors.border },
                ]}
              />
            )}
            <Action
              disabled={busy || !raw.trim() || !client}
              onPress={() => {
                void submit(false);
              }}
            >
              {isMcp ? "Add server" : "Add path"}
            </Action>
            {busy && (
              <ActivityIndicator accessibilityLabel="Validating entry" />
            )}
            {!client && (
              <Copy muted>
                Reconnect to browse and validate entries. Your draft is kept
                here.
              </Copy>
            )}
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
