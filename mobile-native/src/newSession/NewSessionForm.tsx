// New session's form (spec 11): the prompt first, then where the session runs
// and which agent runs it, with Cancel and Start in the header. Start is inert,
// and the sheet says why, while startBlock finds a reason (rulings 19 to 21);
// a hub that refuses the start leaves the sheet open with its reason. Only
// Cancel discards the draft, and it asks first when there is something to lose
// (ruling 18).
import { StackActions, useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { Alert, Pressable, Text, TextInput, View } from "react-native";
import { useStore } from "zustand";
import { CreationPlugins } from "../CreationPlugins";
import { creationImageDraft } from "../creationImageDraft";
import { destructiveButton } from "../haptics";
import { useOptionalSnapshot } from "../hosts/useHubFleet";
import { ImageAttachments } from "../ImageAttachments";
import { ImageSelection } from "../imageSelection";
import { nativeImagePicker } from "../nativeImagePicker";
import { creationModel } from "../newSession";
import { Group, GroupedPage, GroupFooter, GroupGap, GroupLabel, Row, RowValue, Segmented } from "../sheet/Grouped";
import { HeaderButton } from "../sheet/HeaderButton";
import { SheetStatus } from "../sheet/SheetStatus";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { effortLabel, projectName, setupOf } from "./launchSetup";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";
import { hostReach, startBlock } from "./startGate";

/** The prompt grows to six lines, then scrolls. */
const PROMPT_LINES = 6;
const PROMPT_LINE_HEIGHT = 22;

export function NewSessionForm({ navigation }: NativeStackScreenProps<NewSessionRoutes, "Form">) {
	const { store, hubId, client, ready, hosts, memory, hostLabel } = useNewSession();
	const { palette } = useColors();
	const scale = useTextScale();
	const form = useStore(store);
	const imageDocument = useMemo(() => creationImageDraft(store), [store]);
	const imageSelection = useMemo(() => new ImageSelection(imageDocument, nativeImagePicker), [imageDocument]);
	const imageState = useSyncExternalStore(imageSelection.subscribe, imageSelection.getSnapshot);
	useFocusEffect(useCallback(() => () => imageSelection.cancel(), [imageSelection]));
	// The Start gate reads the hub's hosts, so they are kept current while the
	// form is on screen.
	useFocusEffect(useCallback(() => hosts?.start(), [hosts]));
	const hostRows = useOptionalSnapshot(hosts)?.rows ?? null;
	const reach = hostReach(form.source, hostRows);
	const model = creationModel(form.models, form.model, form.launchOverrides);
	const block = startBlock({
		ready,
		busy: !form.storageLoaded || form.submitting || form.loadingModels || form.movingHost || imageState.busy,
		cwd: form.cwd,
		host: form.source,
		hostLabel: hostLabel(form.source),
		reach,
		// The plugin checklist's blocking problems join in PR 10; until then
		// submit's own plugin check refuses them and says why.
		pluginIssues: [],
		// submit starts on a chosen model only once the host's list has it.
		unconfirmedModel: model && form.modelError ? model.displayName || model.model : null,
	});
	const latest = useRef({ ready, client, blocked: block !== null });
	latest.current = { ready, client, blocked: block !== null };
	const close = useCallback(() => navigation.getParent()?.goBack(), [navigation]);
	const start = useCallback(async () => {
		if (!latest.current.ready || latest.current.blocked || imageSelection.getSnapshot().busy) return;
		const submittedClient = latest.current.client;
		const setup = setupOf(store.getState());
		const outcome = await store.getState().submit();
		if (outcome.status !== "created") return;
		try {
			memory.recordStart(setup, Date.now());
		} catch {
			// A start this phone couldn't remember still opens its session.
		}
		if (!navigation.isFocused() || !latest.current.ready || latest.current.client !== submittedClient) return;
		navigation.getParent()?.dispatch(
			StackActions.replace("Conversation", {
				hubId: outcome.hubId,
				ref: outcome.thread.evener.ref,
				title: outcome.thread.name || "Conversation",
			}),
		);
	}, [store, memory, navigation, imageSelection]);
	const cancel = useCallback(() => {
		const { prompt, images } = store.getState();
		if (!prompt.trim() && images.length === 0) {
			close();
			return;
		}
		Alert.alert("Delete this draft?", undefined, [
			{ text: "Keep draft", style: "cancel", onPress: close },
			destructiveButton("Delete draft", () => {
				store.getState().discard();
				close();
			}),
		]);
	}, [store, close]);
	const blocked = block !== null;
	useLayoutEffect(() => {
		navigation.setOptions({
			title: "New session",
			headerLeft: () => <HeaderButton label="Cancel" onPress={cancel} />,
			headerRight: () => <HeaderButton label="Start" strong disabled={blocked} onPress={() => void start()} />,
		});
	}, [navigation, cancel, start, blocked]);

	const editable = form.storageLoaded && !form.submitting;
	const levels = model?.reasoningEffortLevels ?? [];
	const offline = reach === "offline";
	const where = hostLabel(form.source);
	return (
		<GroupedPage>
			<SheetStatus />
			<GroupGap />
			<Group>
				<View style={{ paddingHorizontal: 16, paddingVertical: 11, gap: 8 }}>
					<TextInput
						accessibilityLabel="What should the agent do?"
						placeholder="What should the agent do?"
						placeholderTextColor={palette.inkLow}
						autoFocus
						multiline
						value={form.prompt}
						editable={editable}
						onChangeText={form.setPrompt}
						allowFontScaling={allowFontScaling}
						style={{
							color: palette.inkHi,
							fontSize: 17 * scale,
							lineHeight: PROMPT_LINE_HEIGHT * scale,
							minHeight: 3 * PROMPT_LINE_HEIGHT * scale,
							maxHeight: PROMPT_LINES * PROMPT_LINE_HEIGHT * scale,
							textAlignVertical: "top",
							padding: 0,
						}}
					/>
					<ImageAttachments document={imageDocument} selection={imageSelection} disabled={!editable} />
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Attach images"
						accessibilityState={{ disabled: !editable || imageState.busy }}
						disabled={!editable || imageState.busy}
						onPress={() => void imageSelection.choose()}
						style={{
							minWidth: 44,
							minHeight: 44,
							alignSelf: "flex-start",
							justifyContent: "center",
							opacity: !editable || imageState.busy ? 0.4 : 1,
						}}
					>
						<Text allowFontScaling={allowFontScaling} style={{ color: palette.accentInk, fontSize: 24 * scale }}>
							+
						</Text>
					</Pressable>
				</View>
			</Group>
			{imageState.error ? <GroupFooter tone="danger">{imageState.error}</GroupFooter> : null}
			{block?.message ? <GroupFooter tone="danger">{block.message}</GroupFooter> : null}
			{form.error ? <GroupFooter tone="danger">{form.error}</GroupFooter> : null}
			{form.storageError ? (
				<>
					<GroupFooter tone="danger">{form.storageError}</GroupFooter>
					<Group>
						<Row
							label={form.storageLoaded ? "Retry saving draft" : "Retry loading draft"}
							tone="accent"
							disabled={form.submitting}
							onPress={form.retryStorage}
						/>
					</Group>
				</>
			) : null}
			<GroupLabel>Where</GroupLabel>
			<Group>
				<Row
					icon="server.rack"
					label="Host"
					tone={block?.field === "host" ? "danger" : "normal"}
					value={<RowValue text={where} tag={offline ? { text: "Offline", tone: "amber" } : null} />}
					accessibilityLabel={["Host", where, offline ? "Offline" : null].filter(Boolean).join(", ")}
					chevron
					onPress={() => navigation.navigate("Host")}
				/>
				<Row
					icon="folder"
					label="Project"
					tone={block?.field === "project" ? "danger" : "normal"}
					value={form.cwd.trim() ? projectName(form.cwd.trim()) : "Choose a project"}
					chevron
					onPress={() => navigation.navigate("Project")}
				/>
			</Group>
			{form.hostNote ? <GroupFooter>{form.hostNote}</GroupFooter> : null}
			<GroupLabel>Agent</GroupLabel>
			<Group>
				<Row
					icon="cpu"
					label="Model"
					tone={block?.field === "model" ? "danger" : "normal"}
					sub={model ? `via ${model.provider}` : undefined}
					value={model ? model.displayName || model.model : "Hub default"}
					chevron
					onPress={() => navigation.navigate("Model")}
				/>
				{levels.length > 0 ? (
					<View key="effort" style={{ paddingBottom: 12 }}>
						<Row label="Effort" sub="How long it thinks before acting" />
						<Segmented
							label="Effort"
							options={levels.map((level) => ({ value: level, label: effortLabel(level) }))}
							value={form.reasoning || null}
							onChange={form.setReasoning}
							disabled={!editable}
						/>
					</View>
				) : null}
			</Group>
			<GroupGap />
			<Group>
				{client ? (
					<View key="plugins" style={{ paddingHorizontal: 16, paddingVertical: 4 }}>
						<CreationPlugins
							key={JSON.stringify([hubId, form.source, form.cwd.trim()])}
							client={client}
							host={form.source}
							cwd={form.cwd.trim()}
							value={form.launchOverrides}
							onChange={form.setLaunchOverrides}
							disabled={!ready || !editable}
						/>
					</View>
				) : null}
				<Row
					label="Session options"
					chevron
					disabled={!form.cwd.trim()}
					onPress={() => navigation.navigate("SessionOptions")}
				/>
			</Group>
			<GroupFooter>
				Host, plugins and access are fixed once the session starts. Model and effort can change later.
			</GroupFooter>
		</GroupedPage>
	);
}
