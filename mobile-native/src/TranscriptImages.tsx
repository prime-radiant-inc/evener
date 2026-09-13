import * as SecureStore from "expo-secure-store";
import { useEffect, useState } from "react";
import {
	ActivityIndicator,
	Image,
	Modal,
	Pressable,
	ScrollView,
	View,
} from "react-native";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import type { AttachmentRef } from "../../mobile/src/conversation/model";
import { useConnection } from "./ConnectionProvider";
import { HubProfiles } from "./connection";
import { transcriptImageSource } from "./transcriptImageSource";
import { Action, Copy, useColors } from "./ui";

const hubs = new HubProfiles(SecureStore);

function HubImage({ image, hubId }: { image: AttachmentRef; hubId: string }) {
	const { profiles } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin;
	const colors = useColors();
	const [source, setSource] = useState<ReturnType<
		typeof transcriptImageSource
	> | null>(null);
	const [error, setError] = useState(false);
	const [loading, setLoading] = useState(true);
	useEffect(() => {
		let cancelled = false;
		setSource(null);
		setError(false);
		setLoading(true);
		async function load() {
			const token = image.src.startsWith("data:")
				? ""
				: await hubs.token(hubId);
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
				<ActivityIndicator
					accessibilityLabel="Loading image"
					color={colors.accent}
					style={{ position: "absolute" }}
				/>
			) : null}
			{error ? <Copy muted>Image unavailable</Copy> : null}
		</View>
	);
}

export function TranscriptImages({
	images,
	hubId,
}: {
	images: AttachmentRef[];
	hubId: string;
}) {
	const colors = useColors();
	const [openId, setOpenId] = useState<string | null>(null);
	const index = images.findIndex((image) => image.id === openId);
	const current = images[index];
	function step(direction: number) {
		const next = images[(index + direction + images.length) % images.length];
		if (next) setOpenId(next.id);
	}
	return (
		<>
			<ScrollView
				horizontal
				showsHorizontalScrollIndicator={false}
				contentContainerStyle={{ gap: 12 }}
			>
				{images.map((image, position) => (
					<View key={image.id} style={{ width: 112, gap: 4 }}>
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={`Open image ${position + 1} of ${images.length}: ${image.name ?? "attachment"}`}
							onPress={() => setOpenId(image.id)}
							style={{
								height: 112,
								borderRadius: 12,
								overflow: "hidden",
								backgroundColor: colors.surface,
							}}
						>
							<HubImage image={image} hubId={hubId} />
						</Pressable>
						<Copy muted numberOfLines={2} ellipsizeMode="middle">
							{image.name ?? `Image ${position + 1}`}
						</Copy>
					</View>
				))}
			</ScrollView>
			{current ? (
				<Modal
					animationType="fade"
					presentationStyle="fullScreen"
					onRequestClose={() => setOpenId(null)}
				>
					<SafeAreaProvider>
						<SafeAreaView
							style={{ flex: 1, backgroundColor: colors.background }}
						>
							<View
								style={{
									padding: 16,
									flexDirection: "row",
									alignItems: "center",
									gap: 12,
								}}
							>
								<View style={{ flex: 1 }}>
									<Copy>{current.name ?? "Attached image"}</Copy>
									<Copy muted>
										{index + 1} of {images.length}
									</Copy>
								</View>
								<Action onPress={() => setOpenId(null)}>Done</Action>
							</View>
							<View style={{ flex: 1, padding: 16 }}>
								<HubImage key={current.id} image={current} hubId={hubId} />
							</View>
							{images.length > 1 ? (
								<View
									style={{
										flexDirection: "row",
										justifyContent: "space-between",
										padding: 16,
									}}
								>
									<Action onPress={() => step(-1)}>Previous image</Action>
									<Action onPress={() => step(1)}>Next image</Action>
								</View>
							) : null}
						</SafeAreaView>
					</SafeAreaProvider>
				</Modal>
			) : null}
		</>
	);
}
