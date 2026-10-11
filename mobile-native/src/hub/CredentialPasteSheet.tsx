// Pasting a key or a credential JSON (spec 12: "Replace key (paste)";
// prototype hub.js's key sheet): its own sheet over the provider's detail,
// with Cancel and Save in the header, the key field or the JSON paste control,
// and where the credential is kept. The page owns the draft and the write;
// this is the form.
import { getStringAsync } from "expo-clipboard";
import { useEffect, useMemo, useRef, useState } from "react";
import { AppState } from "react-native";
import { FormError, Group, GroupedPage, GroupFooter, Row, TextFieldRow } from "../sheet/Grouped";
import { ModalSheet } from "../sheet/ModalSheet";

const EMPTY_CLIPBOARD = "The clipboard held no credential JSON. Copy it again, then Paste.";

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
	// A multiline field can't be secure, so a pasted credential JSON is never
	// shown: the sheet says only how much was pasted. The paste itself reads the
	// clipboard, so the secret stays in the page's draft and off the screen.
	const [active, setActive] = useState(AppState.currentState === "active");
	// A read that lands after the sheet is gone must not repopulate the draft.
	const live = useRef(true);
	useEffect(() => {
		live.current = true;
		const subscription = AppState.addEventListener("change", (state) => setActive(state === "active"));
		return () => {
			live.current = false;
			subscription.remove();
		};
	}, []);
	const [pasteError, setPasteError] = useState<string | null>(null);
	async function pasteJson() {
		let text = "";
		try {
			text = await getStringAsync();
		} catch {
			// An unreadable clipboard reads as an empty one; nothing to paste.
		}
		if (!live.current) return;
		if (!text.trim()) {
			setPasteError(EMPTY_CLIPBOARD);
			return;
		}
		setPasteError(null);
		onChangeText(text);
	}
	// FormError speaks each new report, so the same words land as one report.
	const report = useMemo(() => {
		const message = error ?? pasteError;
		return message ? { message } : null;
	}, [error, pasteError]);
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
			privacyCover={json && !active}
		>
			<GroupedPage>
				<FormError error={report} />
				<Group>
					{json ? (
						<>
							<Row
								label="Google credential JSON"
								value={value.trim() ? `${value.trim().length} characters` : "Not pasted"}
							/>
							<Row label="Paste credential JSON" tone="accent" disabled={busy} onPress={() => void pasteJson()} />
						</>
					) : (
						<TextFieldRow
							label="API key"
							placeholder="Paste the API key"
							secure
							value={value}
							onChangeText={onChangeText}
							disabled={busy}
							returnKeyType="done"
							onSubmitEditing={save}
						/>
					)}
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
