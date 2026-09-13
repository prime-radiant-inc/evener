import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useRef, useState } from "react";
import {
	ActivityIndicator,
	KeyboardAvoidingView,
	Platform,
	ScrollView,
	TextInput,
	useWindowDimensions,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { KeybindingsRule } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { useConnection } from "./ConnectionProvider";
import { checkedKeybindingChange, keybindingPreview } from "./keybindingRules";
import { useNativePreferences } from "./NativePreferencesProvider";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

function shortcutLabel(chord: string): string {
	return chord
		.replaceAll("$mod", "Command / Control")
		.replaceAll("Meta", "Command")
		.replaceAll("Alt", "Option");
}

function RuleSummary({ rules }: { rules: readonly KeybindingsRule[] }) {
	const preview = keybindingPreview(rules);
	return (
		<View style={{ gap: 12 }}>
			{rules.length === 0 ? (
				<Copy muted>All shortcuts use their defaults.</Copy>
			) : (
				rules.map((rule, index) => (
					<View
						key={JSON.stringify([
							rule.action,
							rule.chord,
							rules
								.slice(0, index)
								.filter((other) => other.action === rule.action).length,
						])}
					>
						<Copy>
							{preview.rows.find((row) => row.actionId === rule.action)
								?.title ?? rule.action}
						</Copy>
						<Copy muted>
							{rule.chord === null ? "Unbound" : shortcutLabel(rule.chord)}
						</Copy>
					</View>
				))
			)}
		</View>
	);
}

export function KeybindingPreferencesScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "KeybindingPreferences">) {
	const connection = useConnection();
	const preferences = useNativePreferences();
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const [error, setError] = useState<string | null>(null);
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
	const setReviewedRevision = (revision: number | null) =>
		setReview(revision === null ? null : { scope, revision });
	currentScope.current = scope;
	const mounted = useRef(false);
	useEffect(() => {
		mounted.current = true;
		return () => {
			mounted.current = false;
		};
	}, []);
	const domain = preferences.snapshot?.keybindings;
	const rules = domain?.draft?.rules ?? domain?.confirmed?.rules ?? [];
	const preview = useMemo(() => keybindingPreview(rules), [rules]);
	const editor = route.params.editor;
	const action = preview.rows.find((row) => row.actionId === editor?.actionId);
	const model = preferences.model;
	const available =
		!!model &&
		preferences.connected &&
		domain?.support === "supported" &&
		!!domain.confirmed;
	const busy =
		!available ||
		!!domain?.loading ||
		!!domain?.saving ||
		!!domain?.writeUncertain ||
		!!domain?.storageUnavailable ||
		!!domain?.confirmed?.loadError;
	const run = (operation: () => Promise<unknown>, success?: () => void) => {
		if (!scope.connected || !model || scope.hubId !== route.params.hubId)
			return;
		const active = () => mounted.current && currentScope.current === scope;
		setError(null);
		void operation()
			.then(() => {
				if (active()) success?.();
			})
			.catch(() => {
				if (active())
					setError(
						"The change could not be completed. Check current shortcuts and review your changes.",
					);
			});
	};
	const closeEditor = () => navigation.setParams({ editor: undefined });
	const apply = (chord?: string | null) => {
		if (busy || !editor || !model) return;
		try {
			const next = checkedKeybindingChange(rules, editor.actionId, chord);
			run(() => model.editKeybindings(next), closeEditor);
		} catch (failure) {
			setError(
				failure instanceof Error
					? failure.message
					: "This shortcut cannot be used.",
			);
		}
	};
	if (connection.activeProfile?.id !== route.params.hubId)
		return (
			<Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
		);
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			<KeyboardAvoidingView
				style={styles.fill}
				behavior={Platform.OS === "ios" ? "padding" : undefined}
			>
				<ScrollView
					keyboardShouldPersistTaps="handled"
					contentContainerStyle={{ padding: 20, gap: 16 }}
				>
					<Copy>{connection.activeProfile.name}</Copy>
					<Copy muted>
						Configure this hub’s web keyboard shortcuts. This preview uses Apple
						keys. The web character-key setting can turn the ? shortcut off.
					</Copy>
					{!preferences.connected && (
						<Action onPress={connection.retry}>Reconnect</Action>
					)}
					{domain?.support === "unsupported" && (
						<Copy>This hub does not support keyboard shortcut settings.</Copy>
					)}
					{(!domain || domain.loading) && (
						<ActivityIndicator accessibilityLabel="Loading keyboard shortcuts" />
					)}
					<ErrorMessage message={error ?? domain?.error ?? null} />
					{domain?.writeUncertain && (
						<Copy>
							The save could not be confirmed. Your proposal is kept on this
							phone. Check the hub before making another change.
						</Copy>
					)}
					{model && domain?.support === "supported" && (
						<Action
							disabled={
								!preferences.connected || domain.loading || domain.saving
							}
							onPress={() => run(() => model.refresh())}
						>
							Check current shortcuts
						</Action>
					)}
					{editor ? (
						<>
							<Copy>{action?.title ?? editor.actionId}</Copy>
							<Copy muted>
								{action
									? `Current preview: ${action.shortcuts.map(shortcutLabel).join(" or ") || "Unbound"}`
									: "This action is unavailable in this version."}
							</Copy>
							<TextInput
								accessibilityLabel="Shortcut"
								value={editor.chord}
								onChangeText={(chord) =>
									navigation.setParams({ editor: { ...editor, chord } })
								}
								editable={!busy && !!action}
								autoCapitalize="none"
								autoCorrect={false}
								placeholder="Meta+Shift+P"
								placeholderTextColor={colors.secondary}
								style={{
									borderWidth: 1,
									borderColor: colors.border,
									borderRadius: 12,
									padding: 14,
									color: colors.text,
									fontSize: 17 * (Platform.OS === "ios" ? fontScale : 1),
								}}
								allowFontScaling={Platform.OS !== "ios"}
							/>
							<Copy muted>
								Use + between keys. Meta means Command, Alt means Option, and
								$mod uses Command on Apple or Control elsewhere. Put a space
								between successive key presses.
							</Copy>
							<Action
								tone="primary"
								disabled={busy || !action || !editor.chord.trim()}
								onPress={() => apply(editor.chord)}
							>
								Use shortcut
							</Action>
							<Action disabled={busy || !action} onPress={() => apply(null)}>
								Unbind shortcut
							</Action>
							<Action disabled={busy || !action} onPress={() => apply()}>
								Restore default
							</Action>
							<Action
								onPress={() => {
									setError(null);
									closeEditor();
								}}
							>
								Cancel edit
							</Action>
						</>
					) : (
						<>
							{domain?.draft && (
								<View style={{ gap: 12 }}>
									<Copy>Changes saved on this phone</Copy>
									{domain.conflict ? (
										<>
											<Copy>
												The hub has different shortcuts. Review both versions
												before replacing its settings.
											</Copy>
											<Action
												disabled={busy}
												onPress={() =>
													setReviewedRevision(
														domain.confirmed?.revision ?? null,
													)
												}
											>
												Review current settings
											</Action>
											{reviewedRevision !== null && domain.confirmed && (
												<>
													<Copy>Current on the hub</Copy>
													<RuleSummary rules={domain.confirmed.rules} />
													<Copy>Your proposal</Copy>
													<RuleSummary rules={domain.draft.rules} />
													{reviewedRevision !== domain.confirmed.revision && (
														<Copy>
															The hub changed again. Review the current settings
															again.
														</Copy>
													)}
													<Action
														disabled={
															busy ||
															reviewedRevision !== domain.confirmed.revision
														}
														onPress={() => {
															if (model)
																run(
																	() =>
																		model.rebaseKeybindingsDraft(
																			reviewedRevision,
																		),
																	() => setReviewedRevision(null),
																);
														}}
													>
														Keep my proposal
													</Action>
													<Action
														disabled={
															busy ||
															reviewedRevision !== domain.confirmed.revision
														}
														onPress={() => {
															if (model)
																run(
																	() => model.discardKeybindingsDraft(),
																	() => setReviewedRevision(null),
																);
														}}
													>
														Use hub settings
													</Action>
												</>
											)}
										</>
									) : (
										<>
											<Action
												tone="primary"
												disabled={busy}
												onPress={() => {
													if (model) run(() => model.saveKeybindings());
												}}
											>
												Save changes
											</Action>
											<Action
												disabled={busy}
												onPress={() => {
													if (model) run(() => model.discardKeybindingsDraft());
												}}
											>
												Discard changes
											</Action>
										</>
									)}
								</View>
							)}
							{preview.warnings.map((warning, index) => (
								<Copy
									muted
									key={JSON.stringify([
										warning.rule,
										warning.reason,
										warning.conflictWith,
										preview.warnings
											.slice(0, index)
											.filter(
												(other) =>
													JSON.stringify(other) === JSON.stringify(warning),
											).length,
									])}
								>
									{warning.message}. This rule is kept when you edit other
									shortcuts.
								</Copy>
							))}
							{domain?.confirmed &&
								preview.rows.map((row) => (
									<View
										key={row.actionId}
										style={{
											borderTopWidth: 0.5,
											borderColor: colors.border,
											paddingTop: 10,
											gap: 2,
										}}
									>
										<Action
											disabled={busy}
											label={`Edit ${row.title}`}
											onPress={() => {
												setError(null);
												const raw = rules.findLast(
													(rule) => rule.action === row.actionId,
												);
												navigation.setParams({
													editor: {
														actionId: row.actionId,
														chord: raw?.chord ?? row.shortcuts[0] ?? "",
													},
												});
											}}
										>
											{row.title}
										</Action>
										<Copy muted>
											{row.shortcuts.map(shortcutLabel).join(" or ") ||
												"Unbound"}
											{row.customized ? " · Custom" : " · Default"}
										</Copy>
									</View>
								))}
						</>
					)}
				</ScrollView>
			</KeyboardAvoidingView>
		</SafeAreaView>
	);
}
