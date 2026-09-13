import { useEffect, useState } from "react";
import { ActivityIndicator, Pressable, TextInput, View } from "react-native";
import { inactivePromptDependent } from "../../cmd/evener-hub/frontend/src/panes/settings/sections/launchShared/schema";
import { perLaunchEvenerOptions } from "../../cmd/evener-hub/frontend/src/panes/spawn/schema";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
} from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchFieldEditor } from "./LaunchFieldEditor";
import { scalarKinds } from "./launchScalar";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function LaunchOverrides({
  client,
  cwd,
  value,
  onChange,
  disabled,
}: {
  client: ConversationClientLike | null;
  cwd: string;
  value: LaunchConfigLayer;
  onChange(value: LaunchConfigLayer): void;
  disabled: boolean;
}) {
  const colors = useColors();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState<LaunchOption[]>([]);
  const [resolved, setResolved] = useState<LaunchConfigResolved | null>(null);
  const [selected, setSelected] = useState<LaunchOption | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: Explicit retry reloads the same hub, directory and draft.
  useEffect(() => {
    let active = true;
    setResolved(null);
    setOptions([]);
    if (!client || !open) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    Promise.all([
      client.request("evener/launch/schema", {}),
      client
        .request("evener/launch/resolve", { cwd, launchOverrides: value })
        .catch(() => null),
    ])
      .then(([schema, effective]) => {
        if (!active) return;
        setOptions(perLaunchEvenerOptions(schema));
        setResolved(effective);
        setError(
          effective
            ? null
            : "Effective values could not be resolved. You can still correct the session options or retry.",
        );
      })
      .catch(() => {
        if (active)
          setError("Could not load per-session settings. Reconnect or retry.");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [client, cwd, value, open, revision]);
  const count = Object.keys(value).length;
  const visible = options.filter(
    (option) =>
      (scalarKinds.has(option.kind) ||
        ["envMap", "modelList", "pathList", "mcpServerList"].includes(
          option.kind,
        )) &&
      !inactivePromptDependent(option.wireField, value) &&
      `${option.label} ${option.group} ${option.description ?? ""}`
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  return (
    <View style={{ gap: 8, ...(open ? { width: "100%" } : {}) }}>
      <Action disabled={disabled} onPress={() => setOpen(!open)}>
        {open
          ? "Hide session options"
          : count
            ? `Session options · ${count} overrides`
            : "Session options"}
      </Action>
      {open && (
        <>
          <Copy muted>
            Apply only to this new session. Hub and project defaults stay
            unchanged.
          </Copy>
          {!!count && (
            <Action disabled={disabled} onPress={() => onChange({})}>
              Use launch defaults
            </Action>
          )}
          <ErrorMessage message={error} />
          {error && (
            <Action
              disabled={!client || loading}
              onPress={() => setRevision(revision + 1)}
            >
              Retry session options
            </Action>
          )}
          {loading && (
            <ActivityIndicator accessibilityLabel="Resolving session options" />
          )}
          <TextInput
            accessibilityLabel="Search session options"
            value={query}
            onChangeText={setQuery}
            placeholder="Find a setting"
            placeholderTextColor={colors.secondary}
            style={[
              styles.input,
              { color: colors.text, borderColor: colors.border },
            ]}
          />
          {visible.map((option) => {
            const field = option.wireField as keyof LaunchConfigLayer;
            const explicit = value[field];
            const effective = resolved?.effective[field];
            const summary =
              explicit === undefined
                ? "Inherited"
                : typeof explicit === "object"
                  ? "Custom values"
                  : String(explicit);
            return (
              <Pressable
                key={field}
                accessibilityRole="button"
                accessibilityLabel={`Configure ${option.label}`}
                disabled={disabled || loading}
                onPress={() => setSelected(option)}
                style={{
                  paddingVertical: 10,
                  borderBottomWidth: 0.5,
                  borderBottomColor: colors.border,
                }}
              >
                <Copy>{option.label}</Copy>
                <Copy muted numberOfLines={2}>
                  {summary}
                  {explicit === undefined &&
                  effective !== undefined &&
                  typeof effective !== "object"
                    ? ` · ${String(effective)}`
                    : ""}
                </Copy>
              </Pressable>
            );
          })}
        </>
      )}
      {selected && (
        <LaunchFieldEditor
          key={selected.wireField}
          option={selected}
          value={value}
          effective={resolved?.effective ?? {}}
          client={client}
          close={() => setSelected(null)}
          apply={(next) => {
            const updated = { ...value };
            const field = selected.wireField as keyof LaunchConfigLayer;
            if (next === undefined) delete updated[field];
            else Object.assign(updated, { [field]: next });
            onChange(updated);
            setSelected(null);
          }}
        />
      )}
    </View>
  );
}
