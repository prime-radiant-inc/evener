// The Reader (spec 10.2): a document from a session's folder, read quietly.
// It marks what changed since you last read it and steps through those
// changes, reopens where you were, offers its outline, and remembers what you
// read when you leave. It never shows a Retry, Refresh or Reconnect: it reads
// again on its own when it comes back to the front, when the connection
// returns, and when the document's session ends a turn.
import type { NativeStackHeaderItem, NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import * as SecureStore from "expo-secure-store";
import { Component, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	FlatList,
	type CellRendererProps,
	Image,
	type NativeScrollEvent,
	type NativeSyntheticEvent,
	Pressable,
	Text,
	View,
	type ViewToken,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { copyText } from "../clipboard";
import { useConnection } from "../ConnectionProvider";
import { HubProfiles } from "../connection";
import { nativeDocImageSource } from "../nativeDocPort";
import type { Routes } from "../screens";
import { compactDuration } from "../session/format";
import { returnToSession } from "../session/returnToSession";
import { SessionLink } from "../session/sessionMessage";
import { sheetKey, useProvideSheetHost } from "../sheet/sheetHosts";
import { useScreenInFront } from "../sheet/useScreenInFront";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type DocumentBlock, outline } from "./documentBlocks";
import { changedBlocks, changesCaption, restoreBlock } from "./documentChanges";
import type { ReadingPosition } from "./documentMemory";
import { documentKind, documentNotice, type LoadedDocument, truncationNote } from "./documentSource";
import { documentMemory } from "./nativeDocumentMemory";
import { ReaderBlock, useCodeText } from "./ReaderBlock";
import { type ReaderHost, readerHosts } from "./readerHosts";
import { useDocument } from "./useDocument";

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
	const { hubId, sessionRef, path, reviewRef, reviewTitle, updatedAt } = route.params;
	const key = useMemo(() => ({ sessionRef, path }), [sessionRef, path]);
	const memory = documentMemory(hubId);
	const { client } = useConnection();
	const { document, reload } = useDocument(hubId, sessionRef, path);
	const inFront = useScreenInFront(route.key);
	const { palette } = useColors();
	const scale = useTextScale();
	const insets = useSafeAreaInsets();
	const list = useRef<FlatList<Row>>(null);

	// This visit's view of the past: the version you last read, and the
	// clock for "updated 3m ago". A re-read during the visit compares against
	// the same last read.
	const [lastRead] = useState(() => memory.lastRead(key));
	const [now] = useState(Date.now);
	const blocks = document?.kind === "markdown" ? document.blocks : null;
	const rows = useMemo(() => rowsOf(document), [document]);
	const changed = useMemo(
		() => (blocks ? changedBlocks(blocks, lastRead?.blocks ?? null) : []),
		[blocks, lastRead],
	);
	const changedSet = useMemo(() => new Set(changed), [changed]);
	const headings = useMemo(() => (blocks ? outline(blocks) : []), [blocks]);

	// Scrolling to a row, and trying again while the list hasn't measured it.
	const target = useRef<{ index: number; viewOffset?: number; animated: boolean } | null>(null);
	const tries = useRef(0);
	const retry = useRef<ReturnType<typeof setTimeout> | null>(null);
	const scrollTo = useCallback((next: { index: number; viewOffset?: number; animated: boolean }) => {
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
		const block = blocks?.[top];
		const { offset, viewport, content } = metrics.current;
		if (!block || viewport <= 0 || content <= 0) return null;
		return {
			blockIndex: block.index,
			blockHash: block.hash,
			offset: Math.max(0, offset - (rowTops.get(top) ?? offset)),
			progress: Math.min(1, Math.max(0, (offset + viewport) / content)),
		};
	}, [blocks, rowTops]);

	// The nav bar takes the title once the first heading (or the first row)
	// has scrolled out of view and a later row is on screen.
	const anchor = useRef(0);
	anchor.current = headings[0]?.index ?? 0;
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
		if (restored.current || !blocks) return;
		restored.current = true;
		const remembered = memory.position(key);
		const at = remembered ? restoreBlock(remembered, blocks) : null;
		if (at && (at.index > 0 || at.offset > 0)) scrollTo({ index: at.index, viewOffset: -at.offset, animated: false });
	}, [blocks, memory, key, scrollTo]);

	// Leaving (a screen pushed over this one, never its own sheets, or
	// closing it) records what you read and where you were.
	const leave = useRef(() => {});
	leave.current = () => {
		if (!document || !READABLE.has(document.kind)) return;
		memory.left(key, {
			title: document.title,
			blocks: blocks?.map((block) => block.hash) ?? [],
			position: position(),
			reviewRef,
			reviewTitle,
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
			reload();
		}
	}, [inFront, memory, key, reload]);
	useEffect(() => {
		memory.opened(key);
		return () => {
			if (wasInFront.current) leave.current();
		};
	}, [memory, key]);

	// The document's session ending a turn may have rewritten the file.
	useEffect(() => {
		if (!inFront || !client) return;
		const link = new SessionLink(client, sessionRef);
		let status: string | null = null;
		const unsubscribe = link.subscribe(() => {
			const next = link.getSnapshot()?.status ?? null;
			if (status === "active" && next !== "active") reload();
			status = next;
		});
		link.read({ follow: true }).catch(() => {
			// Quiet: the Reader shows the document it has either way.
		});
		return () => {
			unsubscribe();
			link.dispose();
		};
	}, [inFront, client, sessionRef, reload]);

	const host = useMemo<ReaderHost>(
		() => ({ outline: headings, jumpTo: (index) => scrollTo({ index, animated: true }) }),
		[headings, scrollTo],
	);
	useProvideSheetHost(readerHosts, sheetKey(hubId, sessionRef, path), host);

	const updated = updatedAt === undefined ? Number.NaN : Date.parse(updatedAt);
	const about = [documentKind(path), ...(Number.isNaN(updated) ? [] : [`updated ${compactDuration(now - updated)} ago`])].join(
		" · ",
	);
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
						{
							type: "action",
							label: "Open session",
							onPress: () => returnToSession(navigation, { hubId, ref: reviewRef, title: reviewTitle }),
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
			headerTitle: () => (titleShown ? <HeaderTitle title={title} caption={about} /> : null),
			unstable_headerRightItems: () => items,
		});
	}, [navigation, hasOutline, hubId, sessionRef, path, reviewRef, reviewTitle, text, titleShown, title, about]);

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

	const [step, setStep] = useState<number | null>(null);
	const stepBy = (delta: 1 | -1) => {
		const count = changed.length;
		const next = step === null ? (delta === 1 ? 0 : count - 1) : (step + delta + count) % count;
		setStep(next);
		const index = changed[next];
		if (index !== undefined) scrollTo({ index, animated: true });
	};

	const lines = document?.kind === "code" ? rows.length : 0;
	const notice = document ? documentNotice(document) : null;
	const header = (
		<View style={{ paddingTop: 8, paddingBottom: 14, gap: 4 }}>
			{document ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
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
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
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
			{document?.kind === "image" ? <DocumentImage hubId={hubId} sessionRef={sessionRef} path={path} /> : null}
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
						<ReaderBlock block={item.block} changed={changedSet.has(item.block.index)} />
					) : (
						<CodeLine number={item.index + 1} text={item.text} lines={lines} />
					)
				}
				ItemSeparatorComponent={document?.kind === "markdown" ? BlockGap : undefined}
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
			{changed.length > 0 ? (
				<View
					style={{
						flexDirection: "row",
						alignItems: "center",
						justifyContent: "center",
						paddingBottom: insets.bottom,
					}}
				>
					<Chevron name="chevron.left" label="Previous change" onPress={() => stepBy(-1)} />
					<Text
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, minWidth: 120, textAlign: "center" }}
					>
						{step === null
							? `${changed.length} ${changed.length === 1 ? "change" : "changes"}`
							: `Change ${step + 1} of ${changed.length}`}
					</Text>
					<Chevron name="chevron.right" label="Next change" onPress={() => stepBy(1)} />
				</View>
			) : null}
		</View>
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
function DocumentImage({ hubId, sessionRef, path }: { hubId: string; sessionRef: string; path: string }) {
	const { profiles } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin ?? "";
	const [token, setToken] = useState<string | null>(null);
	const [aspect, setAspect] = useState(4 / 3);
	useEffect(() => {
		let current = true;
		hubs
			.token(hubId)
			.then((value) => {
				if (current) setToken(value);
			})
			.catch(() => {
				if (current) setToken("");
			});
		return () => {
			current = false;
		};
	}, [hubId]);
	if (token === null) return null;
	return (
		<Image
			source={nativeDocImageSource(origin, token, sessionRef, path)}
			accessibilityLabel={path}
			resizeMode="contain"
			onLoad={(event) => {
				const { width, height } = event.nativeEvent.source;
				if (width > 0 && height > 0) setAspect(width / height);
			}}
			style={{ marginTop: 10, width: "100%", aspectRatio: aspect }}
		/>
	);
}
