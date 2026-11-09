import { useEffect, useRef, useState } from "react";
import { ActivityIndicator, type FlatList, KeyboardAvoidingView, Platform, TextInput, View } from "react-native";
import { friendlyErrorMessage, type LaunchOption, type PickerRow, schemaPathKind } from "@evener/appwire-client";
import type { ConversationClientLike } from "./conversation/services/conversation";
import { HubPathField } from "./HubPathField";
import { LaunchModelPicker } from "./LaunchModelPicker";
import { assertLaunchFieldCurrent, launchFieldConflictMessage, parseLaunchScalar } from "./launchScalar";
import { allowFontScaling, ErrorMessage, styles, useColors, useTextScale } from "./ui";
import { Group, GroupedPage, GroupFooter, Row, TextFieldRow, useErrorInView, useFormError } from "./sheet/Grouped";
import { ModalSheet } from "./sheet/ModalSheet";
import { space, uiType } from "./design/tokens";

export function LaunchScalarEditor({
	option,
	value,
	effective,
	client,
	close,
	apply,
}: {
	option: LaunchOption;
	value: string;
	effective: string;
	client: ConversationClientLike | null;
	close(): void;
	apply(value: string | number | boolean | undefined): void;
}) {
	const colors = useColors();
	const scale = useTextScale();
	const [raw, setRaw] = useState(value);
	const originalValue = useRef(value);
	const currentValue = useRef(value);
	currentValue.current = value;
	const [busy, setBusy] = useState(false);
	const [error, setError] = useFormError();
	const page = useErrorInView(error);
	const modelList = useRef<FlatList<PickerRow>>(null);
	useEffect(() => {
		// Each refusal has its own report, even when its words haven't changed.
		if (error) modelList.current?.scrollToOffset({ offset: 0, animated: true });
	}, [error]);
	const alive = useRef(true);
	useEffect(
		() => () => {
			alive.current = false;
		},
		[],
	);
	const validation = useRef(0);
	// biome-ignore lint/correctness/useExhaustiveDependencies: A new transport invalidates validation replies from the old connection.
	useEffect(() => {
		validation.current += 1;
		setBusy(false);
	}, [client]);
	async function done() {
		if (busy) return;
		const generation = ++validation.current;
		const active = () => alive.current && generation === validation.current;
		setError(null);
		setBusy(true);
		try {
			const parsed = parseLaunchScalar(option, raw);
			if (parsed !== undefined && option.kind === "path") {
				if (!client) {
					setError("This path can be checked on the hub once it's back.");
					return;
				}
				const validation = await client.request("evener/path/validate", {
					path: String(parsed),
					kind: schemaPathKind(option.pathKind),
				});
				if (!validation.valid) {
					if (active()) setError(validation.error || "This path is not valid on the hub.");
					return;
				}
			}
			if (active()) {
				assertLaunchFieldCurrent(originalValue.current, currentValue.current, parsed);
				apply(parsed);
			}
		} catch (err) {
			if (active())
				setError(
					err instanceof Error &&
						[
							"Enter a whole number.",
							"Choose an available value.",
							"Choose an on or off value.",
							launchFieldConflictMessage,
						].includes(err.message)
						? err.message
						: friendlyErrorMessage(err),
				);
		} finally {
			if (active()) setBusy(false);
		}
	}
	const choices =
		option.kind === "boolean"
			? [
					{ value: "true", label: "On" },
					{ value: "false", label: "Off" },
				]
			: option.choices?.filter((choice) => choice.value !== "");
	const fieldHeader = (
		<>
			<View style={{ paddingHorizontal: space.labelInset }}>
				<ErrorMessage message={error?.message ?? null} />
			</View>
			{option.description && <GroupFooter>{option.description}</GroupFooter>}
			<Group>
				<Row label="Effective value" sub={effective || "Unavailable"} selectable />
				<Row label="Use inherited value" checked={raw === ""} disabled={busy} onPress={() => setRaw("")} />
			</Group>
		</>
	);
	return (
		<ModalSheet
			title={option.label}
			onRequestClose={close}
			onCancel={close}
			done={{
				disabled: busy,
				busy,
				onPress: () => {
					void done();
				},
			}}
		>
			<KeyboardAvoidingView
				style={styles.fill}
				behavior={Platform.OS === "android" ? "height" : undefined}
				enabled={Platform.OS === "android"}
			>
				{option.kind === "modelPicker" ? (
					<LaunchModelPicker client={client} value={raw} onChange={setRaw} header={fieldHeader} scrollRef={modelList} />
				) : (
					<GroupedPage scrollRef={page}>
						{fieldHeader}
						<Group label={option.label}>
							{choices ? (
								choices.map((choice) => (
									<Row
										key={choice.value}
										label={choice.label}
										checked={raw === choice.value}
										disabled={busy || !!choice.disabled}
										onPress={() => setRaw(choice.value)}
									/>
								))
							) : client && option.kind === "path" ? (
								<HubPathField
									kind={option.pathKind === "file" || option.pathKind === "outputFile" ? option.pathKind : "dir"}
									client={client}
									label={option.label}
									value={raw}
									onChange={setRaw}
									disabled={busy}
								/>
							) : option.kind === "integer" ? (
								<TextInput
									accessibilityLabel={option.label}
									value={raw}
									onChangeText={setRaw}
									editable={!busy}
									autoCorrect={false}
									autoCapitalize="none"
									keyboardType="numbers-and-punctuation"
									allowFontScaling={allowFontScaling}
									style={{
										color: colors.text,
										fontSize: uiType.listRow.fontSize * scale,
										minHeight: 44,
										paddingHorizontal: space.rowInset,
										paddingVertical: space.rowPadding,
									}}
								/>
							) : (
								<TextFieldRow
									label={option.label}
									value={raw}
									onChangeText={setRaw}
									disabled={busy}
									multiline={option.kind === "multilineText"}
								/>
							)}
						</Group>
						{busy && <ActivityIndicator accessibilityLabel="Validating setting" />}
					</GroupedPage>
				)}
			</KeyboardAvoidingView>
		</ModalSheet>
	);
}
