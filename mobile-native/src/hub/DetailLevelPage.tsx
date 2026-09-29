// The hub's default detail level (spec 12's Display; spec 8.2's levels):
// choosing a level saves it at once, with no Save button and no draft bar.
// Custom opens the transcript's own switches. The states the old transcript
// editor handled stay, calmly:
// - a saved change this phone can't read offers Discard it (the shared store
//   allows exactly that record to be discarded);
// - a write the hub never confirmed is checked again on its own once the hub
//   is back, once per uncertain write;
// - a conflict with the hub's newer setting offers Keep mine or Use the hub's;
// - a failed load shows its message, and the preferences model reloads on
//   the next connection, so there is nothing to press.
import {
	type ContentVector,
	HOOK_EXIT_DETAILS,
	presetContent,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef, useState } from "react";
import { useNativePreferences } from "../NativePreferencesProvider";
import { DETAIL_LEVELS } from "../session/detailLevels";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row, SwitchRow } from "../sheet/Grouped";
import { Connecting, SheetStatus } from "../sheet/SheetStatus";
import type { HubRoutes } from "./hubSheetContext";
import { useHubSheet } from "./hubSheetContext";

// The old transcript editor's labels (spec 12 keeps them).
const SHOWS: readonly { key: keyof ContentVector; label: string }[] = [
	{ key: "toolIntent", label: "Action summaries" },
	{ key: "toolCalls", label: "Tool calls" },
	{ key: "reasoning", label: "Reasoning" },
	{ key: "expandByDefault", label: "Open details by default" },
];
const MORE_DETAIL = [
	["roundTimings", "Timing"],
	["tokenCounts", "Token counts"],
	["estimatedCost", "Estimated cost"],
	["systemEvents", "System events"],
	["promptEvents", "Prompt events"],
] as const;
const HOOK_LABELS: Record<(typeof HOOK_EXIT_DETAILS)[number], string> = {
	none: "Failures only",
	successful: "Successful exits and failures",
	all: "All hook events",
};

const NOT_SAVED = "The change couldn't be saved. Choose it again.";

export function DetailLevelPage(_props: NativeStackScreenProps<HubRoutes, "DetailLevel">) {
	const { hubName } = useHubSheet();
	const { model, snapshot, connected } = useNativePreferences();
	const [failed, setFailed] = useState(false);
	const state = snapshot?.transcriptMobile;
	const writeUncertain = state?.writeUncertain ?? false;
	// One check per uncertain write, the first time the hub is back for it.
	const checkedUncertainty = useRef(false);
	useEffect(() => {
		if (!writeUncertain) {
			checkedUncertainty.current = false;
			return;
		}
		if (!connected || checkedUncertainty.current || !model) return;
		checkedUncertainty.current = true;
		void model.refresh();
	}, [writeUncertain, connected, model]);
	if (!state || !model) {
		return (
			<GroupedPage>
				<SheetStatus />
				<Connecting hubName={hubName} />
			</GroupedPage>
		);
	}
	const run = (action: () => Promise<unknown>) => {
		setFailed(false);
		action().catch(() => setFailed(true));
	};
	const choose = (config: TranscriptDisplayConfigV1) =>
		run(async () => {
			await model.editTranscript(config);
			await model.saveTranscript();
		});
	const current = state.confirmed;
	const config = (state.draft ?? current)?.config;
	const busy =
		!connected || state.loading || state.saving || writeUncertain || state.storageUnavailable || state.conflict;
	return (
		<GroupedPage>
			<SheetStatus />
			{state.support === "unsupported" ? (
				<GroupFooter>This hub doesn't keep a default detail level.</GroupFooter>
			) : null}
			{state.draftUnreadable ? (
				<>
					<GroupFooter tone="danger">A saved change to this setting couldn't be read on this phone.</GroupFooter>
					<Group>
						<Row
							label="Discard it"
							tone="danger"
							disabled={!connected || state.loading || state.saving || writeUncertain}
							onPress={() => run(() => model.discardTranscriptDraft())}
						/>
					</Group>
				</>
			) : null}
			{writeUncertain ? <GroupFooter>Checking the hub's setting…</GroupFooter> : null}
			{state.conflict && current ? (
				<>
					<GroupFooter tone="attention">The hub's setting changed while you were choosing.</GroupFooter>
					<Group>
						<Row
							label="Keep mine"
							tone="accent"
							disabled={!connected || state.saving}
							onPress={() =>
								run(async () => {
									await model.rebaseTranscriptDraft(current.revision);
									await model.saveTranscript();
								})
							}
						/>
						<Row
							label="Use the hub's"
							tone="accent"
							disabled={!connected || state.saving}
							onPress={() => run(() => model.discardTranscriptDraft())}
						/>
					</Group>
				</>
			) : null}
			{state.error && !state.draftUnreadable && !writeUncertain ? (
				<GroupFooter tone="danger">{state.error}</GroupFooter>
			) : null}
			{failed ? <GroupFooter tone="danger">{NOT_SAVED}</GroupFooter> : null}
			{config && state.support === "supported" ? (
				<>
					<GroupLabel>Default detail level</GroupLabel>
					<Group>
						{DETAIL_LEVELS.map(({ level, label, description }) => (
							<Row
								key={level}
								label={label}
								sub={description}
								checked={config.content.kind === "preset" && config.content.level === level}
								disabled={busy}
								onPress={() => choose({ ...config, content: { kind: "preset", level } })}
							/>
						))}
						<Row
							label="Custom"
							sub="Choose what the transcript shows"
							checked={config.content.kind === "custom"}
							disabled={busy}
							onPress={() => {
								if (config.content.kind === "custom") return;
								choose({ ...config, content: { kind: "custom", ...presetContent(config.content.level) } });
							}}
						/>
					</Group>
					{config.content.kind === "custom" ? <CustomChoices config={config} disabled={busy} choose={choose} /> : null}
				</>
			) : null}
		</GroupedPage>
	);
}

function CustomChoices({
	config,
	disabled,
	choose,
}: {
	config: TranscriptDisplayConfigV1;
	disabled: boolean;
	choose(config: TranscriptDisplayConfigV1): void;
}) {
	const content = config.content;
	if (content.kind !== "custom") return null;
	return (
		<>
			<GroupLabel>Shows</GroupLabel>
			<Group>
				{SHOWS.map(({ key, label }) => (
					<SwitchRow
						key={key}
						label={label}
						value={content[key]}
						disabled={disabled}
						onChange={(value) => choose({ ...config, content: { ...content, [key]: value } })}
					/>
				))}
			</Group>
			<GroupLabel>More detail</GroupLabel>
			<Group>
				{MORE_DETAIL.map(([key, label]) => (
					<SwitchRow
						key={key}
						label={label}
						value={config.advanced[key]}
						disabled={disabled}
						onChange={(value) => choose({ ...config, advanced: { ...config.advanced, [key]: value } })}
					/>
				))}
			</Group>
			<GroupLabel>Hook events</GroupLabel>
			<Group>
				{HOOK_EXIT_DETAILS.map((detail) => (
					<Row
						key={detail}
						label={HOOK_LABELS[detail]}
						checked={config.advanced.hookExits === detail}
						disabled={disabled}
						onPress={() => choose({ ...config, advanced: { ...config.advanced, hookExits: detail } })}
					/>
				))}
			</Group>
		</>
	);
}
