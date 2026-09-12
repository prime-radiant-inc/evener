import { useState } from "react";
import { ScrollView, Switch, View } from "react-native";
import {
	CONTENT_LEVELS,
	type ContentLevel,
	type ContentVector,
	HOOK_EXIT_DETAILS,
	presetContent,
	type TranscriptDisplayConfigV1,
} from "../../cmd/evener-hub/frontend/src/transcriptDisplay/config";
import type { NativePreferencesSnapshot } from "./nativePreferences";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

const levels: Record<ContentLevel, { label: string; description: string }> = {
	chat: { label: "Chat", description: "Messages and action summaries." },
	intent: {
		label: "Intent",
		description: "Messages and the purpose of each action.",
	},
	tools: {
		label: "Tool calls",
		description: "Keep tool details available to expand.",
	},
	activity: {
		label: "Tool output",
		description: "Open tool details by default.",
	},
	full: {
		label: "Everything",
		description: "Include reasoning and open details by default.",
	},
};
const customFields: ReadonlyArray<{ key: keyof ContentVector; label: string }> =
	[
		{ key: "toolIntent", label: "Action summaries" },
		{ key: "toolCalls", label: "Tool calls" },
		{ key: "reasoning", label: "Reasoning" },
		{ key: "expandByDefault", label: "Open details by default" },
	];
const advancedFields = [
	["roundTimings", "Timing"],
	["tokenCounts", "Token counts"],
	["estimatedCost", "Estimated cost"],
	["systemEvents", "System events"],
	["promptEvents", "Prompt events"],
] as const;
const hookLabels = {
	none: "Failures only",
	successful: "Successful exits and failures",
	all: "All hook events",
};

function Toggle({
	label,
	value,
	disabled,
	change,
}: {
	label: string;
	value: boolean;
	disabled: boolean;
	change(value: boolean): void;
}) {
	return (
		<View
			style={[
				styles.row,
				{ justifyContent: "space-between", gap: 16, paddingVertical: 6 },
			]}
		>
			<View style={{ flex: 1 }}>
				<Copy>{label}</Copy>
			</View>
			<Switch
				accessibilityLabel={label}
				value={value}
				disabled={disabled}
				onValueChange={change}
			/>
		</View>
	);
}

export function TranscriptPreferencesEditor({
	hubName,
	state,
	connected,
	edit,
	save,
	refresh,
	discard,
	rebase,
}: {
	hubName: string;
	state: NativePreferencesSnapshot["transcriptMobile"];
	connected: boolean;
	edit(config: TranscriptDisplayConfigV1): void;
	save(): void;
	refresh(): void;
	discard(): void;
	rebase(reviewedRevision: number): void;
}) {
	const colors = useColors();
	const [advanced, setAdvanced] = useState(false);
	const [reviewRevision, setReviewRevision] = useState<number | null>(null);
	const current = state.confirmed;
	const review = current !== null && reviewRevision === current.revision;
	const selected = state.draft ?? current;
	const config = selected?.config;
	const disabled =
		!connected ||
		state.loading ||
		state.saving ||
		state.writeUncertain ||
		state.storageUnavailable;
	const dirty = state.draft !== null;
	return (
		<View style={styles.fill}>
			<ScrollView contentContainerStyle={{ padding: 20, gap: 16 }}>
				<Copy>{hubName}</Copy>
				<Copy muted>
					Applies to mobile views connected to this hub. Questions, failures and
					active work stay visible.
				</Copy>
				{!connected ? (
					<Copy muted>
						Reconnect to change these settings. Your saved draft is kept.
					</Copy>
				) : null}
				{state.support === "unsupported" ? (
					<Copy>This hub does not offer transcript display settings.</Copy>
				) : null}
				{state.support === "unknown" || (state.loading && !current) ? (
					<Copy muted>Loading display settings…</Copy>
				) : null}
				<ErrorMessage message={state.error} />
				{state.writeUncertain ? (
					<View style={{ gap: 8 }}>
						<Copy>The last save could not be confirmed.</Copy>
						<Copy muted>
							Check the hub's current settings before applying this draft again.
						</Copy>
						<Action
							disabled={!connected || state.loading || state.saving}
							onPress={refresh}
						>
							Check current settings
						</Action>
					</View>
				) : state.conflict ? (
					<View style={{ gap: 8 }}>
						<Copy>The hub settings changed while you were editing.</Copy>
						<Action
							disabled={disabled}
							expanded={review}
							onPress={() =>
								setReviewRevision(review ? null : (current?.revision ?? null))
							}
						>
							Review current settings
						</Action>
						{review && current ? (
							<View style={{ gap: 8 }}>
								<ConfigSummary title="On the hub" config={current.config} />
								{config ? (
									<ConfigSummary title="Your draft" config={config} />
								) : null}
								<Action
									disabled={disabled}
									onPress={() => {
										rebase(current.revision);
										setReviewRevision(null);
									}}
								>
									Keep this draft over current settings
								</Action>
								<Action
									disabled={disabled}
									onPress={() => {
										discard();
										setReviewRevision(null);
									}}
								>
									Use current hub settings
								</Action>
							</View>
						) : null}
					</View>
				) : null}
				{config && state.support === "supported" ? (
					<>
						<Copy>Show in the conversation</Copy>
						<View>
							{CONTENT_LEVELS.map((level) => (
								<Choice
									key={level}
									label={levels[level].label}
									selected={
										config.content.kind === "preset" &&
										config.content.level === level
									}
									disabled={disabled}
									onPress={() =>
										edit({ ...config, content: { kind: "preset", level } })
									}
								/>
							))}
							<Choice
								label="Custom"
								selected={config.content.kind === "custom"}
								disabled={disabled}
								onPress={() =>
									edit({
										...config,
										content: {
											kind: "custom",
											...(config.content.kind === "preset"
												? presetContent(config.content.level)
												: config.content),
										},
									})
								}
							/>
						</View>
						<Copy muted>
							{config.content.kind === "preset"
								? levels[config.content.level].description
								: "Choose the content and default detail level."}
						</Copy>
						{config.content.kind === "custom" ? (
							<View>
								{customFields.map(({ key, label }) => (
									<Toggle
										key={key}
										label={label}
										disabled={disabled}
										value={
											config.content.kind === "custom" && config.content[key]
										}
										change={(value) => {
											if (config.content.kind === "custom")
												edit({
													...config,
													content: { ...config.content, [key]: value },
												});
										}}
									/>
								))}
							</View>
						) : null}
						<View
							style={{
								borderTopWidth: 0.5,
								borderColor: colors.border,
								paddingTop: 8,
							}}
						>
							<Action
								expanded={advanced}
								onPress={() => setAdvanced(!advanced)}
							>
								More detail
							</Action>
							{advanced ? (
								<View style={{ gap: 8 }}>
									{advancedFields.map(([key, label]) => (
										<Toggle
											key={key}
											label={label}
											value={config.advanced[key]}
											disabled={disabled}
											change={(value) =>
												edit({
													...config,
													advanced: { ...config.advanced, [key]: value },
												})
											}
										/>
									))}
									<Copy>Hook events</Copy>
									{HOOK_EXIT_DETAILS.map((detail) => (
										<Choice
											key={detail}
											label={hookLabels[detail]}
											disabled={disabled}
											selected={config.advanced.hookExits === detail}
											onPress={() =>
												edit({
													...config,
													advanced: { ...config.advanced, hookExits: detail },
												})
											}
										/>
									))}
								</View>
							) : null}
						</View>
					</>
				) : null}
				{state.error && !state.writeUncertain ? (
					<Action
						disabled={!connected || state.loading || state.saving}
						onPress={refresh}
					>
						Refresh settings
					</Action>
				) : null}
			</ScrollView>
			{state.support === "supported" && config ? (
				<View
					style={{
						paddingHorizontal: 20,
						paddingVertical: 12,
						gap: 8,
						borderTopWidth: 0.5,
						borderColor: colors.border,
					}}
				>
					<Copy muted>
						{state.saving
							? "Saving…"
							: dirty
								? "Unsaved changes"
								: "Using hub settings"}
					</Copy>
					<View style={[styles.row, { flexWrap: "wrap" }]}>
						<Action
							tone="primary"
							disabled={disabled || !dirty || state.conflict}
							onPress={save}
						>
							Save changes
						</Action>
						{dirty ? (
							<Action disabled={disabled || state.conflict} onPress={discard}>
								Discard draft
							</Action>
						) : null}
					</View>
				</View>
			) : null}
		</View>
	);
}

function ConfigSummary({
	title,
	config,
}: {
	title: string;
	config: TranscriptDisplayConfigV1;
}) {
	const content =
		config.content.kind === "preset"
			? levels[config.content.level].label
			: customFields
					.filter(
						({ key }) =>
							config.content.kind === "custom" && config.content[key],
					)
					.map(({ label }) => label)
					.join(", ") || "Messages and essential context";
	const extra = advancedFields
		.filter(([key]) => config.advanced[key])
		.map(([, label]) => label);
	return (
		<View style={{ gap: 3 }}>
			<Copy>{title}</Copy>
			<Copy muted>{content}</Copy>
			<Copy muted>
				{extra.length ? extra.join(", ") : "No extra metadata"} ·{" "}
				{hookLabels[config.advanced.hookExits]}
			</Copy>
		</View>
	);
}
