import { useHeaderHeight } from "@react-navigation/elements";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useState } from "react";
import { KeyboardAvoidingView, Platform, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { pinDrafts } from "./nativeOrganization";
import { PinAssignmentEditor } from "./PinAssignmentEditor";
import type {
	PinAssignmentDraft,
	PinAssignmentSelection,
} from "./pinAssignmentDrafts";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
import { usePinNavigation } from "./usePinNavigation";

const draftError =
	"Your last saved pin proposal is kept. This edit could not be saved on this device; refresh to retry.";

export function PinAssignmentScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "PinAssignment">) {
	const { hubId, ref, title } = route.params;
	const {
		activeProfile,
		ready,
		belongs,
		focused,
		retry,
		pages,
		page,
		actions,
		action,
		observed,
		confirmed,
		isCurrent,
	} = usePinNavigation(hubId, ref);
	const colors = useColors();
	const headerHeight = useHeaderHeight();
	const repository = useMemo(() => pinDrafts(hubId, ref), [hubId, ref]);
	const [proposal, setProposal] = useState<{
		owner: typeof repository;
		draft: PinAssignmentDraft | null;
		error: string | null;
	}>(() => {
		try {
			return { owner: repository, draft: repository.load(), error: null };
		} catch {
			return { owner: repository, draft: null, error: draftError };
		}
	});
	const selected = proposal.owner === repository ? proposal : null;
	useEffect(() => {
		if (proposal.owner === repository) return;
		try {
			setProposal({ owner: repository, draft: repository.load(), error: null });
		} catch {
			setProposal({ owner: repository, draft: null, error: draftError });
		}
	}, [repository, proposal.owner]);
	const available =
		ready && focused && confirmed && !!observed?.location?.top_level;
	const currentAssignment =
		ready &&
		confirmed &&
		!page?.stale &&
		!action?.pending &&
		!action?.uncertain;
	const blocked =
		!available ||
		!page?.loaded ||
		page.loading ||
		page.stale ||
		!!selected?.error ||
		!actions ||
		action?.pending ||
		action?.uncertain ||
		action?.storageUnavailable;
	function refresh() {
		try {
			setProposal({ owner: repository, draft: repository.load(), error: null });
		} catch {
			setProposal((previous) => ({ ...previous, error: draftError }));
		}
		if (ready) void actions?.reconcile();
		else retry();
	}
	function change(selection: PinAssignmentSelection) {
		if (blocked) return;
		try {
			setProposal({
				owner: repository,
				draft: repository.save(selection),
				error: null,
			});
		} catch {
			setProposal((previous) => ({ ...previous, error: draftError }));
		}
	}
	async function mutate(unpin = false) {
		if (blocked || !actions || !selected) return;
		const saved = selected.draft;
		const selection = saved?.selection;
		if (unpin) await actions.unpin({ sessionRef: ref });
		else if (selection?.kind === "existing") {
			if (!page?.rows.some((section) => section.id === selection.sectionId))
				return;
			await actions.assignPin({
				sessionRef: ref,
				sectionId: selection.sectionId,
			});
		} else if (selection?.kind === "new") {
			const name = selection.name.trim();
			if (Array.from(name).length < 1 || Array.from(name).length > 80) return;
			await actions.assignPin({ sessionRef: ref, sectionName: name });
		} else return;
		const result = actions.getSnapshot();
		if (!isCurrent() || result.pending || result.uncertain || result.error)
			return;
		if (saved) {
			try {
				if (repository.removeIf(saved))
					setProposal({ owner: repository, draft: null, error: null });
			} catch {
				setProposal((previous) => ({
					...previous,
					error:
						"The pin change was confirmed, but its saved proposal could not be cleared. Refresh before editing again.",
				}));
			}
		}
	}
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{!belongs ? (
				<Copy>
					This hub is no longer selected. Return to Hubs to reconnect.
				</Copy>
			) : (
				<KeyboardAvoidingView
					style={styles.fill}
					behavior={Platform.OS === "ios" ? "padding" : "height"}
					keyboardVerticalOffset={headerHeight}
				>
					<PinAssignmentEditor
						status={
							<View style={{ gap: 8 }}>
								{!ready ? <Action onPress={retry}>Reconnect</Action> : null}
								<ErrorMessage message={selected?.error ?? null} />
								{selected?.error ? (
									<Action onPress={refresh}>Retry saved proposal</Action>
								) : null}
								{observed ? (
									<Copy muted>
										{!observed.location
											? "This session is no longer available."
											: !observed.location.top_level
												? "Only top-level sessions can be pinned."
												: observed.section
													? `${currentAssignment ? "Currently" : "Last seen"} in ${observed.section.name}`
													: currentAssignment
														? "Not pinned"
														: "Last seen without a pinned section"}
									</Copy>
								) : null}
							</View>
						}
						title={title || "Pin session"}
						hubName={activeProfile?.name ?? "Hub"}
						sections={page?.rows ?? []}
						selection={selected?.draft?.selection ?? null}
						change={change}
						connected={ready && focused}
						canEdit={available && !selected?.error && !!page?.loaded}
						loading={page?.loading ?? false}
						stale={page?.stale ?? false}
						remaining={page?.remaining ?? 0}
						pending={action?.pending ?? false}
						uncertain={!!action?.uncertain || !!action?.storageUnavailable}
						error={action?.error ?? page?.error ?? null}
						refresh={refresh}
						more={() => {
							if (!blocked) void pages?.more();
						}}
						save={() => void mutate()}
						unpin={() => void mutate(true)}
						close={() => navigation.goBack()}
					/>
				</KeyboardAvoidingView>
			)}
		</SafeAreaView>
	);
}
