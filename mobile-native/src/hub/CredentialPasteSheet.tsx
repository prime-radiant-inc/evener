// Pasting a key or a credential JSON (spec 12: "Replace key (paste)";
// prototype hub.js's key sheet): its own sheet over the provider's detail,
// with Cancel and Save in the header, the one field, and where the credential
// is kept. The page owns the draft and the write; this is the form.
import { useMemo } from "react";
import { FormError, Group, GroupedPage, GroupFooter, TextFieldRow } from "../sheet/Grouped";
import { ModalSheet } from "../sheet/ModalSheet";

export function CredentialPasteSheet({
	title,
	kind,
	value,
	onChangeText,
	busy,
	canSave,
	error,
	onSave,
	onCancel,
}: {
	/** The action that opened it: "Replace key", "Set key", … */
	title: string;
	kind: "apiKey" | "credentialJson";
	value: string;
	onChangeText(text: string): void;
	/** Its save is in flight. */
	busy: boolean;
	/** Save may run: there is text and the connection is ready. */
	canSave: boolean;
	/** Why the last save didn't land. */
	error: string | null;
	onSave(): void;
	/** Cancel or a swipe down; the page asks first when there is text. */
	onCancel(): void;
}) {
	const json = kind === "credentialJson";
	// FormError speaks each new report, so the same words land as one report.
	const report = useMemo(() => (error ? { message: error } : null), [error]);
	// Save and the keyboard's Done run through one gate: never while a save
	// runs, and only when Save could.
	const save = () => {
		if (canSave && !busy) onSave();
	};
	return (
		<ModalSheet
			title={title}
			onCancel={onCancel}
			cancelDisabled={busy}
			done={{ label: busy ? "Saving…" : "Save", disabled: busy || !canSave, busy, onPress: save }}
			onRequestClose={onCancel}
		>
			<GroupedPage>
				<FormError error={report} />
				<Group>
					<TextFieldRow
						label={json ? "Google credential JSON" : "API key"}
						placeholder={json ? "Paste the credential JSON" : "Paste the API key"}
						multiline={json}
						secure={!json}
						value={value}
						onChangeText={onChangeText}
						disabled={busy}
						{...(json ? null : { returnKeyType: "done" as const, onSubmitEditing: save })}
					/>
				</Group>
				<GroupFooter>
					{json
						? "Paste a service-account key or application_default_credentials.json. The hub checks and keeps it."
						: "The key is stored on the hub, not on this phone."}
				</GroupFooter>
			</GroupedPage>
		</ModalSheet>
	);
}
