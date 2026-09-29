import { useRef, useState } from "react";
import type { TextInput } from "react-native";
import type { HubProfile, HubUpdate } from "./connection";
import {
	FormError,
	Group,
	GroupedPage,
	GroupFooter,
	Row,
	SwitchRow,
	TextFieldRow,
	useErrorInView,
	useFormError,
} from "./sheet/Grouped";
import { ModalSheet } from "./sheet/ModalSheet";

const TOKEN_REPLACED = "Leave empty to remove the saved token.";
const TOKEN_KEPT = "The saved token will be kept.";

/** Edit hub (spec 12, Hubs): the saved hub's name, its address to read, and
 * its token, replaced only when asked. */
export function HubEditor({
	profile,
	save,
	close,
}: {
	profile: HubProfile;
	save: (id: string, update: HubUpdate) => Promise<void>;
	close: () => void;
}) {
	const [name, setName] = useState(profile.name);
	const [replaceToken, setReplaceToken] = useState(false);
	const [token, setToken] = useState("");
	const [saving, setSaving] = useState(false);
	const pending = useRef(false);
	const [error, setError] = useFormError();
	const tokenInput = useRef<TextInput>(null);
	const page = useErrorInView(error);
	async function submit() {
		// The return key reaches here too, so it holds a blank name as Save does.
		if (pending.current || !name.trim()) return;
		pending.current = true;
		setSaving(true);
		setError(null);
		try {
			await save(profile.id, { name, ...(replaceToken ? { token } : {}) });
			close();
		} catch {
			setError("Could not save this hub. Check the name and token, then retry.");
		} finally {
			pending.current = false;
			setSaving(false);
		}
	}
	// A save in flight finishes before the editor can close.
	function cancel() {
		if (!pending.current) close();
	}
	return (
		<ModalSheet
			title="Edit hub"
			onCancel={cancel}
			cancelDisabled={saving}
			done={{
				label: saving ? "Saving…" : "Save",
				disabled: saving || !name.trim(),
				busy: saving,
				onPress: () => void submit(),
			}}
			onRequestClose={cancel}
		>
			<GroupedPage scrollRef={page}>
				<FormError error={error} />
				<Group label="Name">
					<TextFieldRow
						label="Hub name"
						value={name}
						onChangeText={setName}
						machine={false}
						disabled={saving}
						returnKeyType={replaceToken ? "next" : "done"}
						onSubmitEditing={replaceToken ? () => tokenInput.current?.focus() : () => void submit()}
					/>
				</Group>
				<Group label="Address">
					<Row label={profile.origin} machineLabel accessibilityLabel={`Address, ${profile.origin}`} />
				</Group>
				<GroupFooter>To connect to another address, add a separate hub.</GroupFooter>
				<Group label="Token">
					<SwitchRow label="Replace saved token" value={replaceToken} disabled={saving} onChange={setReplaceToken} />
					{replaceToken ? (
						<TextFieldRow
							label="New bearer token"
							placeholder="New bearer token"
							value={token}
							onChangeText={setToken}
							secure
							disabled={saving}
							returnKeyType="done"
							onSubmitEditing={() => void submit()}
							ref={tokenInput}
						/>
					) : null}
				</Group>
				<GroupFooter>{replaceToken ? TOKEN_REPLACED : TOKEN_KEPT}</GroupFooter>
			</GroupedPage>
		</ModalSheet>
	);
}
