import { ScrollView } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function SessionDeletionEditor({
	title,
	hubName,
	ready,
	busy,
	eligible,
	canDelete,
	missing,
	lastKnown,
	skippedReason,
	error,
	uncertain,
	retryAvailable,
	refresh,
	requestDelete,
	allowRetry,
	openSessions,
	close,
}: {
	title: string;
	hubName: string;
	ready: boolean;
	busy: boolean;
	eligible: boolean;
	canDelete: boolean;
	missing: boolean;
	lastKnown: boolean;
	skippedReason: string | null;
	error: string | null;
	uncertain: boolean;
	retryAvailable: boolean;
	refresh(): void;
	requestDelete(): void;
	allowRetry(): void;
	openSessions(): void;
	close(): void;
}) {
	const colors = useColors();
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			<ScrollView contentContainerStyle={{ padding: 20, gap: 16 }}>
				<Copy>{title}</Copy>
				<Copy muted>{hubName}</Copy>
				<ErrorMessage message={error} />
				{missing && !lastKnown ? (
					<>
						<Copy>This session is no longer saved on the hub.</Copy>
						<Copy muted>Your local unsent draft is kept on this device.</Copy>
					</>
				) : uncertain ? (
					<>
						<Copy>Deletion is unconfirmed.</Copy>
						<Copy muted>
							The request may have been applied. Check its current state before
							deciding whether to allow another attempt.
						</Copy>
						{retryAvailable ? (
							<Action disabled={busy} onPress={allowRetry}>
								Allow another attempt
							</Action>
						) : null}
					</>
				) : (
					<>
						<Copy>Delete saved session?</Copy>
						<Copy muted>
							This permanently removes the saved hub history for “{title}”.
							Other sessions and your local unsent draft are kept.
						</Copy>
						{skippedReason ? (
							<>
								<Copy>The hub kept this session.</Copy>
								<Copy muted>{skippedReason}</Copy>
							</>
						) : null}
						{lastKnown ? (
							<Copy muted>
								Refresh to check the current session before deleting.
							</Copy>
						) : !eligible ? (
							<Copy muted>
								End the session runtime before deleting its saved history.
							</Copy>
						) : null}
						<Action disabled={!canDelete} onPress={requestDelete}>
							Delete saved session
						</Action>
					</>
				)}
				{busy ? <Copy muted>Checking the session…</Copy> : null}
				<Action disabled={busy} onPress={refresh}>
					{ready ? "Refresh session" : "Reconnect"}
				</Action>
				<Action tone="quiet" disabled={busy} onPress={openSessions}>
					{missing && !lastKnown ? "Return to Sessions" : "Open Sessions"}
				</Action>
				<Action tone="quiet" onPress={close}>
					Close
				</Action>
			</ScrollView>
		</SafeAreaView>
	);
}
