import { type ReactNode, type Ref, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { FlatList, Platform, Pressable, TextInput, View } from "react-native";
import { SymbolView } from "expo-symbols";
import { buildPickerRows, type PickerRow } from "@evener/appwire-client";
import type { ConversationClientLike } from "./conversation/services/conversation";
import { HubModels } from "./hubModels";
import { Copy, ErrorMessage, styles, useColors, useTextScale } from "./ui";
import { Spinner } from "./sheet/Spinner";

export function LaunchModelPicker({
	client,
	value,
	onChange,
	header,
	scrollRef,
}: {
	client: ConversationClientLike | null;
	value: string;
	onChange(value: string): void;
	header: ReactNode;
	scrollRef?: Ref<FlatList<PickerRow>>;
}) {
	const colors = useColors();
	const scale = useTextScale();
	const model = useMemo(() => new HubModels(client), [client]);
	const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
	const [query, setQuery] = useState("");
	useEffect(() => {
		void model.refresh();
		return () => model.dispose();
	}, [model]);
	const rows = useMemo(() => buildPickerRows(state.catalog, query), [state.catalog, query]);
	return (
		<FlatList
			ref={scrollRef}
			style={styles.fill}
			contentContainerStyle={{ padding: 20, paddingBottom: 28 }}
			automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
			keyboardShouldPersistTaps="handled"
			keyboardDismissMode="on-drag"
			data={rows}
			keyExtractor={(row) => row.key}
			ListHeaderComponent={
				<View style={{ gap: 12, paddingBottom: 12 }}>
					{header}
					{value ? <Copy>{`Selected · ${value}`}</Copy> : null}
					<TextInput
						accessibilityLabel="Search launch models"
						placeholder="Search models or providers"
						placeholderTextColor={colors.secondary}
						value={query}
						onChangeText={setQuery}
						autoCapitalize="none"
						autoCorrect={false}
						style={[styles.input, { color: colors.text, borderColor: colors.border }]}
					/>
					{!client && <Copy>You can browse this hub's models once it's back. Your selection is kept.</Copy>}
					<ErrorMessage message={state.error} />
					{state.loading && <Spinner label="Loading launch models" />}
					{state.error && (
						// The list reads again on its own when it can (the calm
						// recovery owns the retry), so this only says where it stands.
						<View testID="model-list-status">
							<Copy muted>The model list will load again on its own. Your selection is kept.</Copy>
						</View>
					)}
				</View>
			}
			ListEmptyComponent={
				client && !state.loading && !state.error ? (
					<Copy muted>{query.trim() ? "No models match your search." : "No models are available from this hub."}</Copy>
				) : null
			}
			renderItem={({ item }) => {
				if (item.kind === "group")
					return (
						<View style={{ paddingTop: 16, paddingBottom: 6 }}>
							<Copy muted>{item.label}</Copy>
						</View>
					);
				if (item.kind === "unavailable")
					return (
						<View style={{ paddingVertical: 8 }}>
							<Copy muted>{item.text}</Copy>
						</View>
					);
				if (item.kind === "warning")
					return (
						<View
							style={{
								flexDirection: "row",
								alignItems: "center",
								gap: 6,
								paddingLeft: 16,
								paddingRight: 8,
								paddingBottom: 4,
							}}
						>
							<SymbolView name="exclamationmark.triangle.fill" tintColor={colors.secondary} size={13 * scale} />
							<View style={{ flex: 1, minWidth: 0 }}>
								<Copy muted>{item.text}</Copy>
							</View>
						</View>
					);
				const selected = value === item.option.qualified;
				return (
					<Pressable
						accessibilityRole="radio"
						accessibilityState={{
							checked: selected,
							disabled: !client || state.loading,
						}}
						accessibilityLabel={`Choose ${item.option.label} · ${item.option.entry.provider}`}
						disabled={!client || state.loading}
						onPress={() => onChange(item.option.qualified)}
						style={{
							minHeight: 48,
							paddingVertical: 10,
							borderBottomWidth: 0.5,
							borderColor: colors.border,
						}}
					>
						<View style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
							{selected ? (
								<SymbolView name="checkmark" tintColor={colors.accent} size={14 * scale} weight="semibold" />
							) : null}
							<View style={{ flex: 1, minWidth: 0 }}>
								<Copy>{item.option.label}</Copy>
							</View>
						</View>
						{item.meta ? <Copy muted>{item.meta}</Copy> : null}
					</Pressable>
				);
			}}
		/>
	);
}
