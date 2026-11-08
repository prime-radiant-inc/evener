import { space } from "./design/tokens";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useRef } from "react";
import { View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { KeybindingsRule } from "@evener/appwire-client";
import { useConnection } from "./ConnectionProvider";
import { keybindingPreview } from "./keybindingRules";
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

function shortcutLabel(chord: string): string {
	return chord.replaceAll("$mod", "Command / Control").replaceAll("Meta", "Command").replaceAll("Alt", "Option");
}

const EMPTY_RULES: readonly KeybindingsRule[] = [];

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
	currentScope.current = scope;
	const mounted = useRef(false);
	useEffect(() => {
		mounted.current = true;
		return () => {
			mounted.current = false;
		};
	}, []);
	const domain = preferences.snapshot?.keybindings;
	const rules = domain?.confirmed?.rules ?? EMPTY_RULES;
	const preview = useMemo(() => keybindingPreview(rules), [rules]);
	const editor = route.params.editor;
	const editorRef = useRef(editor);
	editorRef.current = editor;
	const action = preview.rows.find((row) => row.actionId === editor?.actionId);
	const model = preferences.model;
	const available = !!model && preferences.connected && domain?.support === "supported" && !!domain.confirmed;
	const busy = !available || !!domain?.loading || !!domain?.confirmed?.loadError;
	const run = (operation: () => Promise<unknown>, success?: () => void) => {
		if (!scope.connected || !model || scope.hubId !== route.params.hubId) return;
		const active = () => mounted.current && currentScope.current === scope;
		setError(null);
		void operation()
			.then(() => {
				if (active()) success?.();
			})
			.catch((failure: unknown) => {
				if (active())
					setError(
						failure instanceof Error
							? failure.message
							: "The change could not be completed. Check current shortcuts and review your changes.",
					);
			});
	};
	const closeEditor = () => navigation.setParams({ editor: undefined });
	const apply = (chord?: string | null) => {
		if (busy || !editor || !model) return;
		const target = editor;
		run(
			() => model.editKeybinding(target.actionId, chord),
			() => {
				// A reply for an editor the user has since replaced must not
				// close the newer one.
				if (editorRef.current === target) closeEditor();
			},
		);
	};
	const legacy = preferences.legacyKeybindingDraft;
	const applySaved = (saved: readonly KeybindingsRule[]) => {
		if (!model) return;
		run(async () => {
			await model.applySavedKeybindings(saved);
			const outcome = preferences.discardLegacyKeybindingDraft();
			if (outcome !== "removed" && outcome !== "absent" && outcome !== null)
				throw new Error("The saved changes were applied, but this phone could not remove its old copy.");
		});
	};
	const discardSaved = () => {
		try {
			const outcome = preferences.discardLegacyKeybindingDraft();
			if (outcome === "storageUnavailable" || outcome === "refused")
				setError("The saved shortcut changes could not be removed from this phone.");
			else if (outcome !== null) setError(null);
		} catch {
			setError("The saved shortcut changes could not be removed from this phone.");
		}
	};
	const shownError = error
		? { source: "action", message: error.message }
		: domain?.error
			? { source: "store", message: domain.error }
			: null;
	const errorMessage = shownError?.message;
	const errorReport = useMemo(() => (errorMessage ? { message: errorMessage } : null), [errorMessage]);
	const scrollRef = useErrorInView(errorReport);
	if (connection.activeProfile?.id !== route.params.hubId) return <Copy>{HUB_NO_LONGER_SELECTED}</Copy>;
	return (
		<SafeAreaView edges={["bottom"]} style={[styles.fill, { backgroundColor: colors.palette.canvas }]}>
			<GroupedPage scrollRef={scrollRef}>
				<View style={{ paddingHorizontal: space.labelInset }}>
					<ErrorMessage
						message={shownError?.message ?? null}
						testID={shownError ? `keybinding-error:${shownError.source}` : undefined}
					/>
				</View>
				{legacy?.outcome === "readable" && (
					<>
						<Copy>Saved shortcut changes from this phone</Copy>
						<RuleSummary rules={legacy.checkpoint.rules} />
						{legacy.checkpoint.writeUncertain && <GroupFooter>The hub never confirmed this saved change.</GroupFooter>}
						{domain?.confirmed && legacy.checkpoint.baseRevision !== domain.confirmed.revision && (
							<>
								<GroupFooter>The hub's shortcuts changed since this was saved.</GroupFooter>
								<Copy>Current on the hub</Copy>
								<RuleSummary rules={domain.confirmed.rules} />
							</>
						)}
						<Group>
							<Button
								primary
								label="Apply saved changes"
								accessibilityLabel="Apply saved changes"
								disabled={busy}
								onPress={() => applySaved(legacy.checkpoint.rules)}
							/>
							<Button text label="Discard saved changes" onPress={discardSaved} />
						</Group>
					</>
				)}
				{legacy?.outcome === "unreadable" && (
					<>
						<Copy>Saved shortcut changes from this phone</Copy>
						<GroupFooter tone="danger">These saved changes could not be read.</GroupFooter>
						<Group>
							<Button text label="Discard saved changes" onPress={discardSaved} />
						</Group>
					</>
				)}
				{legacy?.outcome === "storageUnavailable" && (
					<GroupFooter tone="danger">Could not read the saved shortcut changes on this phone.</GroupFooter>
				)}
				<Group label={connection.activeProfile.name} selectableLabel>
					<Row
						label="Web keyboard shortcuts"
						sub="Configure this hub’s web keyboard shortcuts. This preview uses Apple keys. The web character-key setting can turn the ? shortcut off."
					/>
				</Group>
				<View style={{ paddingHorizontal: space.labelInset, gap: 12 }}>
					{domain?.support === "unsupported" && <Copy>This hub does not support keyboard shortcut settings.</Copy>}
					{(!domain || domain.loading) && <Spinner label="Loading keyboard shortcuts" />}
					{model && domain?.support === "supported" && (
						<Button
							text
							label="Check current shortcuts"
							disabled={!preferences.connected || domain.loading}
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
