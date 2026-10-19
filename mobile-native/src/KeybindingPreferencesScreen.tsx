import { space } from "./design/tokens";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useRef, useState } from "react";
import { View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { KeybindingsRule } from "@evener/appwire-client";
import { useConnection } from "./ConnectionProvider";
import {
	offlineAwareDraftUnreadable,
	offlineStorageErrorMessage,
	unreadableDraftDiscardDisabled,
} from "./keybindingOfflineRecovery";
import { checkedKeybindingChange, keybindingPreview } from "./keybindingRules";
import { useNativePreferences } from "./NativePreferencesProvider";
import type { HubRoutes } from "./hub/hubSheetContext";
import { Copy, ErrorMessage, styles, useColors } from "./ui";
import { HUB_NO_LONGER_SELECTED } from "./retainedScreen";
import { Spinner } from "./sheet/Spinner";
import {
	Button,
	Group,
	GroupedPage,
	GroupFooter,
	Row,
	TextFieldRow,
	useErrorInView,
	useFormError,
} from "./sheet/Grouped";
import { HeaderButton } from "./sheet/HeaderButton";

function shortcutLabel(chord: string): string {
	return chord.replaceAll("$mod", "Command / Control").replaceAll("Meta", "Command").replaceAll("Alt", "Option");
}

function RuleSummary({ rules }: { rules: readonly KeybindingsRule[] }) {
	const preview = keybindingPreview(rules);
	return (
		<Group>
			{rules.length === 0 ? (
				<Row label="All shortcuts use their defaults" />
			) : (
				rules.map((rule, index) => (
					<Row
						selectable
						key={JSON.stringify([
							rule.action,
							rule.chord,
							rules.slice(0, index).filter((other) => other.action === rule.action).length,
						])}
						label={preview.rows.find((row) => row.actionId === rule.action)?.title ?? rule.action}
						sub={rule.chord === null ? "Unbound" : shortcutLabel(rule.chord)}
					/>
				))
			)}
		</Group>
	);
}

export function KeybindingPreferencesScreen({
	route,
	navigation,
}: NativeStackScreenProps<HubRoutes, "KeybindingPreferences">) {
	const connection = useConnection();
	const preferences = useNativePreferences();
	const colors = useColors();
	const [error, setError] = useFormError();
	const scope = useMemo(
		() => ({
			model: preferences.model,
			hubId: preferences.hubId,
			connected: preferences.connected,
		}),
		[preferences.model, preferences.hubId, preferences.connected],
	);
	const currentScope = useRef(scope);
	const [review, setReview] = useState<{
		scope: typeof scope;
		revision: number;
	} | null>(null);
	const reviewedRevision = review?.scope === scope ? review.revision : null;
	const setReviewedRevision = (revision: number | null) => setReview(revision === null ? null : { scope, revision });
	currentScope.current = scope;
	const mounted = useRef(false);
	useEffect(() => {
		mounted.current = true;
		return () => {
			mounted.current = false;
		};
	}, []);
	const domain = preferences.snapshot?.keybindings;
	const draftUnreadable = offlineAwareDraftUnreadable(
		preferences.connected,
		domain?.draftUnreadable,
		preferences.offlineDraftUnreadable,
	);
	const rules = domain?.draft?.rules ?? domain?.confirmed?.rules ?? [];
	const preview = useMemo(() => keybindingPreview(rules), [rules]);
	const editor = route.params.editor;
	const action = preview.rows.find((row) => row.actionId === editor?.actionId);
	const model = preferences.model;
	const available = !!model && preferences.connected && domain?.support === "supported" && !!domain.confirmed;
	const busy =
		!available ||
		!!domain?.loading ||
		!!domain?.saving ||
		!!domain?.writeUncertain ||
		!!domain?.storageUnavailable ||
		!!domain?.confirmed?.loadError;
	const run = (operation: () => Promise<unknown>, success?: () => void) => {
		if (!scope.connected || !model || scope.hubId !== route.params.hubId) return;
		const active = () => mounted.current && currentScope.current === scope;
		setError(null);
		void operation()
			.then(() => {
				if (active()) success?.();
			})
			.catch(() => {
				if (active()) setError("The change could not be completed. Check current shortcuts and review your changes.");
			});
	};
	const closeEditor = () => navigation.setParams({ editor: undefined });
	const apply = (chord?: string | null) => {
		if (busy || !editor || !model) return;
		try {
			const next = checkedKeybindingChange(rules, editor.actionId, chord);
			run(() => model.editKeybindings(next), closeEditor);
		} catch (failure) {
			setError(failure instanceof Error ? failure.message : "This shortcut cannot be used.");
		}
	};
	// What the screen reports, and whose failure it is: this screen's own
	// action, the live store's, or the phone's storage while offline.
	const offlineStorageError = !preferences.connected
		? offlineStorageErrorMessage(preferences.offlineStorageUnavailable)
		: null;
	const shownError = error
		? { source: "action", message: error.message }
		: domain?.error
			? { source: "store", message: domain.error }
			: offlineStorageError
				? { source: "storage", message: offlineStorageError }
				: null;
	const errorMessage = shownError?.message;
	const errorReport = useMemo(() => error ?? (errorMessage ? { message: errorMessage } : null), [error, errorMessage]);
	const scrollRef = useErrorInView(errorReport);
	if (connection.activeProfile?.id !== route.params.hubId) return <Copy>{HUB_NO_LONGER_SELECTED}</Copy>;
	return (
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.palette.canvas }]}>
			{!editor && domain?.draft && !domain.conflict && (
				<View style={{ alignItems: "flex-end", paddingHorizontal: space.margin }}>
					<HeaderButton
						strong
						label="Save changes"
						disabled={busy}
						onPress={() => {
							if (model) run(() => model.saveKeybindings());
						}}
					/>
				</View>
			)}
			<GroupedPage scrollRef={scrollRef}>
				<View style={{ paddingHorizontal: space.labelInset }}>
					<ErrorMessage
						message={shownError?.message ?? null}
						testID={shownError ? `keybinding-error:${shownError.source}` : undefined}
					/>
				</View>
				<Group label={connection.activeProfile.name} selectableLabel>
					<Row
						label="Web keyboard shortcuts"
						sub="Configure this hub’s web keyboard shortcuts. This preview uses Apple keys. The web character-key setting can turn the ? shortcut off."
					/>
				</Group>
				<View style={{ paddingHorizontal: space.labelInset, gap: 12 }}>
					{domain?.support === "unsupported" && <Copy>This hub does not support keyboard shortcut settings.</Copy>}
					{(!domain || domain.loading) && <Spinner label="Loading keyboard shortcuts" />}
					{domain?.writeUncertain && (
						<Copy>
							The save could not be confirmed. Your proposal is kept on this phone. Check the hub before making another
							change.
						</Copy>
					)}
					{draftUnreadable && (
						<View style={{ gap: 12 }}>
							<Copy>A saved draft on this phone could not be read. Discard it to continue editing shortcuts.</Copy>
							<Button
								text
								label="Discard unreadable draft"
								disabled={unreadableDraftDiscardDisabled(!!model && preferences.connected, domain)}
								onPress={() => {
									if (model && preferences.connected) {
										run(() => model.discardKeybindingsDraft());
										return;
									}
									// Offline, or no live model: the client object survives a
									// dropped connection (model stays non-null while
									// preferences.connected is false), so `run` alone cannot
									// tell disconnected apart from never-connected - checking
									// connectivity directly is what routes both into the
									// store-free path discardStoredKeybindingDraft documents,
									// instead of a dead button.
									try {
										const outcome = preferences.discardUnreadableKeybindingsDraft();
										// A stale error from an earlier failed action must not
										// outlive a successful discard: ErrorMessage prioritizes
										// this component-local error over domain?.error. But a
										// null outcome means there was no hub to discard for (no
										// action ran) - clearing the error then would hide that
										// prior failure instead of reporting the one it describes.
										if (outcome !== null) setError(null);
									} catch {
										setError("The change could not be completed. Check current shortcuts and review your changes.");
									}
								}}
							/>
						</View>
					)}
					{model && domain?.support === "supported" && (
						<Button
							text
							label="Check current shortcuts"
							disabled={!preferences.connected || domain.loading || domain.saving}
							onPress={() => run(() => model.refresh())}
						/>
					)}
				</View>
				{editor ? (
					<>
						<Group label={action?.title ?? editor.actionId} selectableLabel>
							<Row
								selectable
								label="Current preview"
								sub={
									action
										? `Current preview: ${action.shortcuts.map(shortcutLabel).join(" or ") || "Unbound"}`
										: "This action is unavailable in this version."
								}
							/>
							<TextFieldRow
								label="Shortcut"
								value={editor.chord}
								onChangeText={(chord) => navigation.setParams({ editor: { ...editor, chord } })}
								disabled={busy || !action}
								placeholder="Meta+Shift+P"
							/>
						</Group>
						<GroupFooter>
							Use + between keys. Meta means Command, Alt means Option, and $mod uses Command on Apple or Control
							elsewhere. Put a space between successive key presses.
						</GroupFooter>
						<Group>
							<Button
								primary
								compact
								label="Use shortcut"
								disabled={busy || !action || !editor.chord.trim()}
								onPress={() => apply(editor.chord)}
							/>
							<Button text label="Unbind shortcut" disabled={busy || !action} onPress={() => apply(null)} />
							<Button text label="Restore default" disabled={busy || !action} onPress={() => apply()} />
							<Button
								text
								label="Cancel edit"
								onPress={() => {
									setError(null);
									closeEditor();
								}}
							/>
						</Group>
					</>
				) : (
					<>
						{domain?.draft && (
							<Group label="Changes saved on this phone">
								<View style={{ padding: space.rowInset, gap: space.rowGap }}>
									{domain.conflict ? (
										<>
											<Copy>The hub has different shortcuts. Review both versions before replacing its settings.</Copy>
											<Button
												text
												label="Review current settings"
												disabled={busy}
												onPress={() => setReviewedRevision(domain.confirmed?.revision ?? null)}
											/>
											{reviewedRevision !== null && domain.confirmed && (
												<>
													<Copy>Current on the hub</Copy>
													<RuleSummary rules={domain.confirmed.rules} />
													<Copy>Your proposal</Copy>
													<RuleSummary rules={domain.draft.rules} />
													{reviewedRevision !== domain.confirmed.revision && (
														<Copy>The hub changed again. Review the current settings again.</Copy>
													)}
													<Button
														text
														label="Keep my proposal"
														disabled={busy || reviewedRevision !== domain.confirmed.revision}
														onPress={() => {
															if (model)
																run(
																	() => model.rebaseKeybindingsDraft(reviewedRevision),
																	() => setReviewedRevision(null),
																);
														}}
													/>
													<Button
														text
														label="Use hub settings"
														disabled={busy || reviewedRevision !== domain.confirmed.revision}
														onPress={() => {
															if (model)
																run(
																	() => model.discardKeybindingsDraft(),
																	() => setReviewedRevision(null),
																);
														}}
													/>
												</>
											)}
										</>
									) : (
										<>
											<Button
												text
												label="Discard changes"
												disabled={busy}
												onPress={() => {
													if (model) run(() => model.discardKeybindingsDraft());
												}}
											/>
										</>
									)}
								</View>
							</Group>
						)}
						{preview.warnings.map((warning, index) => (
							<GroupFooter
								selectable
								key={JSON.stringify([
									warning.rule,
									warning.reason,
									warning.conflictWith,
									preview.warnings.slice(0, index).filter((other) => JSON.stringify(other) === JSON.stringify(warning))
										.length,
								])}
							>
								{`${warning.message}. This rule is kept when you edit other shortcuts.`}
							</GroupFooter>
						))}
						{domain?.confirmed && (
							<Group label="Shortcuts">
								{preview.rows.map((row) => (
									<Row
										selectable
										key={row.actionId}
										label={row.title}
										sub={`${row.shortcuts.map(shortcutLabel).join(" or ") || "Unbound"} · ${row.customized ? "Custom" : "Default"}`}
										chevron
										disabled={busy}
										onPress={() => {
											setError(null);
											const raw = rules.findLast((rule) => rule.action === row.actionId);
											navigation.setParams({
												editor: {
													actionId: row.actionId,
													chord: raw?.chord ?? row.shortcuts[0] ?? "",
												},
											});
										}}
									/>
								))}
							</Group>
						)}
					</>
				)}
			</GroupedPage>
		</SafeAreaView>
	);
}
