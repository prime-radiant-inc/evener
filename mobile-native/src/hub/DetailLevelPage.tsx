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
import { unreadableDraftDiscardDisabled } from "../keybindingOfflineRecovery";
import { HUB_UNCONFIRMED_MESSAGE } from "../nativePreferences";
import { useNativePreferences } from "../NativePreferencesProvider";
import { DETAIL_LEVELS } from "../session/detailLevels";
import { Group, GroupedPage, GroupFooter, Row, SwitchRow } from "../sheet/Grouped";
import { FirstLoad, SheetStatus } from "../sheet/SheetStatus";
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
const NOT_DONE = "That didn't go through. Try it again.";
const NOT_LOADED = "Couldn't load this hub's setting. It loads again once the hub is back.";
const NOT_UPDATED = "This phone couldn't update its copy of this setting.";

export function DetailLevelPage(_props: NativeStackScreenProps<HubRoutes, "DetailLevel">) {
	const { hubName } = useHubSheet();
	const { model, snapshot, connected } = useNativePreferences();
	const [failed, setFailed] = useState(false);
	const state = snapshot?.transcriptMobile;
	const writeUncertain = state?.writeUncertain ?? false;
	// While a write is uncertain, one check through each model each time the
	// hub is back, so a check that failed gets another; refresh settles its
	// own errors.
	const checkedThrough = useRef<unknown>(null);
	useEffect(() => {
		if (!writeUncertain || !connected) {
			checkedThrough.current = null;
			return;
		}
		if (!model || checkedThrough.current === model) return;
		checkedThrough.current = model;
		void model.refresh();
	}, [writeUncertain, connected, model]);
	// A save whose reply was lost may have landed: once the check settles
	// it, the save's failure no longer stands.
	const wasUncertain = useRef(writeUncertain);
	useEffect(() => {
		if (wasUncertain.current && !writeUncertain) setFailed(false);
		wasUncertain.current = writeUncertain;
	}, [writeUncertain]);
	const current = state?.confirmed ?? null;
	const config = (state?.draft ?? current)?.config;
	// A page that has never loaded says it is connecting (ruling 21).
	if (!state || !model || (!config && (state.loading || state.support === "unknown"))) {
		return (
			<GroupedPage>
				<SheetStatus />
				<FirstLoad hubName={hubName} label="Loading the default detail level" />
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
	// The shared store marks an unconfirmed write as a conflict too; the page
	// checks the hub's setting itself, so there is nothing to resolve yet.
	const conflict = state.conflict && !writeUncertain;
	// Resolving a conflict writes, so it holds for everything a write does.
	const resolveHeld = !connected || state.loading || state.saving || writeUncertain || state.storageUnavailable;
	const busy = resolveHeld || state.conflict;
	// A choice the phone still holds after its save failed, or after an
	// uncertain write settled without the hub taking it: the rows show it, so
	// the page says it hasn't landed and offers to send it again.
	// A draft the phone can't keep in step (a confirmed save whose cleanup
	// failed, a draft it couldn't save or restore) is the storage footer's to
	// explain; every write is held until then anyway.
	const unsaved = !!state.draft && !state.saving && !writeUncertain && !state.conflict && !state.storageUnavailable;
	const stateErrorShown =
		!!state.error && !state.draftUnreadable && !writeUncertain && (!conflict || state.storageUnavailable);
	// A choice that failed can be chosen again; a failed conflict action or
	// discard stays on screen, so the page says it didn't go through unless
	// the store's own failure already says why.
	const failedChoice =
		failed && !unsaved && !writeUncertain && !conflict && !state.storageUnavailable && !state.draftUnreadable;
	const failedAction = failed && !writeUncertain && (conflict || state.draftUnreadable) && !stateErrorShown;
	// What happened to a choice, and what to do about it, reads beneath the
	// levels it was made in; a page with no levels to show leads with it.
	const status = (
		<>
			{state.draftUnreadable ? (
				<>
					<GroupFooter tone="danger">A saved change to this setting couldn't be read on this phone.</GroupFooter>
					<Group>
						{/* Discarding an unreadable record touches only this phone, so the
						    hub being away must not hold it - the same rule the keybindings
						    screen applies. */}
						<Row
							label="Discard it"
							tone="danger"
							disabled={unreadableDraftDiscardDisabled(connected, state)}
							onPress={() => run(() => model.discardTranscriptDraft())}
						/>
					</Group>
				</>
			) : null}
			{writeUncertain ? <GroupFooter>Checking the hub's setting…</GroupFooter> : null}
			{conflict ? <GroupFooter tone="attention">The hub's setting changed while you were choosing.</GroupFooter> : null}
			{conflict && current ? (
				<>
					<Group>
						<Row
							label="Keep mine"
							tone="accent"
							disabled={resolveHeld}
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
							disabled={resolveHeld}
							onPress={() => run(() => model.discardTranscriptDraft())}
						/>
					</Group>
				</>
			) : null}
			{/* During a conflict the storage failure still shows: it is why the rows hold. */}
			{stateErrorShown ? (
				// The store's own messages name its plumbing ("transcript display",
				// a Check current settings button this page doesn't have), so the
				// page says what happened in its own words.
				<GroupFooter tone="danger">{state.error === HUB_UNCONFIRMED_MESSAGE ? NOT_LOADED : NOT_UPDATED}</GroupFooter>
			) : null}
			{/* The phone that can't keep a change holds every row, so there is nothing to choose again. */}
			{failedChoice ? <GroupFooter tone="danger">{NOT_SAVED}</GroupFooter> : null}
			{failedAction ? <GroupFooter tone="danger">{NOT_DONE}</GroupFooter> : null}
			{unsaved ? (
				<>
					<GroupFooter tone="attention">This change hasn't reached the hub yet.</GroupFooter>
					<Group>
						<Row
							label="Save it"
							tone="accent"
							disabled={resolveHeld}
							onPress={() => run(() => model.saveTranscript())}
						/>
					</Group>
				</>
			) : null}
		</>
	);
	return (
		<GroupedPage>
			<SheetStatus />
			{state.support === "unsupported" ? (
				<GroupFooter>This hub doesn't keep a default detail level.</GroupFooter>
			) : null}
			{config && state.support === "supported" ? (
				<>
					{/* The page's title names the setting. */}
					<Group>
						{DETAIL_LEVELS.map(({ level, label, description }) => (
							<Row
								key={level}
								label={label}
								sub={description}
								checked={config.content.kind === "preset" && config.content.level === level}
								disabled={busy}
								onPress={() => {
									if (config.content.kind === "preset" && config.content.level === level) return;
									choose({ ...config, content: { kind: "preset", level } });
								}}
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
					{status}
					{config.content.kind === "custom" ? <CustomChoices config={config} disabled={busy} choose={choose} /> : null}
				</>
			) : (
				status
			)}
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
			<Group label="Shows">
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
			<Group label="More detail">
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
			<Group label="Hook events">
				{HOOK_EXIT_DETAILS.map((detail) => (
					<Row
						key={detail}
						label={HOOK_LABELS[detail]}
						checked={config.advanced.hookExits === detail}
						disabled={disabled}
						onPress={() => {
							if (config.advanced.hookExits === detail) return;
							choose({ ...config, advanced: { ...config.advanced, hookExits: detail } });
						}}
					/>
				))}
			</Group>
		</>
	);
}
