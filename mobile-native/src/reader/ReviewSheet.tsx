// The Review sheet (spec 10.2, rulings 16, 26 and 30): a verdict, an optional
// note and this document's comments, sent to the session the document was
// opened in through the composer's one Send. It sends while the agent is
// idle, queues while it works (never steering or interrupting it), and
// resumes a shut-down session. The session then shows the review as your
// message, or as a queued one with Steer now, so sending raises no toast.
import { basename } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useState, useSyncExternalStore } from "react";
import { Pressable, ScrollView, Text, TextInput, View } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import type { Routes } from "../screens";
import { returnToSession } from "../session/returnToSession";
import type { SendAction } from "../session/sendAction";
import { SendButton } from "../session/SendButton";
import { readSendAction, type SessionTarget, submitSessionMessage } from "../session/sessionMessage";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { QuoteBlock } from "./CommentSheet";
import { documentMemory } from "./nativeDocumentMemory";
import { reviewMessage, type Verdict } from "./reviewMessage";

const VERDICTS: readonly { verdict: Verdict; label: string }[] = [
	{ verdict: "approve", label: "Approve" },
	{ verdict: "requestChanges", label: "Request changes" },
	{ verdict: "commentOnly", label: "Comment only" },
];

export function ReviewSheet({ route, navigation }: NativeStackScreenProps<Routes, "ReviewSheet">) {
	const { hubId, sessionRef, path, sessionTitle } = route.params;
	const { client, state } = useConnection();
	const online = state === "ready" && client !== null;
	const memory = documentMemory(hubId);
	const key = { sessionRef, path };
	useSyncExternalStore(memory.subscribe, memory.getRevision);
	const comments = memory.comments(key);
	const [verdict, setVerdict] = useState<Verdict | null>(null);
	const [note, setNote] = useState("");
	const [sending, setSending] = useState(false);
	const [failure, setFailure] = useState<string | null>(null);
	const sheet = useSheet({ dirty: verdict !== null || note.trim() !== "", discardTitle: "Discard this review?" });
	const { palette } = useColors();
	const scale = useTextScale();

	// What Send does, read when the sheet opens and again each time the
	// connection returns, which forgets the last answer. Until a read
	// answers, Send waits.
	const [send, setSend] = useState<{ action: SendAction; target: SessionTarget } | null>(null);
	useEffect(() => {
		setSend(null);
		if (!online || !client) return;
		let current = true;
		readSendAction(getNativeMutationRuntime(), client, hubId, sessionRef)
			.then((next) => {
				if (current) setSend(next);
			})
			.catch(() => {
				// Quiet: Send stays disabled, and the next reconnect reads again.
			});
		return () => {
			current = false;
		};
	}, [online, client, hubId, sessionRef]);

	const canSend = online && send !== null && send.action !== "none";
	const submit = async () => {
		if (!verdict || !client || !canSend) return;
		setSending(true);
		setFailure(null);
		try {
			// The session may have started or stopped working since the sheet
			// read it, so Send asks again: queue while it works, never steer.
			const runtime = getNativeMutationRuntime();
			const now = await readSendAction(runtime, client, hubId, sessionRef);
			setSend(now);
			if (now.action === "none") {
				setSending(false);
				return;
			}
			await submitSessionMessage(
				runtime,
				client,
				now.target,
				now.action === "queue" ? "queue" : "send",
				reviewMessage(path, verdict, comments, note),
			);
		} catch (error) {
			setFailure(error instanceof Error ? error.message : String(error));
			setSending(false);
			return;
		}
		memory.clearComments(key);
		sheet.finish(() => returnToSession(navigation, { hubId, ref: sessionRef, title: sessionTitle }));
	};

	const why = !online
		? "Send when you're back online."
		: send?.action === "none"
			? "This session can't take a message right now."
			: null;
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale };
	const to = (
		<Text
			allowFontScaling={allowFontScaling}
			numberOfLines={1}
			style={{ ...small, color: palette.inkMid, paddingHorizontal: 16, paddingBottom: 8, textAlign: "center" }}
		>
			{`To ${sessionTitle} · ${basename(path)}`}
		</Text>
	);
	return (
		<Sheet title="Review" onCancel={sheet.close} accessory={to}>
			<ScrollView automaticallyAdjustKeyboardInsets contentContainerStyle={{ padding: 16, gap: 16 }}>
				<View style={{ gap: 8 }}>
					<View style={{ flexDirection: "row", borderRadius: 10, backgroundColor: palette.inset, padding: 2 }}>
						{VERDICTS.map((choice) => {
							const selected = choice.verdict === verdict;
							return (
								<Pressable
									key={choice.verdict}
									accessibilityRole="button"
									accessibilityLabel={choice.label}
									accessibilityState={{ selected }}
									onPress={() => setVerdict(choice.verdict)}
									style={{
										flex: 1,
										minHeight: 44,
										alignItems: "center",
										justifyContent: "center",
										borderRadius: 8,
										backgroundColor: selected ? palette.accentBg : undefined,
									}}
								>
									{/* The choice shows in its fill and colour alone: a bolder
									    "Request changes" outgrows its third of the row. */}
									<Text
										allowFontScaling={allowFontScaling}
										style={{
											color: selected ? palette.accentInk : palette.inkHi,
											fontSize: 15 * scale,
											lineHeight: 20 * scale,
											textAlign: "center",
										}}
									>
										{choice.label}
									</Text>
								</Pressable>
							);
						})}
					</View>
					{verdict === null ? (
						<Text allowFontScaling={allowFontScaling} style={{ ...small, color: palette.inkMid }}>
							Choose one to send your review.
						</Text>
					) : null}
				</View>
				<View style={{ flexDirection: "row", alignItems: "flex-end", gap: 8 }}>
					<TextInput
						accessibilityLabel="Overall note"
						multiline
						placeholder={
							verdict === "approve" ? "Optional: anything to keep in mind" : "Optional: the gist of what to change"
						}
						placeholderTextColor={palette.inkLow}
						value={note}
						onChangeText={setNote}
						allowFontScaling={allowFontScaling}
						style={{
							flex: 1,
							minHeight: 88,
							color: palette.inkHi,
							fontSize: 17 * scale,
							lineHeight: 22 * scale,
							textAlignVertical: "top",
						}}
					/>
					<SendButton
						label="Send review"
						disabled={verdict === null || !canSend || sending}
						onPress={() => void submit()}
					/>
				</View>
				{why ? (
					<Text allowFontScaling={allowFontScaling} style={{ ...small, color: palette.inkMid }}>
						{why}
					</Text>
				) : null}
				{failure !== null ? (
					<Text allowFontScaling={allowFontScaling} style={{ ...small, color: palette.dangerInk }}>
						{`Couldn't send this: ${failure}`}
					</Text>
				) : null}
				{comments.length === 0 ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
					>
						No comments. Touch and hold a paragraph to add one.
					</Text>
				) : (
					comments.map((comment) => (
						<View key={comment.id} style={{ gap: 6 }}>
							<QuoteBlock words={comment.quote} limit={120} size={14} lineHeight={19} />
							<Text
								allowFontScaling={allowFontScaling}
								style={{ color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale }}
							>
								{comment.text}
							</Text>
						</View>
					))
				)}
			</ScrollView>
		</Sheet>
	);
}
