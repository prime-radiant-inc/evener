// Edit a host in the Hub (spec 12): every field the web's Edit dialog has,
// under its labels and help lines, filled from the host's row. Save sends the
// guarded update and goes back; the hub is the one validator, so Save holds
// only while it saves, and a refusal that names a field lands under it.
import {
	type EditableHostField,
	friendlyErrorMessage,
	HOST_ENTRY_FIELD_ORDER,
	HOST_ENTRY_FIELD_TEXT,
	type HostEntry,
	type HostRow,
	hostFieldError,
	rootsFromText,
	rootsToText,
} from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useLayoutEffect, useState } from "react";
import type { HostsController } from "../hosts/hostsController";
import { Group, GroupedPage, GroupFooter, GroupLabel, TextFieldRow } from "../sheet/Grouped";
import { HeaderButton } from "../sheet/HeaderButton";
import { SheetStatus } from "../sheet/SheetStatus";
import { HostsNotListed } from "./HostsPage";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen } from "./useHostsOnScreen";

type Props = NativeStackScreenProps<HubRoutes, "HostEdit">;
type Fields = Record<EditableHostField, string>;

export function HostEditPage(props: Props) {
	const { hubName, hosts } = useHubSheet();
	const { state, loadError } = useHostsOnScreen();
	const row = state?.rows?.find((candidate) => candidate.name === props.route.params.name);
	if (!row || !hosts) return <HostsNotListed hubName={hubName} error={loadError} />;
	return <HostEditForm {...props} row={row} hosts={hosts} />;
}

function fieldsFrom(row: HostRow): Fields {
	return {
		address: row.address ?? "",
		user: row.user ?? "",
		keyPath: row.keyPath ?? "",
		evenerPath: row.evenerPath ?? "",
		configPath: row.configPath ?? "",
		addr: row.addr ?? "",
		roots: rootsToText(row.roots),
	};
}

function entryFrom(fields: Fields): HostEntry {
	return {
		address: fields.address.trim(),
		user: fields.user.trim(),
		keyPath: fields.keyPath.trim(),
		evenerPath: fields.evenerPath.trim(),
		configPath: fields.configPath.trim(),
		addr: fields.addr.trim(),
		roots: rootsFromText(fields.roots),
	};
}

// The form keeps what was typed while the page polls: it starts from the row
// once and never re-reads it.
function HostEditForm({ navigation, route, row, hosts }: Props & { row: HostRow; hosts: HostsController }) {
	const { name } = route.params;
	const [fields, setFields] = useState(() => fieldsFrom(row));
	const [saving, setSaving] = useState(false);
	const [error, setError] = useState<{ field: EditableHostField | null; message: string } | null>(null);
	useLayoutEffect(() => {
		const save = async () => {
			setSaving(true);
			setError(null);
			try {
				await hosts.update(name, entryFrom(fields));
				navigation.goBack();
			} catch (refusal) {
				const field = hostFieldError(refusal);
				setError({
					field: HOST_ENTRY_FIELD_ORDER.find((candidate) => candidate === field) ?? null,
					message: friendlyErrorMessage(refusal),
				});
			} finally {
				setSaving(false);
			}
		};
		navigation.setOptions({
			title: `Edit ${name}`,
			headerLeft: () => <HeaderButton label="Cancel" onPress={() => navigation.goBack()} />,
			headerRight: () => <HeaderButton label="Save" emphasized disabled={saving} onPress={() => void save()} />,
		});
	}, [navigation, name, hosts, fields, saving]);
	return (
		<GroupedPage>
			<SheetStatus />
			{error && error.field === null ? <GroupFooter tone="danger">{error.message}</GroupFooter> : null}
			{HOST_ENTRY_FIELD_ORDER.map((field) => (
				<Field
					key={field}
					field={field}
					value={fields[field]}
					error={error?.field === field ? error.message : null}
					disabled={saving}
					onChange={(text) => setFields((current) => ({ ...current, [field]: text }))}
				/>
			))}
		</GroupedPage>
	);
}

function Field({
	field,
	value,
	error,
	disabled,
	onChange,
}: {
	field: EditableHostField;
	value: string;
	error: string | null;
	disabled: boolean;
	onChange(text: string): void;
}) {
	const { label, help } = HOST_ENTRY_FIELD_TEXT[field];
	return (
		<>
			<GroupLabel>{label}</GroupLabel>
			<Group>
				<TextFieldRow
					label={label}
					value={value}
					onChangeText={onChange}
					multiline={field === "roots"}
					disabled={disabled}
				/>
			</Group>
			<GroupFooter>{help}</GroupFooter>
			{error ? <GroupFooter tone="danger">{error}</GroupFooter> : null}
		</>
	);
}
