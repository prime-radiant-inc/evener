// The model sheet (spec 8.5): the session's model and its effort, or, opened
// from the Session sheet, its vision model (ruling 17). A formSheet route
// that opens at medium, as pickers do (sheetRoutes.ts). Search stays pinned;
// effort choices and errors scroll with the models so large text cannot
// consume the list's viewport. The session screen provides the session and
// its controls through modelHosts.
import { type ModelDescriptor, sessionEffortLevels } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef, useState } from "react";
import { type SectionList, Text, View } from "react-native";
import { ChoiceRow, ModelPicker } from "../ModelPicker";
import type { ModelPickerEntry, ModelSection } from "../modelPickerEntries";
import type { MobileConversation } from "../projectedRows";
import type { Routes } from "../screens";
import { type SessionControls, type Unread, useControlsState, verifiedSetting } from "../sessionControls";
import { SearchField } from "../sheet/SearchField";
import { Segmented } from "../sheet/Grouped";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import type { ToastMessage } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { effortName } from "./sessionFacts";

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

export function ModelSheet({ route }: NativeStackScreenProps<Routes, "ModelSheet">) {
	const { hubId, ref, setting } = route.params;
	const sheet = useSheet();
	const host = useSheetHost(modelHosts, sheetKey(hubId, ref), sheet);
	const [query, setQuery] = useState("");
	if (!host) return null;
	return (
		<ModelSheetBody host={host} vision={setting === "vision"} query={query} setQuery={setQuery} finish={sheet.finish} />
	);
}

function ModelSheetBody({
	host,
	vision,
	query,
	setQuery,
	finish,
}: {
	host: ModelHost;
	vision: boolean;
	query: string;
	setQuery(query: string): void;
	finish(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const list = useRef<SectionList<ModelPickerEntry, ModelSection>>(null);
	const { session, controls, ready } = host;
	const state = useControlsState(controls);
	// The screen loads the catalog when it opens; a sheet opened before that
	// finished, after it failed, or while the hub was away asks again.
	useEffect(() => {
		if (!controls) return;
		const { catalog, loadingModels } = controls.getSnapshot();
		if (!catalog && !loadingModels) void controls.loadModels();
	}, [controls]);
	const busy = !controls || !ready || state?.pending != null;
	const levels = vision ? [] : sessionEffortLevels(session.reasoningEffortLevels, session.supportsReasoning);
	const current = vision ? session.visionModel : session.modelProvider;
	const canChange = vision ? session.capabilities.changeVisionModel : session.capabilities.changeModel;
	// The rows below decide "already set" by comparing against the session's
	// projection, and that is only the answer once a read has landed after the
	// last set. A set the hub acknowledged whose readback could not run leaves
	// the projection showing the value from before it, so a match there means
	// "not written yet". The record of the value the hub did take is what says
	// which of the two a match is.
	const verified = (setting: "model" | "vision", value: string) =>
		verifiedSetting(state?.unread ?? NOTHING_UNREAD, setting, value);
	// A refusal belongs to the set that dispatched the write: a recycle can
	// replace this sheet's controls while the answer is out, and the set that
	// sent it is the one that recorded what the hub said. The replacement's
	// snapshot has no such error, so reading that one would show the refusal
	// nowhere after a rebind - the picker stays open and silent, which reads as
	// a pick that did nothing. The refusal is held here until the reader tries
	// again.
	const [refused, setRefused] = useState<string | null>(null);
	const error =
		refused ?? state?.modelError ?? (state?.lastAction && MODEL_ACTIONS.has(state.lastAction) ? state.error : null);
	// Each refusal reveals the first header row, even when it repeats the
	// previous message after the user has scrolled away to try again.
	const revealError = (from: SessionControls | null = controls) => {
		if (from?.getSnapshot().error) list.current?.getScrollResponder()?.scrollTo({ y: 0, animated: true });
	};
	/** Sends one change through the controls that can take it now, and settles
	 * the sheet on what the hub answered them. `sentWith` is the binding that
	 * dispatched, which may be replaced while the answer is out. */
	const closeOn = (sentWith: SessionControls | null, change: Promise<boolean>, then?: () => void) => {
		// A new attempt is the reader's, so the previous refusal stops being the
		// answer to what the picker now shows.
		setRefused(null);
		void change.then((changed) => {
			if (changed) {
				finish();
				then?.();
				return;
			}
			// That binding is the authority on what the hub said, whether or not it
			// is still the set this sheet reads.
			setRefused(sentWith?.getSnapshot().error ?? null);
			revealError(sentWith);
		});
	};
	const choose = (model: ModelDescriptor) => {
		if (verified(vision ? "vision" : "model", current) && `${model.provider}/${model.model}` === current) {
			finish();
			return;
		}
		if (!controls) return;
		if (vision) closeOn(controls, controls.changeVisionModel(model.provider, model.model));
		else
			closeOn(controls, controls.changeModel(model.provider, model.model), () =>
				host.toast({ text: "Model changed. Applies from the next turn." }),
			);
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
					choose={(level) => void controls?.setReasoningEffort(level).then(() => revealError())}
				/>
			) : null}
		</View>
	);
	return (
		<Sheet
			title={vision ? "Vision model" : "Model"}
			done={{ onPress: () => finish() }}
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
				disabled={busy || !canChange}
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
									// Already the setting: the daemon would acknowledge an
									// unchanged value, and announcing that as a change tells the
									// reader something happened when nothing did.
									onPress={() =>
										verified("vision", session.visionModel) && session.visionModel === ""
											? finish()
											: controls && closeOn(controls, controls.setVisionModel(""))
									}
								/>
								<ChoiceRow
									title="Off"
									selected={session.visionModel === "off"}
									disabled={busy || !canChange}
									onPress={() =>
										verified("vision", session.visionModel) && session.visionModel === "off"
											? finish()
											: controls && closeOn(controls, controls.setVisionModel("off"))
									}
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
