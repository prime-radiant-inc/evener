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
