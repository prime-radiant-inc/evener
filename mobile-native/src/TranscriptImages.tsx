import * as SecureStore from "expo-secure-store";
import { useEffect, useRef, useState } from "react";
import { ActivityIndicator, FlatList, Image, Pressable, ScrollView, useWindowDimensions, View } from "react-native";
import type { AttachmentRef } from "./projectedRows";
import { useConnection } from "./ConnectionProvider";
import { HubProfiles } from "./connection";
import { ModalSheet } from "./sheet/ModalSheet";
import { transcriptImageSource } from "./transcriptImageSource";
import { Copy, useColors } from "./ui";

const hubs = new HubProfiles(SecureStore);

function HubImage({ image, hubId }: { image: AttachmentRef; hubId: string }) {
	const { profiles } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin;
	const colors = useColors();
	const [source, setSource] = useState<ReturnType<typeof transcriptImageSource> | null>(null);
	const [error, setError] = useState(false);
	const [loading, setLoading] = useState(true);
	useEffect(() => {
		let cancelled = false;
		setSource(null);
		setError(false);
		setLoading(true);
		async function load() {
			const token = image.src.startsWith("data:") ? "" : await hubs.token(hubId);
			const next = transcriptImageSource(image.src, origin ?? "", token);
			if (!cancelled) setSource(next);
		}
		void load().catch(() => {
			if (!cancelled) {
				setError(true);
				setLoading(false);
			}
		});
		return () => {
			cancelled = true;
		};
	}, [image.src, hubId, origin]);
	return (
		<View style={{ flex: 1, alignItems: "center", justifyContent: "center" }}>
			{source && !error ? (
				<Image
					source={source}
					resizeMode="contain"
					accessibilityLabel={image.name ?? "Attached image"}
					style={{ width: "100%", height: "100%" }}
					onLoadEnd={() => setLoading(false)}
					onError={() => {
						setError(true);
						setLoading(false);
					}}
				/>
			) : null}
			{loading ? (
				<ActivityIndicator accessibilityLabel="Loading image" color={colors.accent} style={{ position: "absolute" }} />
			) : null}
			{error ? <Copy muted>Image unavailable</Copy> : null}
		</View>
	);
}

// A row of images in the transcript (spec 8.2, "Images"): 96pt thumbnails,
// and a viewer that swipes between them.
export function TranscriptImages({ images, hubId }: { images: AttachmentRef[]; hubId: string }) {
	const colors = useColors();
	const { width } = useWindowDimensions();
	// The page the viewer shows, or null while it's closed.
	const [page, setPage] = useState<number | null>(null);
	const pager = useRef<FlatList<AttachmentRef>>(null);
	// VoiceOver moves between images with a swipe up or down on the viewer
	// (the adjustable actions), as well as by paging.
	function turnTo(next: number) {
		if (next < 0 || next >= images.length) return;
		setPage(next);
		pager.current?.scrollToIndex({ index: next, animated: true });
	}
	return (
		<>
			<ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 8 }}>
				{images.map((image, position) => (
					<Pressable
						key={image.id}
						accessibilityRole="button"
						accessibilityLabel={`Open image ${position + 1} of ${images.length}: ${image.name ?? "attachment"}`}
						onPress={() => setPage(position)}
						style={{
							width: 96,
							height: 96,
							borderRadius: 12,
							overflow: "hidden",
							backgroundColor: colors.surface,
						}}
					>
						<HubImage image={image} hubId={hubId} />
					</Pressable>
				))}
			</ScrollView>
			{page !== null ? (
				<ModalSheet
					title={images[page]?.name ?? "Attached image"}
					accessory={
						<View style={{ alignItems: "center", paddingVertical: 6 }}>
							<Copy muted>{`${page + 1} of ${images.length}`}</Copy>
						</View>
					}
					done={{ onPress: () => setPage(null) }}
					onRequestClose={() => setPage(null)}
				>
					<FlatList
						ref={pager}
						accessible
						accessibilityRole="adjustable"
						accessibilityLabel="Images"
						accessibilityValue={{ text: `Image ${page + 1} of ${images.length}` }}
						accessibilityActions={[
							{ name: "increment", label: "Next image" },
							{ name: "decrement", label: "Previous image" },
						]}
						onAccessibilityAction={(event) => turnTo(page + (event.nativeEvent.actionName === "increment" ? 1 : -1))}
						data={images}
						keyExtractor={(image) => image.id}
						horizontal
						pagingEnabled
						showsHorizontalScrollIndicator={false}
						initialScrollIndex={page}
						getItemLayout={(_data, index) => ({ length: width, offset: width * index, index })}
						onMomentumScrollEnd={(event) => setPage(Math.round(event.nativeEvent.contentOffset.x / Math.max(1, width)))}
						renderItem={({ item }) => (
							<View style={{ width, flex: 1, padding: 16 }}>
								<HubImage image={item} hubId={hubId} />
							</View>
						)}
					/>
				</ModalSheet>
			) : null}
		</>
	);
}
