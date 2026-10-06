// The Reader (spec 10.2): a document from a session's folder, read quietly.
// It marks what changed since you last read it and steps through those
// changes, reopens where you were, offers its outline, takes your comments
// on its paragraphs and list items, and remembers what you read when you
// leave. It recovers while visible, and its existing Document actions menu
// offers Reload for files or permissions that changed.
import { docImageReadURL, rebindFileReference, type FileReference } from "@evener/appwire-client/docContent";
import type { NativeStackHeaderItem, NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import * as SecureStore from "expo-secure-store";
import { Component, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import {
	FlatList,
	type CellRendererProps,
	Image,
	type NativeScrollEvent,
	type NativeSyntheticEvent,
	Platform,
	Pressable,
	Text,
	View,
	type ViewToken,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useHeldAlertCount, useHoldAlerts } from "../alerts/alertsContext";
import { copyText } from "../clipboard";
import { useConnection } from "../ConnectionProvider";
import { HubProfiles } from "../connection";
import type { Routes } from "../screens";
import { timeAgo } from "../session/format";
import { useMinuteClock } from "../session/minuteClock";
import { holdQuote } from "../session/pendingQuote";
import { BackButton } from "../session/BackButton";
import { returnToSession } from "../session/returnToSession";
import { SessionLink } from "../session/sessionMessage";
import { sheetKey, useProvideSheetHost } from "../sheet/sheetHosts";
import { useScreenInFront } from "../sheet/useScreenInFront";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type DocumentBlock, hashText, outline } from "./documentBlocks";
import { anchorBlock, changedBlocks, changesCaption, type Place, restoreBlock } from "./documentChanges";
import type { ReadingPosition } from "./documentMemory";
import { documentKind, documentNotice, type LoadedDocument, truncationNote } from "./documentSource";
import { documentMemory } from "./nativeDocumentMemory";
import { type BlockAction, ReaderBlock, useCodeText } from "./ReaderBlock";
import { type ReaderHost, readerHosts } from "./readerHosts";
import { useDocument, type NativeImageAttempt } from "./useDocument";

const hubs = new HubProfiles(SecureStore);

type Row = { kind: "block"; block: DocumentBlock } | { kind: "line"; index: number; text: string };

interface ScrollMetrics {
	offset: number;
	viewport: number;
	content: number;
}

/** A scroll the list couldn't make yet (the row isn't measured): the list
 * scrolls near it by estimate, then tries again, a few times at most. */
const RESTORE_TRIES = 3;
const RESTORE_RETRY_MS = 50;

/** The kinds you actually saw, which leaving records as read. A document that
 * failed to load, went missing or lives elsewhere was never read, so leaving
 * it must not replace the version you last read. */
const READABLE: ReadonlySet<LoadedDocument["kind"]> = new Set(["markdown", "code", "image", "binary"]);

function rowsOf(document: LoadedDocument | null): Row[] {
	if (document?.kind === "markdown") return document.blocks.map((block) => ({ kind: "block", block }));
	if (document?.kind === "code")
		return document.text
			.replace(/\n$/, "")
			.split("\n")
			.map((text, index) => ({ kind: "line", index, text }));
	return [];
}

export function ReaderScreen({ route, navigation }: NativeStackScreenProps<Routes, "Reader">) {
	const { hubId, sessionRef, path, sessionTitle, updatedAt } = route.params;
	const key = useMemo(() => ({ sessionRef, path }), [sessionRef, path]);
	const memory = documentMemory(hubId);
	const { client, activeProfile, state } = useConnection();
	const inFront = useScreenInFront(route.key);
	const routeReference = route.params.reference;
	const [publication, setPublication] = useState<{ source: string; cwd: string } | null>(null);
	const source = JSON.stringify([hubId, sessionRef]);
	const reference = useMemo(
		() =>
			publication?.source === source && publication.cwd !== routeReference.cwd
				? rebindFileReference(routeReference, publication.cwd)
				: routeReference,
		[publication, source, routeReference],
	);
	const { document, notice: refreshNotice, imageAttempt, reload } = useDocument(hubId, sessionRef, reference, inFront);
	// Banners wait while you read (spec 13.3); Back counts what waits.
	// "In front" is the Reader's own notion of reading: it stays true while a
	// sheet covers the Reader and follows the stack, so a hold ends when the
	// Reader is actually left even if a fast swipe never delivers a blur.
	useHoldAlerts(inFront, "quiet");
	const held = useHeldAlertCount();
	const { palette } = useColors();
	const scale = useTextScale();
	const insets = useSafeAreaInsets();
	const list = useRef<FlatList<Row>>(null);

	// This visit's view of the past: the version you last read. A re-read
	// during the visit compares against the same last read.
	const [lastRead] = useState(() => memory.lastRead(key));
	const now = useMinuteClock();
	const blocks = document?.kind === "markdown" ? document.blocks : null;
	const rows = useMemo(() => rowsOf(document), [document]);
	const changed = useMemo(() => (blocks ? changedBlocks(blocks, lastRead?.blocks ?? null) : []), [blocks, lastRead]);
	const changedSet = useMemo(() => new Set(changed), [changed]);
	const headings = useMemo(() => (blocks ? outline(blocks) : []), [blocks]);
	// Where a position can point: each block, or each line of a code file.
	const places = useMemo<Place[]>(
		() => rows.map((row) => (row.kind === "block" ? row.block : { index: row.index, hash: hashText(row.text) })),
		[rows],
	);

	// Scrolling to a row, and trying again while the list hasn't measured it.
	const target = useRef<{ index: number; viewOffset?: number; animated: boolean } | null>(null);
	const tries = useRef(0);
	const retry = useRef<ReturnType<typeof setTimeout> | null>(null);
	const scrollTo = useCallback((next: { index: number; viewOffset?: number; animated: boolean }) => {
		// A retry still pending for an earlier target would undo this scroll.
		if (retry.current !== null) clearTimeout(retry.current);
		retry.current = null;
		target.current = next;
		tries.current = 0;
		list.current?.scrollToIndex(next);
	}, []);
	useEffect(
		() => () => {
			if (retry.current !== null) clearTimeout(retry.current);
		},
		[],
	);
	const scrollFailed = useCallback((info: { index: number; averageItemLength: number }) => {
		list.current?.scrollToOffset({ offset: info.averageItemLength * info.index, animated: false });
		const wanted = target.current;
		if (!wanted || tries.current >= RESTORE_TRIES) return;
		tries.current += 1;
		retry.current = setTimeout(() => list.current?.scrollToIndex(wanted), RESTORE_RETRY_MS);
	}, []);

	// Where you are: the rows on screen, each row's top, and the scroll.
	const viewable = useRef<number[]>([]);
	const rowTops = useRef(new Map<number, number>()).current;
	const metrics = useRef<ScrollMetrics>({ offset: 0, viewport: 0, content: 0 });
	const position = useCallback((): ReadingPosition | null => {
		const top = Math.min(...viewable.current);
		const place = places[top];
		const { offset, viewport, content } = metrics.current;
		if (!place || viewport <= 0 || content <= 0) return null;
		return {
			blockIndex: place.index,
			blockHash: place.hash,
			offset: Math.max(0, offset - (rowTops.get(top) ?? offset)),
			progress: Math.min(1, Math.max(0, (offset + viewport) / content)),
		};
	}, [places, rowTops]);

	// The nav bar takes the title once the first heading (or the first row)
	// has scrolled out of view and a later row is on screen.
	const anchor = useRef(0);
	useEffect(() => {
		anchor.current = headings[0]?.index ?? 0;
	}, [headings]);
	const [titleShown, setTitleShown] = useState(false);
	const viewabilityChanged = useRef(({ viewableItems }: { viewableItems: Pick<ViewToken, "index">[] }) => {
		viewable.current = viewableItems.flatMap((token) => (token.index === null ? [] : [token.index]));
		setTitleShown(
			!viewable.current.includes(anchor.current) && viewable.current.some((index) => index > anchor.current),
		);
	}).current;

	const tracked = (event: NativeSyntheticEvent<NativeScrollEvent>) => {
		const { contentOffset, layoutMeasurement, contentSize } = event.nativeEvent;
		metrics.current = { offset: contentOffset.y, viewport: layoutMeasurement.height, content: contentSize.height };
	};
	const scrollEnded = (event: NativeSyntheticEvent<NativeScrollEvent>) => {
		tracked(event);
		const at = position();
		if (at) memory.savePosition(key, at);
	};

	// Opening marks the Board's way back done, then reopens where you were,
	// once, when the first read lands.
	const restored = useRef(false);
	useEffect(() => {
		if (restored.current || places.length === 0) return;
		restored.current = true;
		const remembered = memory.position(key);
		const at = remembered ? restoreBlock(remembered, places) : null;
		if (at && (at.index > 0 || at.offset > 0)) scrollTo({ index: at.index, viewOffset: -at.offset, animated: false });
	}, [places, memory, key, scrollTo]);

	// Leaving (a screen pushed over this one, never its own sheets, or
	// closing it) records what you read and where you were.
	const leave = useRef(() => {});
	leave.current = () => {
		if (!document || !READABLE.has(document.kind)) return;
		memory.left(key, {
			title: document.title,
			reference,
			blocks: blocks?.map((block) => block.hash) ?? [],
			position: position(),
			sessionTitle,
			...(updatedAt === undefined ? {} : { updatedAt }),
		});
	};
	const wasInFront = useRef(inFront);
	useEffect(() => {
		const was = wasInFront.current;
		wasInFront.current = inFront;
		if (was && !inFront) leave.current();
		if (!was && inFront) {
			memory.opened(key);
		}
	}, [inFront, memory, key]);
	useEffect(() => {
		memory.opened(key);
		return () => {
			if (wasInFront.current) leave.current();
		};
	}, [memory, key]);

	// The document's session ending a turn may have rewritten the file. The
	// same read says whether it can take the review: the review always goes to
	// the session the document was opened in (ruling 16), which is this one.
	const [canReview, setCanReview] = useState(false);
	useEffect(() => {
		if (!inFront || !client || activeProfile?.id !== hubId || state !== "ready") return;
		const link = new SessionLink(client, sessionRef);
		let status: string | null = null;
		const unsubscribe = link.subscribe(() => {
			const session = link.getSnapshot();
			const next = session?.status ?? null;
			if (status === "active" && next !== "active") reload();
			status = next;
			if (session?.cwd)
				setPublication((previous) =>
					previous?.source === source && previous.cwd === session.cwd ? previous : { source, cwd: session.cwd },
				);
			setCanReview(Boolean(session?.capabilities.send || session?.capabilities.queue));
		});
		link.read({ follow: true }).catch(() => {
			// Quiet: the Reader shows the document it has either way.
		});
		return () => {
			unsubscribe();
			link.dispose();
		};
	}, [inFront, client, activeProfile?.id, hubId, state, sessionRef, source, reload]);
	useEffect(() => {
		if (reference !== routeReference) navigation.setParams({ reference });
	}, [reference, routeReference, navigation]);

	// Your comments on this document, followed as the comment sheets change
	// them, and how many sit on each block now (ruling 14).
	const revision = useSyncExternalStore(memory.subscribe, memory.getRevision);
	// biome-ignore lint/correctness/useExhaustiveDependencies: the revision is what says the comments changed
	const comments = useMemo(() => memory.comments(key), [memory, key, revision]);
	const commentCounts = useMemo(() => {
		const counts = new Map<number, number>();
		if (!blocks) return counts;
		for (const comment of comments) {
			const index = anchorBlock(comment, blocks);
			if (index !== null) counts.set(index, (counts.get(index) ?? 0) + 1);
		}
		return counts;
	}, [comments, blocks]);

	const host = useMemo<ReaderHost>(
		() => ({
			outline: headings,
			jumpTo: (index) => scrollTo({ index, animated: true }),
			anchor: (comment) => (blocks ? anchorBlock(comment, blocks) : null),
			canReview,
		}),
		[headings, scrollTo, blocks, canReview],
	);
	useProvideSheetHost(readerHosts, sheetKey(hubId, sessionRef, path), host);

	const updated = updatedAt === undefined ? Number.NaN : Date.parse(updatedAt);
	const about = Number.isNaN(updated)
		? documentKind(path)
		: `${documentKind(path)} · updated ${timeAgo(now - updated)}`;
	const changeNote = lastRead ? changesCaption(changed.length, lastRead.readAt, now) : null;
	const title = document?.title ?? "";
	const text = document?.kind === "markdown" || document?.kind === "code" ? document.text : null;
	const hasOutline = headings.length >= 2;

	useEffect(() => {
		const items: NativeStackHeaderItem[] = [
			...(hasOutline
				? [
						{
							type: "button" as const,
							label: "Outline",
							accessibilityLabel: "Outline",
							icon: { type: "sfSymbol" as const, name: "list.bullet.indent" as const },
							onPress: () => navigation.navigate("OutlineSheet", { hubId, sessionRef, path }),
						},
					]
				: []),
			{
				type: "menu",
				label: "Document actions",
				accessibilityLabel: "Document actions",
				icon: { type: "sfSymbol", name: "ellipsis.circle" },
				menu: {
					items: [
						{ type: "action", label: "Reload", onPress: reload },
						{
							type: "action",
							label: "Open session",
							onPress: () => returnToSession(navigation, { hubId, ref: sessionRef, title: sessionTitle }),
						},
						{ type: "action", label: "Copy path", onPress: () => void copyText(path) },
						...(text === null
							? []
							: [{ type: "action" as const, label: "Copy text", onPress: () => void copyText(text) }]),
					],
				},
			},
		];
		navigation.setOptions({
			title: "",
			// iPhone only, as the Session's: Android keeps its own back arrow.
			...(Platform.OS === "ios" && {
				headerLeft: () => (
					<BackButton
						count={held}
						label={held > 0 ? `Back, ${held} new while you read` : "Back"}
						onPress={() => navigation.goBack()}
					/>
				),
			}),
			headerTitle: () => (titleShown ? <HeaderTitle title={title} caption={about} /> : null),
			unstable_headerRightItems: () => items,
		});
	}, [navigation, held, hasOutline, hubId, sessionRef, path, sessionTitle, text, titleShown, title, about, reload]);

	const cellRenderer = useMemo(
		() =>
			class ReaderCell extends Component<CellRendererProps<Row>> {
				render() {
					const { children, index, onLayout, style } = this.props;
					return (
						<View
							style={style}
							onLayout={(event) => {
								onLayout?.(event);
								rowTops.set(index, event.nativeEvent.layout.y);
							}}
						>
							{children}
						</View>
					);
				}
			},
		[rowTops],
	);

	// The change you stepped to, in the set of changes it belongs to: a
	// re-read that changes what changed starts the steps over. Every re-read
	// builds new arrays, so the set is compared by its blocks.
	const changeSet = changed.join(",");
	const [stepped, setStepped] = useState<{ changeSet: string; step: number } | null>(null);
	const step = stepped?.changeSet === changeSet ? stepped.step : null;
	const stepBy = (delta: 1 | -1) => {
		const count = changed.length;
		const next = step === null ? (delta === 1 ? 0 : count - 1) : (step + delta + count) % count;
		setStepped({ changeSet, step: next });
		const index = changed[next];
		if (index !== undefined) scrollTo({ index, animated: true });
	};

	// The block whose menu is open, and the one Select text chose. Pressing
	// another block ends a selection, and so does a tap on any block: the
	// markdown view doesn't say when its selection menu closes.
	const [menuOpen, setMenuOpen] = useState<number | null>(null);
	const [selecting, setSelecting] = useState<number | null>(null);
	const onMenu = useCallback((index: number | null) => {
		setMenuOpen(index);
		if (index !== null) setSelecting((current) => (current === index ? current : null));
	}, []);
	const endSelection = useCallback(() => setSelecting(null), []);
	const sheetParams = { hubId, sessionRef, path, sessionTitle };
	// The rows keep one callback; it reaches this render's values through the ref.
	const act = useRef((_action: BlockAction, _block: DocumentBlock, _words?: string) => {});
	act.current = (action, block, words) => {
		const selected = words ?? block.text;
		switch (action) {
			case "comment":
				setSelecting(null);
				navigation.navigate("CommentSheet", {
					hubId,
					sessionRef,
					path,
					blockIndex: block.index,
					blockHash: block.hash,
					quote: selected,
				});
				return;
			case "quote":
				setSelecting(null);
				holdQuote(hubId, sessionRef, selected);
				returnToSession(navigation, { hubId, ref: sessionRef, title: sessionTitle });
				return;
			case "copy":
				void copyText(block.text);
				return;
			case "select":
				setSelecting(block.index);
				return;
			case "comments":
				navigation.navigate("CommentsSheet", sheetParams);
		}
	};
	const onAction = useCallback(
		(action: BlockAction, block: DocumentBlock, words?: string) => act.current(action, block, words),
		[],
	);

	const rowState = useMemo(
		() => ({ changedSet, menuOpen, selecting, commentCounts }),
		[changedSet, menuOpen, selecting, commentCounts],
	);
	const lines = document?.kind === "code" ? rows.length : 0;
	const notice = refreshNotice ?? (document ? documentNotice(document) : null);
	// The caption and the truncation note: 13/18 in ink-low.
	const caption = { color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale };
	const header = (
		<View style={{ paddingTop: 8, paddingBottom: 14, gap: 4 }}>
			{document ? (
				<Text allowFontScaling={allowFontScaling} style={caption}>
					{about}
					{changeNote ? (
						<>
							{" · "}
							<Text style={{ color: palette.accentInk }}>{changeNote}</Text>
						</>
					) : null}
				</Text>
			) : (
				<DocumentSkeleton />
			)}
			{document && "truncated" in document && document.truncated ? (
				<Text allowFontScaling={allowFontScaling} style={caption}>
					{truncationNote(document.truncated)}
				</Text>
			) : null}
			{notice ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ marginTop: 10, color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
				>
					{notice}
				</Text>
			) : null}
			{document?.kind === "image" && imageAttempt ? (
				<DocumentImage hubId={hubId} sessionRef={sessionRef} reference={reference} attempt={imageAttempt} />
			) : null}
		</View>
	);

	return (
		<View style={{ flex: 1, backgroundColor: palette.page }}>
			<FlatList
				ref={list}
				data={rows}
				keyExtractor={(row) => (row.kind === "block" ? `b${row.block.index}` : `l${row.index}`)}
				renderItem={({ item }) =>
					item.kind === "block" ? (
						<ReaderBlock
							block={item.block}
							changed={changedSet.has(item.block.index)}
							selected={menuOpen === item.block.index}
							selecting={selecting === item.block.index}
							commentCount={commentCounts.get(item.block.index)}
							onAction={onAction}
							onMenu={onMenu}
							onTap={endSelection}
						/>
					) : (
						<CodeLine number={item.index + 1} text={item.text} lines={lines} />
					)
				}
				ItemSeparatorComponent={document?.kind === "markdown" ? BlockGap : undefined}
				extraData={rowState}
				ListHeaderComponent={header}
				CellRendererComponent={cellRenderer}
				initialNumToRender={12}
				contentContainerStyle={{ paddingHorizontal: 16, paddingBottom: 24 }}
				onViewableItemsChanged={viewabilityChanged}
				onScroll={tracked}
				scrollEventThrottle={100}
				onMomentumScrollEnd={scrollEnded}
				onScrollEndDrag={scrollEnded}
				onLayout={(event) => {
					metrics.current = { ...metrics.current, viewport: event.nativeEvent.layout.height };
				}}
				onContentSizeChange={(_width, height) => {
					metrics.current = { ...metrics.current, content: height };
				}}
				onScrollToIndexFailed={scrollFailed}
			/>
			{blocks ? (
				<View style={{ paddingBottom: insets.bottom, paddingHorizontal: 16 }}>
					{comments.length === 0 ? <CommentTip /> : null}
					{/* The two ends take the room their words need, and the change
					    stepper the rest: in equal thirds Send review wrapped. */}
					<View style={{ minHeight: 44, flexDirection: "row", alignItems: "center" }}>
						<View style={{ alignItems: "flex-start" }}>
							{comments.length > 0 ? (
								<BarButton
									label="Comments"
									symbol="bubble.left"
									text={String(comments.length)}
									onPress={() => navigation.navigate("CommentsSheet", sheetParams)}
								/>
							) : null}
						</View>
						{changed.length > 0 ? (
							<View style={{ flex: 1, flexDirection: "row", alignItems: "center", justifyContent: "center" }}>
								<Chevron name="chevron.left" label="Previous change" onPress={() => stepBy(-1)} />
								<Text
									allowFontScaling={allowFontScaling}
									style={{
										color: palette.inkMid,
										fontSize: 15 * scale,
										lineHeight: 20 * scale,
										minWidth: 120,
										textAlign: "center",
									}}
								>
									{step === null
										? `${changed.length} ${changed.length === 1 ? "change" : "changes"}`
										: `Change ${step + 1} of ${changed.length}`}
								</Text>
								<Chevron name="chevron.right" label="Next change" onPress={() => stepBy(1)} />
							</View>
						) : (
							<View style={{ flex: 1 }} />
						)}
						<View style={{ alignItems: "flex-end" }}>
							{canReview ? (
								<Pressable
									accessibilityRole="button"
									accessibilityLabel="Send review"
									onPress={() => navigation.navigate("ReviewSheet", sheetParams)}
									style={{ minHeight: 44, justifyContent: "center" }}
								>
									<Text
										allowFontScaling={allowFontScaling}
										numberOfLines={1}
										style={{
											color: palette.accentInk,
											fontSize: 15 * scale,
											lineHeight: 20 * scale,
											fontWeight: "600",
										}}
									>
										Send review
									</Text>
								</Pressable>
							) : null}
						</View>
					</View>
				</View>
			) : null}
		</View>
	);
}

/** Until this document has a comment, how to leave one. */
function CommentTip() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", alignItems: "center", justifyContent: "center", gap: 6, paddingTop: 8 }}>
			<SymbolView name="bubble.left" size={13} tintColor={palette.inkLow} />
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 17 * scale }}
			>
				Touch and hold a paragraph to comment on it
			</Text>
		</View>
	);
}

/** A bottom-bar button: a symbol and a short word, 44pt tall. */
function BarButton({
	label,
	symbol,
	text,
	onPress,
}: {
	label: string;
	symbol: "bubble.left";
	text: string;
	onPress(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityValue={{ text }}
			onPress={onPress}
			style={{ minHeight: 44, minWidth: 44, flexDirection: "row", alignItems: "center", gap: 6 }}
		>
			<SymbolView name={symbol} size={17} tintColor={palette.accentInk} />
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.accentInk, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{text}
			</Text>
		</Pressable>
	);
}

function BlockGap() {
	return <View style={{ height: 14 }} />;
}

function HeaderTitle({ title, caption }: { title: string; caption: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ alignItems: "center" }}>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale, fontWeight: "600" }}
			>
				{title}
			</Text>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale }}
			>
				{caption}
			</Text>
		</View>
	);
}

function Chevron({ name, label, onPress }: { name: "chevron.left" | "chevron.right"; label: string; onPress(): void }) {
	const { palette } = useColors();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onPress}
			style={{ width: 44, height: 44, alignItems: "center", justifyContent: "center" }}
		>
			<SymbolView name={name} size={17} tintColor={palette.accentInk} />
		</Pressable>
	);
}

/** Three quiet lines until the first read lands: no spinner, no shimmer. */
function DocumentSkeleton() {
	const { palette } = useColors();
	return (
		<View accessible accessibilityLabel="Loading document" style={{ gap: 10 }}>
			{(["92%", "100%", "64%"] as const).map((width) => (
				<View key={width} style={{ width, height: 14, borderRadius: 4, backgroundColor: palette.inset }} />
			))}
		</View>
	);
}

/** A code file's line: its number in a right-aligned gutter, then the line,
 * wrapping. */
function CodeLine({ number, text, lines }: { number: number; text: string; lines: number }) {
	const { palette } = useColors();
	const code = useCodeText();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", gap: 12 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ ...code, color: palette.inkLow, textAlign: "right", width: String(lines).length * 8 * scale }}
			>
				{number}
			</Text>
			<Text allowFontScaling={allowFontScaling} selectable style={{ ...code, flex: 1 }}>
				{text}
			</Text>
		</View>
	);
}

/** An image file, through the hub's /doc/image with this hub's token, fit to
 * the width at its own aspect ratio. */
function DocumentImage({
	hubId,
	sessionRef,
	reference,
	attempt,
}: {
	hubId: string;
	sessionRef: string;
	reference: FileReference;
	attempt: NativeImageAttempt;
}) {
	const { profiles } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin ?? "";
	const identity = JSON.stringify([hubId, origin, sessionRef, reference]);
	const current = useRef({ identity, generation: attempt.generation, settled: false });
	if (current.current.identity !== identity || current.current.generation !== attempt.generation)
		current.current = { identity, generation: attempt.generation, settled: false };
	const [credential, setCredential] = useState<{ identity: string; token: string } | null>(null);
	const [healthy, setHealthy] = useState<{
		identity: string;
		source: { uri: string; headers: { Authorization: string } };
		aspect: number;
	} | null>(null);
	useEffect(() => {
		let live = true;
		hubs
			.token(hubId)
			.then((value) => {
				if (live) setCredential({ identity, token: value });
			})
			.catch(() => {
				if (live) attempt.failed();
			});
		return () => {
			live = false;
		};
	}, [hubId, identity, attempt]);
	if (credential?.identity !== identity) return null;
	const source = {
		uri: docImageReadURL(origin, sessionRef, reference.readTarget, attempt.generation),
		headers: { Authorization: `Bearer ${credential.token}` },
	};
	const shown = healthy?.identity === identity ? healthy : null;
	const pending = shown?.source.uri !== source.uri;
	const isCurrent = () =>
		attempt.isCurrent() &&
		current.current.identity === identity &&
		current.current.generation === attempt.generation &&
		!current.current.settled;
	return (
		<View>
			{shown ? (
				<Image
					source={shown.source}
					accessibilityLabel={reference.path}
					resizeMode="contain"
					style={{ marginTop: 10, width: "100%", aspectRatio: shown.aspect }}
				/>
			) : null}
			{pending ? (
				<Image
					key={attempt.generation}
					source={source}
					accessibilityLabel={shown ? undefined : reference.path}
					resizeMode="contain"
					onLoad={(event) => {
						if (!isCurrent()) return;
						current.current.settled = true;
						const { width, height } = event.nativeEvent.source;
						setHealthy({ identity, source, aspect: width > 0 && height > 0 ? width / height : 4 / 3 });
						attempt.loaded();
					}}
					onError={() => {
						if (isCurrent()) {
							current.current.settled = true;
							attempt.failed();
						}
					}}
					style={
						shown
							? { position: "absolute", width: 1, height: 1, opacity: 0 }
							: { marginTop: 10, width: "100%", aspectRatio: 4 / 3 }
					}
				/>
			) : null}
		</View>
	);
}
