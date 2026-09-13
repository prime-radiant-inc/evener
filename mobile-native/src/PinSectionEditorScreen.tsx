import { useHeaderHeight } from "@react-navigation/elements";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useState } from "react";
import {
	Alert,
	KeyboardAvoidingView,
	Platform,
	ScrollView,
	TextInput,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { sectionDrafts } from "./nativeOrganization";
import type { PinSectionDraft } from "./pinSectionDrafts";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
import { usePinNavigation } from "./usePinNavigation";

const storageError =
	"Your last saved section name is kept. This edit could not be saved on this device; refresh to retry.";
export function PinSectionEditorScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "PinSectionEditor">) {
	const { hubId, sectionId, title } = route.params;
	const pin = usePinNavigation(hubId, undefined, sectionId);
	const colors = useColors(),
		headerHeight = useHeaderHeight();
	const repository = useMemo(
		() => sectionDrafts(hubId, sectionId),
		[hubId, sectionId],
	);
	const [proposal, setProposal] = useState<{
		owner: typeof repository;
		draft: PinSectionDraft | null;
		error: string | null;
	}>(() => {
		try {
			return { owner: repository, draft: repository.load(), error: null };
		} catch {
			return { owner: repository, draft: null, error: storageError };
		}
	});
	const selected = proposal.owner === repository ? proposal : null;
	useEffect(() => {
		if (proposal.owner === repository) return;
		try {
			setProposal({ owner: repository, draft: repository.load(), error: null });
		} catch {
			setProposal({ owner: repository, draft: null, error: storageError });
		}
	}, [repository, proposal.owner]);
	const section = pin.observed?.section;
	const fresh =
		pin.confirmed &&
		pin.ready &&
		!pin.page?.loading &&
		!pin.page?.stale &&
		!pin.action?.pending &&
		!pin.action?.uncertain;
	const blocked =
		!fresh ||
		!section ||
		!selected ||
		!!selected.error ||
		!pin.actions ||
		!!pin.action?.storageUnavailable;
	const name = selected?.draft?.name ?? section?.name ?? title;
	const length = Array.from(name.trim()).length;
	const valid = length > 0 && length <= 80;
	function refresh() {
		try {
			setProposal({ owner: repository, draft: repository.load(), error: null });
		} catch {
			setProposal((previous) => ({ ...previous, error: storageError }));
		}
		if (pin.ready) void pin.actions?.reconcile();
		else pin.retry();
	}
	function change(value: string) {
		if (blocked) return;
		try {
			setProposal({
				owner: repository,
				draft: repository.save(value),
				error: null,
			});
		} catch {
			setProposal((previous) => ({ ...previous, error: storageError }));
		}
	}
	function clear(saved: PinSectionDraft) {
		try {
			if (repository.removeIf(saved))
				setProposal({ owner: repository, draft: null, error: null });
			return true;
		} catch {
			setProposal((previous) => ({
				...previous,
				error:
					"The saved name could not be cleared from this device. Refresh before editing again.",
			}));
			return false;
		}
	}
	async function execute(remove = false) {
		if (blocked || !pin.actions || (!remove && !valid) || !pin.isCurrent())
			return;
		const saved = selected?.draft;
		if (remove) await pin.actions.deletePinSection({ sectionId });
		else await pin.actions.renamePinSection({ sectionId, name: name.trim() });
		const result = pin.actions.getSnapshot();
		if (!pin.isCurrent() || result.pending || result.uncertain || result.error)
			return;
		if (saved && !clear(saved)) return;
		if (remove) navigation.popTo("PinSections", { hubId });
	}
	function confirmDelete() {
		if (blocked || !section) return;
		const version = pin.pages?.getResourceVersion();
		Alert.alert(
			`Delete ${section.name}?`,
			`This removes the pinned section and unpins its ${section.count} ${section.count === 1 ? "session" : "sessions"}. Sessions and their history are kept.`,
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Delete section",
					style: "destructive",
					onPress: () => {
						const current = pin.pages?.getResourceVersion(),
							state = pin.pages?.getSnapshot();
						if (
							!pin.isCurrent() ||
							state?.stale ||
							state?.loading ||
							!version ||
							current?.generationId !== version.generationId ||
							current.revision !== version.revision
						)
							return;
						void execute(true);
					},
				},
			],
		);
	}
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{!pin.belongs ? (
				<Copy>
					This hub is no longer selected. Return to Hubs to reconnect.
				</Copy>
			) : (
				<KeyboardAvoidingView
					style={styles.fill}
					behavior={Platform.OS === "ios" ? "padding" : "height"}
					keyboardVerticalOffset={headerHeight}
				>
					<ScrollView
						keyboardShouldPersistTaps="handled"
						keyboardDismissMode="on-drag"
						contentContainerStyle={{ padding: 20, gap: 16 }}
					>
						<Copy>{section?.name ?? title}</Copy>
						<Copy muted>{pin.activeProfile?.name ?? "Hub"}</Copy>
						<ErrorMessage
							message={
								selected?.error ?? pin.action?.error ?? pin.page?.error ?? null
							}
						/>
						{!pin.ready || !fresh || selected?.error ? (
							<Action
								disabled={!!pin.action?.pending || !!pin.page?.loading}
								onPress={refresh}
							>
								{pin.ready ? "Refresh section" : "Reconnect"}
							</Action>
						) : null}
						{pin.action?.pending ? (
							<Copy muted>Checking the section…</Copy>
						) : null}
						{pin.confirmed && !section ? (
							<Copy>This section no longer exists. Its sessions are kept.</Copy>
						) : section ? (
							<Copy muted>
								{fresh ? "Currently" : "Last seen"}: {section.count}{" "}
								{section.count === 1 ? "session" : "sessions"}
							</Copy>
						) : null}
						<View style={{ gap: 8 }}>
							<Copy>Section name</Copy>
							<TextInput
								accessibilityLabel="Pinned section name"
								value={name}
								editable={!blocked}
								onChangeText={change}
								style={[
									styles.input,
									{
										color: colors.text,
										borderColor: colors.border,
										backgroundColor: colors.surface,
									},
								]}
							/>
							{!valid ? <Copy muted>Use 1–80 characters.</Copy> : null}
						</View>
						<View style={[styles.row, { flexWrap: "wrap" }]}>
							<Action
								tone="primary"
								disabled={blocked || !valid}
								onPress={() => void execute()}
							>
								Save name
							</Action>
							{selected?.draft ? (
								<Action
									tone="quiet"
									disabled={!fresh}
									onPress={() => {
										if (selected.draft) clear(selected.draft);
									}}
								>
									Discard saved edit
								</Action>
							) : null}
						</View>
						<Action disabled={blocked} onPress={confirmDelete}>
							Delete section
						</Action>
						<Copy muted>
							Deleting a section keeps its sessions and their history.
						</Copy>
						<Action tone="quiet" onPress={() => navigation.goBack()}>
							Close
						</Action>
					</ScrollView>
				</KeyboardAvoidingView>
			)}
		</SafeAreaView>
	);
}
