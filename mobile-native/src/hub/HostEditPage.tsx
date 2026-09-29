// Edit a host in the Hub (spec 12): every field the web's Edit dialog has,
// under its labels and help lines, filled from the host's row. Save sends the
// guarded update and goes back; the hub is the one validator, so Save holds
// only while it saves or the hub is away, and a refusal that names a field
// lands under it. The page goes back if its host leaves the list.
import {
	type EditableHostField,
	friendlyErrorMessage,
	HOST_CHANGED_MESSAGE,
	HOST_ENTRY_FIELD_ORDER,
	HOST_ENTRY_FIELD_TEXT,
	type HostEntry,
	type HostRow,
	hostChangedSinceOpened,
	hostFieldError,
	rootsFromText,
	rootsToText,
} from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { type ScrollView, View } from "react-native";
import type { HostsController } from "../hosts/hostsController";
import { Group, GroupedPage, GroupFooter, TextFieldRow } from "../sheet/Grouped";
import { HeaderButton } from "../sheet/HeaderButton";
import { useSheet } from "../sheet/useSheet";
import { SheetStatus } from "../sheet/SheetStatus";
import { HostsNotListed } from "../hosts/HostsNotListed";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen, useLeavesWithHost } from "./useHostsOnScreen";

type Props = NativeStackScreenProps<HubRoutes, "HostEdit">;
type Fields = Record<EditableHostField, string>;

export function HostEditPage(props: Props) {
	const { hubName, hosts } = useHubSheet();
	const { state, loadError } = useHostsOnScreen();
	const row = state?.rows?.find((candidate) => candidate.name === props.route.params.name);
	useLeavesWithHost(state ? !!row : null, props.navigation.goBack);
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
	const { ready } = useHubSheet();
	const [fields, setFields] = useState(() => fieldsFrom(row));
	// What the page opened with: typed back to it, there is nothing to lose.
	const [original] = useState(() => fieldsFrom(row));
	const dirty = HOST_ENTRY_FIELD_ORDER.some((field) => fields[field] !== original[field]);
	// The pair of the row the form opened on: the edit speaks for what the
	// person saw, so a host changed since then refuses instead of being
	// overwritten.
	const [opened] = useState(() => ({ generation: row.generation, incarnationId: row.incarnationId }));
	const [saving, setSaving] = useState(false);
	// Cancel, a swipe and Back ask before an edit goes (spec 6), and wait out
	// a save in flight; a save that lands leaves without asking.
	const sheet = useSheet({ dirty, busy: saving });
	// Two taps land before the header re-renders disabled; one update goes.
	const inFlight = useRef(false);
	const [error, setError] = useState<{ field: EditableHostField | null; message: string } | null>(null);
	// Save sits in the header, so the person may be anywhere on the page when a
	// refusal lands: the page scrolls to the field it names, or to the top for
	// one above the fields, so Save never looks like it did nothing.
	const page = useRef<ScrollView>(null);
	const fieldTops = useRef<Partial<Record<EditableHostField, number>>>({});
	useEffect(() => {
		if (error) page.current?.scrollTo({ y: error.field ? (fieldTops.current[error.field] ?? 0) : 0, animated: true });
	}, [error]);
	useLayoutEffect(() => {
		const save = async () => {
			if (inFlight.current) return;
			inFlight.current = true;
			setSaving(true);
			setError(null);
			try {
				await hosts.update(name, entryFrom(fields), opened);
				// A host gone from the re-read is useLeavesWithHost's to leave with,
				// and a page already swiped away has left: going back here too
				// would pop the page beneath.
				const listed = hosts.getSnapshot().rows?.some((candidate) => candidate.name === name);
				if (listed && navigation.isFocused()) sheet.finish();
			} catch (refusal) {
				const field = hostFieldError(refusal);
				setError({
					field: HOST_ENTRY_FIELD_ORDER.find((candidate) => candidate === field) ?? null,
					message: hostChangedSinceOpened(refusal) ? HOST_CHANGED_MESSAGE : friendlyErrorMessage(refusal),
				});
			} finally {
				inFlight.current = false;
				setSaving(false);
			}
		};
		navigation.setOptions({
			title: `Edit ${name}`,
			// Cancel holds while Save runs: a save that lands goes back itself.
			headerLeft: () => <HeaderButton label="Cancel" disabled={saving} onPress={sheet.close} />,
			headerRight: () => <HeaderButton label="Save" strong disabled={saving || !ready} onPress={() => void save()} />,
		});
	}, [navigation, name, hosts, fields, saving, ready, opened, sheet]);
	return (
		<GroupedPage scrollRef={page}>
			<SheetStatus />
			{error && error.field === null ? <GroupFooter tone="danger">{error.message}</GroupFooter> : null}
			{HOST_ENTRY_FIELD_ORDER.map((field) => (
				<View
					key={field}
					testID={`host-field-${field}`}
					onLayout={(event) => {
						fieldTops.current[field] = event.nativeEvent.layout.y;
					}}
				>
					<Field
						field={field}
						value={fields[field]}
						error={error?.field === field ? error.message : null}
						disabled={saving}
						onChange={(text) => setFields((current) => ({ ...current, [field]: text }))}
					/>
				</View>
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
			<Group label={label}>
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
