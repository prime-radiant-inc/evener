import { useEffect, useRef, useState } from "react";
import { TextInput, View } from "react-native";
import type {
  InstanceEntry,
  InstanceCreateParams,
  InstanceEditParams,
  ProviderDescriptor,
} from "@evener/appwire-client";
import { createProviderParams, editProviderParams, type ProviderDraft } from "./providerForm";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ProviderEditor({
  instance,
  providers,
  onCreate,
  onEdit,
  disabled,
  onSaved,
  onCancel,
}: {
  instance?: InstanceEntry;
  providers: ProviderDescriptor[];
  // The screen owns the write gate (one write at a time, refused while the
  // listing refuses configuration): the editor hands a validated draft back
  // and the screen issues it against the credential core. Each resolves the
  // core's applied verdict, so the editor reports success only on a confirmed
  // write.
  onCreate(params: InstanceCreateParams): Promise<boolean>;
  onEdit(params: InstanceEditParams): Promise<boolean>;
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
      let applied: boolean;
      if (edit) applied = await onEdit(edit);
      else if (create) applied = await onCreate(create);
      else {
        // Neither an edit nor a create was built: there is no write to issue,
        // so the draft is simply accepted as it stands.
        if (alive.current) onSaved(instance?.name ?? draft.name.trim());
        return;
      }
      if (!applied) {
        // A newer listing superseded this save's answer: the write may have
        // landed on the host, but the store cannot confirm it, so the editor
        // does not close reporting success.
        if (alive.current)
          setError(
            "Save could not be confirmed. Check the provider list before trying again.",
          );
        return;
      }
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
          {Object.entries(
            providers.find((provider) => provider.id === draft.base)?.vars ?? {},
          )
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([template, environment]) =>
              field(environment, draft.vars[template] ?? "", (value) =>
                setDraft({ ...draft, vars: { ...draft.vars, [template]: value } }),
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
