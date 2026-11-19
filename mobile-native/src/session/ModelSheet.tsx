// The model sheet (spec 8.5): the session's model and its effort, or, opened
// from the Session sheet, its vision model (ruling 17). A formSheet route
// that opens at medium, as pickers do (sheetRoutes.ts). Search stays pinned;
// effort choices and errors scroll with the models so large text cannot
// consume the list's viewport. The session screen provides the session and
// its controls through modelHosts.
import {
	coldSessionCanResume,
	type ModelDescriptor,
	modelControlDisplay,
	sessionEffortLevels,
} from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef, useState } from "react";
import { type SectionList, Text, View } from "react-native";
import { ChoiceRow, ModelPicker } from "../ModelPicker";
import type { ModelPickerEntry, ModelSection } from "../modelPickerEntries";
import type { MobileConversation } from "../projectedRows";
import type { Routes } from "../screens";
import { modelPickCanSend } from "../conversation/services/conversation";
import { type ActionOutcome, type SessionControls, useControlsState } from "../sessionControls";
import { type Unread, verifiedSetting } from "../sessionControls";
import { SearchField } from "../sheet/SearchField";
import { Segmented } from "../sheet/Grouped";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import type { ToastMessage } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { effortName, qualifiedModelIdentity } from "./sessionFacts";

const NOTHING_UNREAD: Unread = { model: null, vision: null };

export interface ModelHost {
	session: MobileConversation;
	/** Null while the hub is away: the sheet stays, and nothing can change. */
	controls: SessionControls | null;
	/** Whether the session can take a change now: connected, open and idle. */
	ready: boolean;
	/** The session's own toast, for a change that closed the sheet. */
	toast(message: ToastMessage): void;
}

export const modelHosts = sheetHosts<ModelHost>();

// The actions whose errors this sheet shows; the Session sheet shows every other one.
export const MODEL_ACTIONS = new Set(["changeModel", "setReasoningEffort", "setVisionModel"]);

export function ModelSheet({ route, navigation }: NativeStackScreenProps<Routes, "ModelSheet">) {
	const { hubId, ref, setting } = route.params;
	const sheet = useSheet();
	const host = useSheetHost(modelHosts, sheetKey(hubId, ref), sheet);
	const [query, setQuery] = useState("");
	if (!host) return null;
	// A pick can settle after the reader dismissed the picker, and a newer sheet
	// can hold the top of the stack by then: a bare goBack would pop that sheet,
	// or the conversation underneath it. Only this exact route, still on top,
	// is the sheet the answer may close. The session keeps the acknowledgement
	// and the readback either way; only the dismissal depends on the route.
	const close = () => {
		const stack = navigation.getState();
		if (stack.routes[stack.index]?.key === route.key) sheet.finish();
	};
	return <ModelSheetBody host={host} vision={setting === "vision"} query={query} setQuery={setQuery} close={close} />;
}

function ModelSheetBody({
	host,
	vision,
	query,
	setQuery,
	close,
}: {
	host: ModelHost;
	vision: boolean;
	query: string;
	setQuery(query: string): void;
	close(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const list = useRef<SectionList<ModelPickerEntry, ModelSection>>(null);
	const { session, controls, ready } = host;
	const state = useControlsState(controls);
	// An initiator never reads its outcome from state.error: that channel is
	// the behind-screen observer's. The refusal this sheet's own action got is
	// held here, from the promise, so it survives the reconnect that replaced
	// the controls while the pick was out.
	const [refusal, setRefusal] = useState<string | null>(null);
	// The screen loads the catalog when it opens; a sheet opened before that
	// finished, after it failed, or while the hub was away asks again.
	useEffect(() => {
		if (!controls) return;
		const { catalog, loadingModels } = controls.getSnapshot();
		if (!catalog && !loadingModels) void controls.loadModels();
	}, [controls]);
	const busy = !controls || !ready || state?.pending != null;
	// Whether this session can take a pick is the shared predicate's question
	// (modelPickCanSend), so the sheet and the browser picker cannot disagree.
	const modelBusy = busy || (!vision && !modelPickCanSend(session));
	const levels = vision ? [] : sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning);
	// One qualified identity for the row this sheet marks as current, the pick
	// it sends and the record it checks. The model-control state carries the
	// authoritative display target - the live snapshot's desired-or-active one,
	// the connection's canonical display, or a recovery's last safe one - so
	// the pending pick and the recovery keep their row while the raw fields
	// would show the stale active model. A target whose two fields agree names
	// a bare id, so the snapshot's provider instance (modelProfile) qualifies
	// it: the id may carry a slash of its own (an OpenRouter row is
	// "anthropic/claude-opus-5"), so the provider named beside the id is the
	// only thing that says the identity is whole.
	const modelTarget = modelControlDisplay(session);
	const current = vision
		? session.visionModel
		: modelTarget.modelProvider === modelTarget.model
			? qualifiedModelIdentity({ ...session, modelProvider: modelTarget.modelProvider, model: modelTarget.model })
			: `${modelTarget.modelProvider}/${modelTarget.model}`;
	const canChange = vision ? session.capabilities.changeVisionModel : session.capabilities.changeModel;
	// The rows below decide "already set" by comparing against the session's
	// projection, and that is only the answer once a read has landed after the
	// last set. A set the hub acknowledged whose readback could not run leaves
	// the projection showing the value from before it, so a match there means
	// "not written yet". The record of the value the hub did take is what says
	// which of the two a match is.
	const verified = (setting: "model" | "vision", value: string) =>
		verifiedSetting(state?.unread ?? NOTHING_UNREAD, setting, value);
	const error =
		refusal ?? state?.modelError ?? (state?.lastAction && MODEL_ACTIONS.has(state.lastAction) ? state.error : null);
	// Each refusal reveals the first header row, even when it repeats the
	// previous message after the user has scrolled away to try again.
	const revealError = (outcome: ActionOutcome) => {
		if (outcome.disposition !== "refused") return;
		list.current?.getScrollResponder()?.scrollTo({ y: 0, animated: true });
	};
	/** Sends one change through the controls that can take it now, and settles
	 * the sheet on the outcome the promise carries. The promise is the only
	 * channel an initiator reads: a recycle can replace this sheet's controls
	 * while the answer is out, and the outcome still reaches the reader who
	 * asked, whatever became of the binding that sent it. Every action goes
	 * through here, so a new attempt always retires the previous refusal.
	 * `closes` separates a pick, which ends the sheet, from an effort change,
	 * which stays open behind its result. A `notSent` outcome is the guards'
	 * own no-op - an unchanged pick, an unoffered level, a stale catalog - and
	 * closes nothing and says nothing new. */
	const dispatch = (
		sentWith: SessionControls | null,
		change: Promise<ActionOutcome>,
		closes: boolean,
		then?: () => void,
	) => {
		// A new attempt is the reader's, so the previous refusal stops being the
		// answer to what the picker now shows.
		setRefusal(null);
		void change.then((outcome) => {
			if (outcome.disposition === "notSent") return;
			if (outcome.disposition === "accepted") {
				if (!closes) return;
				close();
				then?.();
				return;
			}
			// The refusal is the daemon's own answer, carried by the promise from
			// the binding that dispatched it whether or not that binding is still
			// the set this sheet reads.
			setRefusal(outcome.message);
			revealError(outcome);
		});
	};
	const choose = (model: ModelDescriptor) => {
		if (!vision && !modelPickCanSend(session)) return;
		// The selected row is only a no-op on a LIVE session. A cold session can
		// carry the same row as its display while holding no authority for it -
		// a pick left pending when the daemon exited, whose snapshot the status
		// frame keeps - and closing on it would strand the choice the departed
		// process never applied. There the row runs through the resume and sends
		// once on the authority the post-resume read establishes.
		//
		// That exception is the primary picker's alone. A vision model is a
		// setting the saved read already reports as in effect, so re-picking it
		// changes nothing and must not start a daemon to say so.
		// A verified record short-circuits only what a publication has
		// established: an acknowledged pick whose readback has not landed must
		// still send the correction rather than dismiss on the stale projection.
		if (
			verified(vision ? "vision" : "model", current) &&
			`${model.provider}/${model.model}` === current &&
			(vision || !coldSessionCanResume(session))
		) {
			close();
			return;
		}
		if (!controls) return;
		if (vision) dispatch(controls, controls.changeVisionModel(model.provider, model.model), true);
		else
			dispatch(controls, controls.changeModel(model.provider, model.model), true, () =>
				host.toast({ text: "Model changed. Applies from the next turn." }),
			);
	};
	// Already the setting: the daemon would acknowledge an unchanged value,
	// and announcing that as a change tells the reader something happened when
	// nothing did.
	const chooseVision = (value: string) => {
		if (verified("vision", session.visionModel) && session.visionModel === value) {
			close();
			return;
		}
		if (controls) dispatch(controls, controls.setVisionModel(value), true);
	};
	const settings = (
		<View style={{ paddingHorizontal: 16, paddingBottom: 8, gap: 8 }}>
			{error ? (
				<Text
					accessibilityRole="alert"
					allowFontScaling={allowFontScaling}
					style={{ color: palette.dangerInk, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
					{error}
				</Text>
			) : null}
			{levels.length > 0 ? (
				<Effort
					levels={levels}
					current={session.reasoningEffort ?? ""}
					disabled={busy}
					choose={(level) => {
						if (controls) dispatch(controls, controls.setReasoningEffort(level), false);
					}}
				/>
			) : null}
		</View>
	);
	return (
		<Sheet
			title={vision ? "Vision model" : "Model"}
			done={{ onPress: () => close() }}
			accessory={
				<View style={{ paddingHorizontal: 16, paddingBottom: 8 }}>
					<SearchField label="Search models" value={query} onChangeText={setQuery} />
				</View>
			}
		>
			<ModelPicker
				ref={list}
				catalog={state?.catalog ?? null}
				loading={state?.loadingModels ?? true}
				query={query}
				visionOnly={vision}
				current={current}
				disabled={modelBusy || !canChange}
				choose={choose}
				header={
					<>
						{levels.length > 0 || error ? settings : null}
						{vision ? (
							<>
								<ChoiceRow
									title="Session model"
									selected={session.visionModel === ""}
									disabled={busy || !canChange}
									onPress={() => chooseVision("")}
								/>
								<ChoiceRow
									title="Off"
									selected={session.visionModel === "off"}
									disabled={busy || !canChange}
									onPress={() => chooseVision("off")}
								/>
							</>
						) : null}
					</>
				}
			/>
		</Sheet>
	);
}

/** Effort as the shared segmented control (spec 16.1): one segment per level
 * the model supports, the current one filled. The control owns the 44pt
 * target and the selection tick, so the sheet's old private twin is gone. */
function Effort({
	levels,
	current,
	disabled,
	choose,
}: {
	levels: string[];
	current: string;
	disabled: boolean;
	choose(level: string): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const caption = { color: palette.inkMid, fontSize: 13 * scale, lineHeight: 18 * scale };
	return (
		<View style={{ gap: 4 }}>
			<Text allowFontScaling={allowFontScaling} style={{ ...caption, fontWeight: "600" }}>
				Effort
			</Text>
			<Segmented
				label="Effort"
				options={levels.map((level) => ({ value: level, label: effortName(level) }))}
				value={current}
				disabled={disabled}
				onChange={choose}
			/>
			<Text allowFontScaling={allowFontScaling} style={caption}>
				Applies from the next turn
			</Text>
		</View>
	);
}
