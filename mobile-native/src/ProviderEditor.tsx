import { type ReactNode, useEffect, useRef, useState } from "react";
import {
	isEndpointConflict,
	type InstanceCreateParams,
	type InstanceEditParams,
	type InstanceEntry,
	type ProviderDescriptor,
} from "@evener/appwire-client";
import type { LiveReadiness } from "./connectionDisplay";
import { createProviderParams, editProviderParams, type ProviderDraft } from "./providerForm";
import type { TextInput } from "react-native";
import {
	FormError,
	Group,
	GroupedPage,
	GroupFooter,
	GroupGap,
	Row,
	SearchField,
	TextFieldRow,
	useErrorInView,
	useFormError,
} from "./sheet/Grouped";
import { Sheet } from "./sheet/Sheet";

const CREDENTIAL_HEADER_HELP = "Optional. Use a $VARIABLE reference here; store API keys from the provider’s details.";

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
	onSavingChange,
	accessory,
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
	/** Told when a save starts and ends, so the modal around the editor can
	 * hold a swipe down while one is in flight. */
	onSavingChange?: (saving: boolean) => void;
	/** Pinned under the title, such as the connection's status line. */
	accessory?: ReactNode;
}) {
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
	// reconnects behind a status line (hub/ProvidersPage), so a row another client
	// moved while this one was away republishes under the open editor with a
	// new fingerprint - and an assertion read from the live row would approve
	// a save against a destination the user never saw. Captured here, the
	// moved row's refusal routes through the endpoint-conflict path: the
	// editor closes, the list re-reads, the screen warns in its own words. The
	// editor is keyed by instance name, so the ref lives exactly as long as
	// this editor's target row; a row the hub could not fingerprint at open
	// asserts nothing, as before.
	const assertedFingerprint = useRef(instance?.endpointFingerprint);
	const [error, setError] = useFormError();
	const [choosing, setChoosing] = useState(false);
	const [query, setQuery] = useState("");
	const [saving, setSaving] = useState(false);
	const busy = disabled || saving;
	const savingChanged = useRef(onSavingChange);
	savingChanged.current = onSavingChange;
	useEffect(() => savingChanged.current?.(saving), [saving]);
	// A save that lands closes the editor before it can clear `saving`.
	useEffect(() => () => savingChanged.current?.(false), []);
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
				edit = editProviderParams({ ...instance, endpointFingerprint: assertedFingerprint.current }, draft.baseUrl);
			else create = createProviderParams(draft, providers);
		} catch (failure) {
			setError(failure instanceof Error ? failure.message : "Check the form fields.");
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
				if (alive.current) setError("Save could not be confirmed. Check the provider list before trying again.");
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
					setError("Save could not be confirmed. Check the provider list before trying again.");
				}
			}
		} finally {
			if (alive.current) setSaving(false);
		}
	}
	const base = providers.find((provider) => provider.id === draft.base);
	const baseName = base ? base.name || base.id : null;
	const matches = providers.filter((provider) =>
		`${provider.id} ${provider.name ?? ""}`.toLowerCase().includes(query.trim().toLowerCase()),
	);
	const varFields = Object.entries(base?.vars ?? {}).sort(([a], [b]) => a.localeCompare(b));
	// The form's fields in order, so each one's return key leads to the next
	// and the last one's saves.
	const order = instance
		? ["baseUrl"]
		: ["name", "baseUrl", ...varFields.map(([template]) => template), "apiKeyEnv", "credentialHeader"];
	const inputs = useRef<Record<string, TextInput | null>>({});
	function chain(key: string) {
		const next = order[order.indexOf(key) + 1];
		return {
			ref: (input: TextInput | null) => {
				inputs.current[key] = input;
			},
			returnKeyType: next ? ("next" as const) : ("done" as const),
			onSubmitEditing: next ? () => inputs.current[next]?.focus() : () => void save(),
		};
	}
	const incomplete = !instance && (!base || !draft.name.trim());
	const page = useErrorInView(error);
	return (
		<Sheet
			title={instance ? `Edit ${instance.name}` : "Add provider"}
			onCancel={onCancel}
			cancelDisabled={saving}
			done={{
				label: saving ? "Saving…" : "Save",
				disabled: busy || incomplete,
				busy: saving,
				onPress: () => void save(),
			}}
			accessory={accessory}
		>
			<GroupedPage scrollRef={page}>
				<FormError error={error} />
				{!instance && (
					<>
						<Group label="Base provider">
							<Row
								label={baseName ?? "Choose a provider"}
								accessibilityLabel={baseName ? `Base provider, ${baseName}` : "Choose base provider"}
								tone={baseName ? "normal" : "accent"}
								chevron
								disabled={busy}
								onPress={() => setChoosing(!choosing)}
							/>
						</Group>
						{choosing ? (
							<>
								<GroupGap />
								<SearchField label="Find provider" query={query} onChange={setQuery} />
								{matches.length > 0 ? (
									<Group>
										{matches.map((provider) => (
											<Row
												key={provider.id}
												label={provider.name || provider.id}
												checked={draft.base === provider.id}
												disabled={busy}
												onPress={() => {
													setDraft({ ...draft, base: provider.id, vars: {} });
													setChoosing(false);
													setQuery("");
												}}
											/>
										))}
									</Group>
								) : (
									<GroupFooter>No providers match.</GroupFooter>
								)}
							</>
						) : null}
						<Group label="Name">
							<TextFieldRow
								label="Instance name"
								value={draft.name}
								onChangeText={(name) => setDraft({ ...draft, name })}
								disabled={busy}
								{...chain("name")}
							/>
						</Group>
					</>
				)}
				<Group label="Base URL">
					<TextFieldRow
						label="Base URL"
						value={draft.baseUrl}
						onChangeText={(baseUrl) => setDraft({ ...draft, baseUrl })}
						disabled={busy}
						{...chain("baseUrl")}
					/>
				</Group>
				<GroupFooter>
					{instance?.baseUrl && !draft.baseUrl.trim()
						? "Resets the endpoint to the provider’s default."
						: "Optional. Empty uses the provider’s default."}
				</GroupFooter>
				{!instance && (
					<>
						{varFields.map(([template, environment]) => (
							<Group key={template} label={environment} machineLabel>
								<TextFieldRow
									label={environment}
									value={draft.vars[template] ?? ""}
									onChangeText={(value) => setDraft({ ...draft, vars: { ...draft.vars, [template]: value } })}
									disabled={busy}
									{...chain(template)}
								/>
							</Group>
						))}
						<Group label="API key variable">
							<TextFieldRow
								label="API key environment variable"
								value={draft.apiKeyEnv}
								onChangeText={(apiKeyEnv) => setDraft({ ...draft, apiKeyEnv })}
								disabled={busy}
								{...chain("apiKeyEnv")}
							/>
						</Group>
						<GroupFooter>Optional.</GroupFooter>
						<Group label="Credential header">
							<TextFieldRow
								label="Credential header"
								value={draft.credentialHeader}
								onChangeText={(credentialHeader) => setDraft({ ...draft, credentialHeader })}
								disabled={busy}
								{...chain("credentialHeader")}
							/>
						</Group>
						<GroupFooter>{CREDENTIAL_HEADER_HELP}</GroupFooter>
					</>
				)}
			</GroupedPage>
		</Sheet>
	);
}
