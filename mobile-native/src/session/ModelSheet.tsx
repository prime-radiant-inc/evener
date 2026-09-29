// The model sheet (spec 8.5): the session's model and its effort, or, opened
// from the Session sheet, its vision model (ruling 17). A formSheet route
// that opens at medium, as pickers do (sheetRoutes.ts). The search field and
// Effort sit in its header, so both stay in reach at half height: a
// formSheet has no pinned footer (ruling 37). The session screen provides
// the session and its controls through modelHosts.
import { type ModelDescriptor, sessionEffortLevels } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { ChoiceRow, ModelPicker } from "../ModelPicker";
import type { MobileConversation } from "../projectedRows";
import type { Routes } from "../screens";
import { type SessionControls, useControlsState } from "../sessionControls";
import { SearchField } from "../sheet/SearchField";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import type { ToastMessage } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { effortName } from "./sessionFacts";
import { haptic } from "../haptics";

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
	const error = state?.modelError ?? (state?.lastAction && MODEL_ACTIONS.has(state.lastAction) ? state.error : null);
	const closeOn = (change: Promise<boolean>, then?: () => void) =>
		void change.then((changed) => {
			if (!changed) return;
			finish();
			then?.();
		});
	const choose = (model: ModelDescriptor) => {
		if (`${model.provider}/${model.model}` === current) {
			finish();
			return;
		}
		if (!controls) return;
		if (vision) closeOn(controls.changeVisionModel(model.provider, model.model));
		else
			closeOn(controls.changeModel(model.provider, model.model), () =>
				host.toast({ text: "Model changed. Applies from the next turn." }),
			);
	};
	const pinned = (
		<View style={{ paddingHorizontal: 16, paddingBottom: 8, gap: 8 }}>
			<SearchField label="Search models" value={query} onChangeText={setQuery} />
			{levels.length > 0 ? (
				<Effort
					levels={levels}
					current={session.reasoningEffort ?? ""}
					disabled={busy}
					choose={(level) => void controls?.setReasoningEffort(level)}
				/>
			) : null}
			{error ? (
				<Text
					accessibilityRole="alert"
					allowFontScaling={allowFontScaling}
					style={{ color: palette.dangerInk, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
					{error}
				</Text>
			) : null}
		</View>
	);
	return (
		<Sheet title={vision ? "Vision model" : "Model"} done={{ onPress: () => finish() }} accessory={pinned}>
			<ModelPicker
				catalog={state?.catalog ?? null}
				loading={state?.loadingModels ?? true}
				query={query}
				visionOnly={vision}
				current={current}
				disabled={busy || !canChange}
				choose={choose}
				header={
					vision ? (
						<>
							<ChoiceRow
								title="Session model"
								selected={session.visionModel === ""}
								disabled={busy || !canChange}
								onPress={() => controls && closeOn(controls.setVisionModel(""))}
							/>
							<ChoiceRow
								title="Off"
								selected={session.visionModel === "off"}
								disabled={busy || !canChange}
								onPress={() => controls && closeOn(controls.setVisionModel("off"))}
							/>
						</>
					) : null
				}
			/>
		</Sheet>
	);
}

/** Effort as a segmented control: one segment per level the model supports,
 * the current one filled (spec 16.1). */
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
			<View
				accessibilityRole="radiogroup"
				style={{ flexDirection: "row", borderRadius: 9, backgroundColor: palette.inset, padding: 2, gap: 2 }}
			>
				{levels.map((level) => {
					const selected = level === current;
					return (
						<Pressable
							key={level}
							accessibilityRole="radio"
							accessibilityLabel={effortName(level)}
							accessibilityState={{ selected, disabled }}
							disabled={disabled}
							onPress={() => {
								if (!selected) haptic("selection");
								choose(level);
							}}
							style={[
								{
									flex: 1,
									minHeight: 32,
									alignItems: "center",
									justifyContent: "center",
									borderRadius: 7,
									backgroundColor: selected ? palette.accentBg : "transparent",
								},
								{ opacity: disabled ? 0.4 : 1 },
							]}
						>
							<Text
								allowFontScaling={allowFontScaling}
								numberOfLines={1}
								style={{
									color: selected ? palette.accentInk : palette.inkHi,
									fontSize: 15 * scale,
									lineHeight: 20 * scale,
									fontWeight: selected ? "600" : "400",
								}}
							>
								{effortName(level)}
							</Text>
						</Pressable>
					);
				})}
			</View>
			<Text allowFontScaling={allowFontScaling} style={caption}>
				Applies from the next turn
			</Text>
		</View>
	);
}
