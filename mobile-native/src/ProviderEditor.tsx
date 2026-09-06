import { useEffect, useRef, useState } from "react";
import { TextInput, View } from "react-native";
import type {
  InstanceEntry,
  ProviderDescriptor,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  createProviderParams,
  editProviderParams,
  type ProviderDraft,
} from "./providerForm";
import type { ProviderInstances } from "./providerInstances";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ProviderEditor({
  instance,
  providers,
  model,
  disabled,
  onSaved,
  onCancel,
}: {
  instance?: InstanceEntry;
  providers: ProviderDescriptor[];
  model: ProviderInstances;
  disabled: boolean;
  onSaved(name: string): void;
  onCancel(): void;
}) {
  const colors = useColors();
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const [draft, setDraft] = useState<ProviderDraft>({
    name: instance?.name ?? "",
    base: "",
    baseUrl: instance?.baseUrl ?? "",
    vars: {},
    apiKeyEnv: "",
    credentialHeader: "",
  });
  const [error, setError] = useState<string | null>(null);
  const [choosing, setChoosing] = useState(false);
  const [query, setQuery] = useState("");
  const [saving, setSaving] = useState(false);
  const busy = disabled || saving;
  function field(
    label: string,
    value: string,
    change: (value: string) => void,
  ) {
    return (
      <View key={label} style={{ gap: 4 }}>
        <Copy>{label}</Copy>
        <TextInput
          accessibilityLabel={label}
          value={value}
          onChangeText={change}
          editable={!busy}
          autoCapitalize="none"
          autoCorrect={false}
          style={[
            styles.input,
            { color: colors.text, borderColor: colors.border },
          ]}
        />
      </View>
    );
  }
  async function save() {
    if (busy) return;
    setError(null);
    let create: ReturnType<typeof createProviderParams> | undefined;
    let edit: ReturnType<typeof editProviderParams> | undefined;
    try {
      if (instance) edit = editProviderParams(instance, draft.baseUrl);
      else create = createProviderParams(draft, providers);
    } catch (failure) {
      setError(
        failure instanceof Error ? failure.message : "Check the form fields.",
      );
      return;
    }
    setSaving(true);
    try {
      if (edit) await model.edit(edit);
      else if (create) await model.create(create);
      if (alive.current) onSaved(instance?.name ?? draft.name.trim());
    } catch {
      if (alive.current)
        setError(
          "Save could not be confirmed. Check the provider list before trying again.",
        );
    } finally {
      if (alive.current) setSaving(false);
    }
  }
  return (
    <View style={{ gap: 12 }}>
      <Copy>
        {instance ? `Edit ${instance.name}` : "Add provider instance"}
      </Copy>
      {!instance && (
        <>
          <Copy muted>Base provider</Copy>
          <Action
            disabled={busy}
            expanded={choosing}
            onPress={() => setChoosing(!choosing)}
          >
            {providers.find((provider) => provider.id === draft.base)?.name ||
              draft.base ||
              "Choose base provider"}
          </Action>
          {choosing && (
            <>
              {field("Find provider", query, setQuery)}
              {providers
                .filter((provider) =>
                  `${provider.id} ${provider.name ?? ""}`
                    .toLowerCase()
                    .includes(query.trim().toLowerCase()),
                )
                .map((provider) => (
                  <Choice
                    key={provider.id}
                    label={provider.name || provider.id}
                    selected={draft.base === provider.id}
                    disabled={busy}
                    onPress={() => {
                      setDraft({ ...draft, base: provider.id, vars: {} });
                      setChoosing(false);
                      setQuery("");
                    }}
                  />
                ))}
            </>
          )}
          {field("Instance name", draft.name, (name) =>
            setDraft({ ...draft, name }),
          )}
        </>
      )}
      {field("Base URL (optional)", draft.baseUrl, (baseUrl) =>
        setDraft({ ...draft, baseUrl }),
      )}
      {instance?.baseUrl && !draft.baseUrl.trim() && (
        <Copy>Resets the endpoint to the provider’s default.</Copy>
      )}
      {!instance && (
        <>
          {(
            providers.find((provider) => provider.id === draft.base)?.varsEnv ??
            []
          ).map((name) =>
            field(name, draft.vars[name] ?? "", (value) =>
              setDraft({ ...draft, vars: { ...draft.vars, [name]: value } }),
            ),
          )}
          {field(
            "API key environment variable (optional)",
            draft.apiKeyEnv,
            (apiKeyEnv) => setDraft({ ...draft, apiKeyEnv }),
          )}
          {field(
            "Credential header (optional)",
            draft.credentialHeader,
            (credentialHeader) => setDraft({ ...draft, credentialHeader }),
          )}
          <Copy muted>
            Use a $VARIABLE reference in credential headers. Store API keys from
            the instance details.
          </Copy>
        </>
      )}
      <ErrorMessage message={error} />
      <View style={[styles.row, { flexWrap: "wrap" }]}>
        <Action
          disabled={busy}
          onPress={() => {
            void save();
          }}
        >
          {saving ? "Saving…" : "Save instance"}
        </Action>
        <Action disabled={saving} onPress={onCancel}>
          Cancel
        </Action>
      </View>
    </View>
  );
}
