// Adding a hub (spec 15): scan the pairing code Evener shows on a computer,
// paste its pairing link, or type an address and token by hand (ruling 23).
// Each way ends on one review step that names the hub and saves it. The token
// is held in state and handed to saveHub; it is never rendered as text.
import { CameraView, useCameraPermissions } from "expo-camera";
import { getStringAsync } from "expo-clipboard";
import { useEffect, useRef, useState } from "react";
import { Linking, Text, TextInput, type TextInputProps, View } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { fonts } from "../design/tokens";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type PairingTarget, pairingFrom, suggestedHubName } from "./pairing";

type How = "scan" | "paste" | "address";

interface Review {
	target: PairingTarget;
	name: string;
}

export function AddHub({ how: initialHow, onConnected }: { how: How; onConnected(): void }) {
	const { saveHub } = useConnection();
	const [how, setHow] = useState(initialHow);
	const [review, setReview] = useState<Review | null>(null);
	const [saving, setSaving] = useState(false);
	const [refused, setRefused] = useState(false);

	// The first pairing link wins: the camera keeps reporting codes until it
	// unmounts, and a later one must not replace the hub being reviewed.
	function startReview(target: PairingTarget) {
		setReview((current) => current ?? { target, name: suggestedHubName(target.origin) });
	}

	async function connect() {
		if (!review || saving) return;
		setSaving(true);
		setRefused(false);
		try {
			const selected = await saveHub({
				name: review.name,
				origin: review.target.origin,
				token: review.target.token,
			});
			if (selected) onConnected();
		} catch {
			setRefused(true);
		} finally {
			setSaving(false);
		}
	}

	if (review)
		return (
			<GroupedPage>
				<GroupLabel>Pair with</GroupLabel>
				<Group>
					<MachineRow text={review.target.origin} />
				</Group>
				<GroupLabel>Name</GroupLabel>
				<Group>
					<Field
						label="Name"
						value={review.name}
						editable={!saving}
						onChangeText={(name) => setReview({ ...review, name })}
					/>
				</Group>
				<GroupGap />
				<Group>
					<Row
						label={saving ? "Connecting…" : "Connect"}
						tone="accent"
						disabled={saving}
						onPress={() => void connect()}
					/>
				</Group>
				{refused ? (
					<GroupFooter tone="danger">Couldn't save this hub. Check the name and the link, and try again.</GroupFooter>
				) : null}
			</GroupedPage>
		);
	if (how === "scan") return <Scan onPairing={startReview} onPasteInstead={() => setHow("paste")} />;
	if (how === "paste") return <Paste onPairing={startReview} />;
	return <Address onPairing={startReview} />;
}

function Scan({
	onPairing,
	onPasteInstead,
}: {
	onPairing(target: PairingTarget): void;
	onPasteInstead(): void;
}) {
	const [permission, requestPermission] = useCameraPermissions();
	const [notPairing, setNotPairing] = useState(false);
	const asked = useRef(false);
	const canAsk = permission !== null && !permission.granted && permission.canAskAgain;

	useEffect(() => {
		if (!canAsk || asked.current) return;
		asked.current = true;
		void requestPermission();
	}, [canAsk, requestPermission]);

	if (permission === null || canAsk) return <GroupedPage>{null}</GroupedPage>;
	if (!permission.granted)
		return (
			<GroupedPage>
				<GroupFooter>Camera access is off for Evener.</GroupFooter>
				<Group>
					<Row label="Open Settings" tone="accent" onPress={() => void Linking.openSettings()} />
					<Row label="Paste the link instead" tone="accent" onPress={onPasteInstead} />
				</Group>
			</GroupedPage>
		);
	return (
		<GroupedPage>
			<View style={{ marginHorizontal: 16, marginTop: 16, aspectRatio: 1, borderRadius: 12, overflow: "hidden" }}>
				<CameraView
					style={{ flex: 1 }}
					facing="back"
					barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
					onBarcodeScanned={({ data }) => {
						const target = pairingFrom(data);
						if (target) onPairing(target);
						else setNotPairing(true);
					}}
				/>
			</View>
			{notPairing ? (
				<GroupFooter tone="danger">That isn't an Evener pairing code.</GroupFooter>
			) : (
				<GroupFooter>Point the camera at the pairing code.</GroupFooter>
			)}
		</GroupedPage>
	);
}

function Paste({ onPairing }: { onPairing(target: PairingTarget): void }) {
	const [link, setLink] = useState("");
	const [notPairing, setNotPairing] = useState(false);

	function reviewLink(text: string) {
		const target = pairingFrom(text);
		if (target) onPairing(target);
		else setNotPairing(true);
	}

	async function paste() {
		let text = "";
		try {
			text = await getStringAsync();
		} catch {
			// An unreadable clipboard reads as an empty one: the refusal below
			// asks for the link to be copied again.
		}
		setLink(text);
		reviewLink(text);
	}

	return (
		<GroupedPage>
			<GroupLabel>Pairing link</GroupLabel>
			<Group>
				<Field
					label="Pairing link"
					value={link}
					secure
					returnKeyType="go"
					onChangeText={(text) => {
						setLink(text);
						setNotPairing(false);
					}}
					onSubmitEditing={() => reviewLink(link)}
				/>
				<Row label="Paste" tone="accent" onPress={() => void paste()} />
			</Group>
			{notPairing ? (
				<GroupFooter tone="danger">
					That isn't an Evener pairing link. Copy it again from Settings, then Mobile app, in Evener on your
					computer.
				</GroupFooter>
			) : null}
		</GroupedPage>
	);
}

function Address({ onPairing }: { onPairing(target: PairingTarget): void }) {
	const [address, setAddress] = useState("");
	const [token, setToken] = useState("");
	return (
		<GroupedPage>
			<GroupLabel>Address</GroupLabel>
			<Group>
				<Field
					label="Address"
					value={address}
					keyboardType="url"
					placeholder="https://hub.example.com:9180"
					onChangeText={setAddress}
				/>
			</Group>
			<GroupLabel>Token (optional)</GroupLabel>
			<Group>
				<Field label="Token (optional)" value={token} secure onChangeText={setToken} />
			</Group>
			<GroupGap />
			<Group>
				<Row
					label="Continue"
					tone="accent"
					disabled={!address.trim()}
					onPress={() => onPairing({ origin: address.trim(), token })}
				/>
			</Group>
		</GroupedPage>
	);
}

/** A field row in a group: the label above the group names it, and VoiceOver
 * reads the same label. Addresses, links and tokens are typed as they are. */
function Field({
	label,
	secure = false,
	...input
}: {
	label: string;
	secure?: boolean;
} & Pick<
	TextInputProps,
	"value" | "onChangeText" | "onSubmitEditing" | "placeholder" | "keyboardType" | "editable" | "returnKeyType"
>) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<TextInput
			accessibilityLabel={label}
			allowFontScaling={allowFontScaling}
			autoCapitalize="none"
			autoCorrect={false}
			secureTextEntry={secure}
			placeholderTextColor={palette.inkLow}
			style={{ minHeight: 44, paddingHorizontal: 16, color: palette.inkHi, fontSize: 17 * scale }}
			{...input}
		/>
	);
}

/** Machine text on a quiet row: the hub's origin, in Menlo. */
function MachineRow({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ minHeight: 44, justifyContent: "center", paddingHorizontal: 16, paddingVertical: 11 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkMid, fontFamily: fonts.mono, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{text}
			</Text>
		</View>
	);
}

/** The space between an action's group and the fields above it. */
function GroupGap() {
	return <View style={{ height: 20 }} />;
}
