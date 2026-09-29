import { useRef, useState } from "react";
import type { HubProfile, HubUpdate } from "./connection";
import { Group, GroupedPage, GroupFooter, Row, SwitchRow, TextFieldRow } from "./sheet/Grouped";
import { ModalSheet } from "./sheet/ModalSheet";

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
	const [error, setError] = useState<string | null>(null);
	async function submit() {
		if (pending.current) return;
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
			done={{ label: "Save", disabled: saving || !name.trim(), onPress: () => void submit() }}
			onRequestClose={cancel}
		>
			<GroupedPage>
				<Group label="Name">
					<TextFieldRow label="Hub name" value={name} onChangeText={setName} machine={false} disabled={saving} />
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
						/>
					) : null}
				</Group>
				<GroupFooter>
					{replaceToken ? "Leave empty to remove the saved token." : "The saved token will be kept."}
				</GroupFooter>
				{error ? <GroupFooter tone="danger">{error}</GroupFooter> : null}
			</GroupedPage>
		</ModalSheet>
	);
}
