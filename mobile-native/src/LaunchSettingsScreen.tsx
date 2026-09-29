import { useFocusEffect, usePreventRemove } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { Pressable, ScrollView, TextInput, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { inactivePromptDependent, LAUNCH_CHANGED_ELSEWHERE, LaunchSettings } from "@evener/appwire-client";
import type { LaunchConfigLayer, LaunchConfigLayerName, LaunchOption } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { LaunchFieldEditor } from "./LaunchFieldEditor";
import { scalarKinds } from "./launchScalar";
import { RepositoryLaunchReview } from "./RepositoryLaunchReview";
import { HUB_NO_LONGER_SELECTED } from "./retainedScreen";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
import { Spinner } from "./sheet/Spinner";
import { confirmDiscard } from "./sheet/confirmDiscard";

type Props = NativeStackScreenProps<Routes, "LaunchSettings">;
export function LaunchSettingsScreen({ route, navigation }: Props) {
	const { activeProfile, client, state } = useConnection();
	if (activeProfile?.id !== route.params.hubId) return <Copy>{HUB_NO_LONGER_SELECTED}</Copy>;
	const cwd = route.params.projectCwd ?? "/";
	const layer = route.params.projectCwd === undefined ? "global" : "project";
	return (
		<LaunchDefaults
			key={JSON.stringify([activeProfile.id, layer, cwd])}
			cwd={cwd}
			layer={layer}
			client={state === "ready" ? client : null}
			hubName={activeProfile.name}
			navigation={navigation}
		/>
	);
}
function scalarValue(config: LaunchConfigLayer | null, field: string): string {
	const value = (config as Record<string, unknown> | null)?.[field];
	if (value && typeof value === "object" && !Array.isArray(value)) return Object.keys(value).join(", ");
	if (Array.isArray(value))
		return value.length ? value.map((item) => (typeof item === "string" ? item : item.name)).join(" → ") : "None";
	return value === undefined ? "" : String(value);
}
function LaunchDefaults({
	cwd,
	layer,
	client,
	hubName,
	navigation,
}: {
	cwd: string;
	layer: LaunchConfigLayerName;
	client: ConversationClientLike | null;
	hubName: string;
	navigation: Props["navigation"];
}) {
	const colors = useColors();
	const model = useMemo(() => new LaunchSettings(null, cwd, layer), [cwd, layer]);
	const state = useSyncExternalStore(model.subscribe, model.getSnapshot);
	const [selected, setSelected] = useState<LaunchOption | null>(null);
	const [query, setQuery] = useState("");
	const [notice, setNotice] = useState<string | null>(null);
	const alive = useRef(true);
	useEffect(() => {
		alive.current = true;
		return () => {
			alive.current = false;
			model.dispose();
		};
	}, [model]);
	useEffect(() => {
		void model.setConnection(client);
	}, [model, client]);
	// The page keeps itself current (principle 2): after a failed read, of
	// the layer or of its effective values, it reads again each time the page
	// comes back into view. The store never reads over an unsaved edit.
	useFocusEffect(
		useCallback(() => {
			const { error, resolveError } = model.getSnapshot();
			if (error || resolveError) void model.refresh();
		}, [model]),
	);
	usePreventRemove(state.dirty || state.saving, ({ data }) => {
		if (state.saving) return;
		confirmDiscard(() => navigation.dispatch(data.action));
	});
	const options = (state.options ?? []).filter(
		(option) =>
			option.defaultableLayers?.includes(layer) &&
			(scalarKinds.has(option.kind) ||
				option.kind === "envMap" ||
				option.kind === "modelList" ||
				option.kind === "pathList" ||
				option.kind === "mcpServerList") &&
			!inactivePromptDependent(option.wireField, state.draft ?? {}) &&
			`${option.label} ${option.group} ${option.description ?? ""}`.toLowerCase().includes(query.toLowerCase()),
	);
	return (
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
			<ScrollView keyboardShouldPersistTaps="handled" contentContainerStyle={{ padding: 20, gap: 10 }}>
				<Copy>{hubName}</Copy>
				{!client && <Copy>Disconnected. Your unsaved changes are kept here.</Copy>}
				<Copy muted>
					{layer === "project"
						? `Defaults for new Evener sessions in ${cwd}. These override hub and trusted repository settings; per-launch values can override them.`
						: "Defaults for new Evener sessions. Project and per-launch settings can override these values."}
				</Copy>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<Action
						disabled={!client || !state.dirty || state.loading || state.saving || state.changedElsewhere}
						onPress={() => {
							setNotice(null);
							void model.save().then((saved) => {
								if (saved && alive.current) setNotice("Launch defaults saved.");
							});
						}}
					>
						Save defaults
					</Action>
					{state.dirty && state.changedElsewhere ? (
						<Action
							disabled={!client || state.loading || state.saving}
							onPress={() =>
								confirmDiscard(() => {
									setNotice(null);
									void model.refresh(true);
								})
							}
						>
							Discard changes
						</Action>
					) : null}
				</View>
				{state.loading && <Spinner label="Loading launch defaults" />}
				{state.saving && <Spinner label="Saving launch defaults" />}
				<ErrorMessage message={state.error} />
				<ErrorMessage message={state.dirty && state.changedElsewhere ? LAUNCH_CHANGED_ELSEWHERE : null} />
				<ErrorMessage message={state.resolveError} />
				{notice && <Copy>{notice}</Copy>}
				{state.resolved?.diagnostics?.map((d) => (
					<Copy muted key={JSON.stringify(d)}>
						{d.field ? `${d.field}: ${d.message}` : d.message}
					</Copy>
				))}
				{layer === "project" && (
					<RepositoryLaunchReview
						hubName={hubName}
						repo={state.resolved?.repo}
						disabled={!client || state.loading || state.saving || state.dirty}
						error={state.error}
						trust={model.trustRepository}
					/>
				)}
				<TextInput
					accessibilityLabel="Search launch settings"
					value={query}
					onChangeText={setQuery}
					placeholder="Find a setting"
					placeholderTextColor={colors.secondary}
					style={[styles.input, { color: colors.text, borderColor: colors.border }]}
				/>
				{options.map((option, index) => {
					const displayValue =
						scalarValue(state.draft, option.wireField) ||
						`Inherited · ${scalarValue(state.resolved?.effective ?? null, option.wireField) || "Default"}`;
					return (
						<View key={option.wireField}>
							{(index === 0 || options[index - 1]?.group !== option.group) && (
								<View style={{ paddingTop: 14, paddingBottom: 4 }}>
									<Copy muted>{option.group}</Copy>
								</View>
							)}
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`Edit ${option.label}`}
								accessibilityValue={{ text: displayValue }}
								accessibilityHint="Opens the setting editor"
								accessibilityState={{
									disabled: state.loading || state.saving,
								}}
								disabled={state.loading || state.saving}
								onPress={() => setSelected(option)}
								style={{
									minHeight: 48,
									paddingVertical: 7,
									borderBottomWidth: 0.5,
									borderColor: colors.border,
								}}
							>
								<Copy>{option.label}</Copy>
								<Copy muted numberOfLines={2}>
									{displayValue}
								</Copy>
							</Pressable>
						</View>
					);
				})}
			</ScrollView>
			{selected && (
				<LaunchFieldEditor
					key={selected.wireField}
					option={selected}
					value={state.draft ?? {}}
					effective={state.resolved?.effective ?? {}}
					client={client}
					close={() => setSelected(null)}
					apply={(value) => {
						model.edit(selected.wireField as keyof LaunchConfigLayer, value);
						setNotice(null);
						setSelected(null);
					}}
				/>
			)}
		</SafeAreaView>
	);
}
