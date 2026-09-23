import { useEffect, useRef, useState } from "react";
import { TextInput, View } from "react-native";
import {
  isEndpointConflict,
  type InstanceCreateParams,
  type InstanceEditParams,
  type InstanceEntry,
  type ProviderDescriptor,
} from "@evener/appwire-client";
import type { LiveReadiness } from "./connectionDisplay";
import { createProviderParams, editProviderParams, type ProviderDraft } from "./providerForm";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ProviderEditor({
  instance,
  providers,
  onCreate,
  onEdit,
  disabled,
  canUseConnection,
  onSaved,
  onEndpointConflict,
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
  /** The live readiness an issued save re-checks at invocation time: the
   * `disabled` prop is a render-time snapshot, and a disconnect between the
   * render and the press leaves it saying ready. Every other mutation entry
   * on the providers screen guards through this same predicate (act's own
   * entry check, whenReady around each control); the save is the one write
   * the screen cannot wrap, so the editor guards it itself. */
  canUseConnection: LiveReadiness;
  onSaved(name: string): void;
  onEndpointConflict(name: string): void;
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
  // The save's assertion belongs to the row this editor was OPENED on, not
  // whatever it resolves to now: the screen this editor lives in survives
  // reconnects behind a banner (ProvidersScreen), so a row another client
  // moved while this one was away republishes under the open editor with a
  // new fingerprint - and an assertion read from the live row would approve
  // a save against a destination the user never saw. Captured here, the
  // moved row's refusal routes through the endpoint-conflict path: the
  // editor closes, the list re-reads, the screen warns in its own words. The
  // editor is keyed by instance name, so the ref lives exactly as long as
  // this editor's target row; a row the hub could not fingerprint at open
  // asserts nothing, as before.
  const assertedFingerprint = useRef(instance?.endpointFingerprint);
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
    // The invocation-time readiness guard, ahead of every state change: a
    // save that cannot be sent bails before clearing the error slot or
    // reporting a failure, so the draft stays exactly as typed for the
    // connection's return instead of reading as a save that was tried.
    if (busy || !canUseConnection()) return;
    setError(null);
    let create: ReturnType<typeof createProviderParams> | undefined;
    let edit: ReturnType<typeof editProviderParams> | undefined;
    try {
      if (instance)
        edit = editProviderParams(
          { ...instance, endpointFingerprint: assertedFingerprint.current },
          draft.baseUrl,
        );
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
    } catch (err) {
      if (alive.current) {
        if (isEndpointConflict(err)) {
          // The hub refused the asserted destination: the row moved since this
          // editor was opened, so nothing was written. Hand it to the screen,
          // which clears this editor, re-reads the provider list, and warns in
          // its own words - the rejection's text can echo submitted values and
          // is never shown.
          onEndpointConflict(instance?.name ?? draft.name.trim());
        } else {
          setError(
            "Save could not be confirmed. Check the provider list before trying again.",
          );
        }
      }
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
