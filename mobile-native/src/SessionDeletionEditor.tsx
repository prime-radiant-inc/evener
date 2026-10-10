import { ScrollView } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { Copy, ErrorMessage, styles, useColors } from "./ui";
import { Button } from "./sheet/Grouped";

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
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
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
							The request may have been applied. Check its current state before deciding whether to allow another
							attempt.
						</Copy>
						{retryAvailable ? <Button text label="Allow another attempt" disabled={busy} onPress={allowRetry} /> : null}
					</>
				) : (
					<>
						<Copy>Delete saved session?</Copy>
						<Copy muted>
							This permanently removes the saved hub history for “{title}”. Other sessions and your local unsent draft
							are kept.
						</Copy>
						{skippedReason ? (
							<>
								<Copy>The hub kept this session.</Copy>
								<Copy muted>{skippedReason}</Copy>
							</>
						) : null}
						{lastKnown ? (
							<Copy muted>Check the current session before deleting.</Copy>
						) : !eligible ? (
							<Copy muted>End the session runtime before deleting its saved history.</Copy>
						) : null}
						<Button text label="Delete saved session" disabled={!canDelete} onPress={requestDelete} />
					</>
				)}
				{busy ? <Copy muted>Checking the session…</Copy> : null}
				{ready ? <Button text label="Check again" disabled={busy} onPress={refresh} /> : null}
				<Button
					text
					quiet
					label={missing && !lastKnown ? "Return to Sessions" : "Open Sessions"}
					disabled={busy}
					onPress={openSessions}
				/>
				<Button text quiet label="Close" onPress={close} />
			</ScrollView>
		</SafeAreaView>
	);
}
