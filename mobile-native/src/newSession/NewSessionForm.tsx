// New session's form (spec 11): the prompt first, then where the session runs
// and which agent runs it, with Cancel and Start in the header. Start is inert,
// and the sheet says why, while startBlock finds a reason (rulings 19 to 21);
// a hub that refuses the start leaves the sheet open with its reason. Only
// Cancel discards the draft, and it asks first when there is something to lose
// (ruling 18).
import { StackActions, useFocusEffect } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { Alert, Pressable, Text, TextInput, View } from "react-native";
import { useStore } from "zustand";
import { useOfferAlert, useStartFailureSeen } from "../alerts/alertsContext";
import { creationImageDraft } from "../creationImageDraft";
import { space } from "../design/tokens";
import { destructiveButton } from "../haptics";
import { useOptionalSnapshot } from "../hosts/useHubFleet";
import { ImageAttachments } from "../ImageAttachments";
import { ImageSelection } from "../imageSelection";
import { nativeImagePicker } from "../nativeImagePicker";
import { creationModel, startedSetup } from "../newSession";
import { Group, GroupedPage, GroupFooter, Row, RowValue, Segmented } from "../sheet/Grouped";
import { HeaderButton } from "../sheet/HeaderButton";
import { SheetStatus } from "../sheet/SheetStatus";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { NewSessionService } from "../../../mobile/src/services/newSession";
import { effortLabel, knownAccess, projectName } from "./launchSetup";
import { formFront, showForm } from "./formFront";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";
import { pluginChoice } from "./sheetPlugins";
import { hostReach, startBlock } from "./startGate";
import { useHostRead } from "./useHostRead";

/** The prompt grows to six lines, then scrolls. */
const PROMPT_LINES = 6;
const PROMPT_LINE_HEIGHT = 22;

/** The project's current branch, or null outside a repository. */
const readBranch = (service: NewSessionService, host: string, cwd: string) => service.branch(host, cwd);

/** The launch settings More options sets. */
const MORE_OPTIONS = ["contextStrategy", "maxSubagentDepth", "maxRounds"] as const;

export function NewSessionForm({ navigation }: NativeStackScreenProps<NewSessionRoutes, "Form">) {
	const { store, hubId, hubName, client, ready, hosts, memory, hostLabel, plugins, launchDefaults } = useNewSession();
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
	const pluginsChosen = pluginChoice(form.launchOverrides, plugins);
	const branch = useHostRead(client, ready, form.source, form.cwd, readBranch);
	// The Plugins row: what the preview says, or why there is nothing to count.
	const pluginsLine: { sub?: string; subTone?: "danger"; value?: string } = !form.cwd.trim()
		? { sub: "Listed once a project is chosen" }
		: pluginsChosen.response
			? {
					sub: pluginsChosen.issues.length > 0 ? `${pluginsChosen.issues.length} need attention` : undefined,
					subTone: "danger",
					value: `${pluginsChosen.on.length} of ${pluginsChosen.total}`,
				}
			: plugins.status === "error"
				? { sub: "Couldn't list this host's plugins", subTone: "danger" }
				: { value: "…" };
	const access = knownAccess(form.launchOverrides.sandbox, launchDefaults);
	const startMayRepeat = form.startMayRepeat();
	const block = startBlock({
		ready,
		busy: !form.storageLoaded || form.submitting || form.loadingModels || form.movingHost || imageState.busy,
		cwd: form.cwd,
		host: form.source,
		hostLabel: hostLabel(form.source),
		reach,
		pluginIssues: pluginsChosen.issues,
		// submit starts on a chosen model only once the host's list has it.
		unconfirmedModel: model && form.modelError ? model.displayName || model.model : null,
		startMayRepeat,
	});
	const offerAlert = useOfferAlert();
	const startFailureSeen = useStartFailureSeen(hubId);
	const latest = useRef({ ready, client, blocked: block !== null });
	latest.current = { ready, client, blocked: block !== null };
	useEffect(() => showForm(store, { navigation, latest }), [store, navigation]);
	// Whenever this form is in front, it shows the store's own error, so an
	// alert saying the same goes.
	useFocusEffect(startFailureSeen);
	const close = useCallback(() => navigation.getParent()?.goBack(), [navigation]);
	const start = useCallback(async () => {
		if (!latest.current.ready || latest.current.blocked || imageSelection.getSnapshot().busy) return;
		const submittedClient = latest.current.client;
		const setup = startedSetup(store.getState());
		const outcome = await store.getState().submit();
		// A removed hub's start has no one left to tell.
		if (store.getState().retired) return;
		// The form in front now: this one, or a sheet reopened while the start
		// was on its way, whether or not the hub is reachable from it.
		const front = formFront(store);
		const inFront = !!front && front.navigation.isFocused();
		if (outcome.status !== "created") {
			// A form in front shows the store's error itself; else an alert says
			// it. With no error there is nothing to say: nothing was started.
			// Uncertain only while a start of this very draft may exist, the same
			// measure Start holds on: a changed draft's failure is a plain one.
			const { error, startMayRepeat } = store.getState();
			if (inFront || !error) return;
			offerAlert({ kind: "startFailed", hubId, hubName, uncertain: startMayRepeat() });
			return;
		}
		try {
			memory.recordStart(setup, Date.now());
		} catch {
			// A start this phone couldn't remember still opens its session.
		}
		// Only a form in front, on the connection the start went out on, can
		// open the session.
		if (!front || !inFront || !front.latest.current.ready || front.latest.current.client !== submittedClient) {
			// The session exists but this sheet can no longer open it: say so
			// where the person is, so it isn't started twice (#3048).
			offerAlert({
				kind: "started",
				ref: outcome.thread.evener.ref,
				title: outcome.thread.name || "New session",
				why: null,
			});
			return;
		}
		front.navigation.getParent()?.dispatch(
			StackActions.replace("Conversation", {
				hubId: outcome.hubId,
				ref: outcome.thread.evener.ref,
				title: outcome.thread.name || "Conversation",
			}),
		);
	}, [store, hubId, hubName, memory, imageSelection, offerAlert]);
	const cancel = useCallback(() => {
		const { prompt, images, submitting } = store.getState();
		// A draft whose start is on its way can't be discarded: Cancel only closes.
		if (submitting || (!prompt.trim() && images.length === 0)) {
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
	const starting = form.submitting;
	useLayoutEffect(() => {
		navigation.setOptions({
			title: "New session",
			headerLeft: () => <HeaderButton label="Cancel" onPress={cancel} />,
			headerRight: () => (
				<HeaderButton label={starting ? "Starting…" : "Start"} strong disabled={blocked} onPress={() => void start()} />
			),
		});
	}, [navigation, cancel, start, blocked, starting]);

	const editable = form.storageLoaded && !form.submitting;
	const levels = model?.reasoningEffortLevels ?? [];
	const offline = reach === "offline";
	const where = hostLabel(form.source);
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				<View style={{ paddingHorizontal: space.rowInset, paddingVertical: space.rowPadding, gap: 8 }}>
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
			<Group label="Where">
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
				{/* Information only: the hub can't start a session on a new branch
				    (ruling 15), so there is nothing to choose. */}
				{branch ? <Row icon="arrow.triangle.branch" label="Branch" value={branch} /> : null}
			</Group>
			{form.hostNote ? <GroupFooter>{form.hostNote}</GroupFooter> : null}
			<Group label="Agent">
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
				<Row
					icon="puzzlepiece.extension"
					label="Plugins"
					tone={block?.field === "plugins" ? "danger" : "normal"}
					{...pluginsLine}
					chevron
					onPress={() => navigation.navigate("Plugins")}
				/>
				<Row
					icon="lock.shield"
					label="Access"
					sub={access?.detail || undefined}
					value={access?.label}
					chevron
					onPress={() => navigation.navigate("Access")}
				/>
				<Row
					label="More options"
					sub="Context strategy, subagent depth, turn limit"
					value={MORE_OPTIONS.some((field) => form.launchOverrides[field] !== undefined) ? "Custom" : undefined}
					chevron
					onPress={() => navigation.navigate("MoreOptions")}
				/>
			</Group>
			<GroupFooter>
				Host, plugins and access are fixed once the session starts. Model and effort can change later.
			</GroupFooter>
		</GroupedPage>
	);
}
