// Adding a hub (spec 15): scan the pairing code Evener shows on a computer,
// paste its pairing link, or type an address and token by hand (ruling 23).
// Each way ends on one review step that names the hub and saves it. The token
// is held in state and handed to saveHub; it is never rendered as text.
import { CameraView, useCameraPermissions } from "expo-camera";
import { getStringAsync } from "expo-clipboard";
import { useEffect, useRef, useState } from "react";
import { AppState, Linking, Text, TextInput, type TextInputProps, View } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { fonts, space, uiType } from "../design/tokens";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type PairingTarget, pairingFrom, suggestedHubName } from "./pairing";

/** The ways to add a hub: scan its code, paste its link, or type its address. */
export type How = "scan" | "paste" | "address";

interface Review {
	target: PairingTarget;
	name: string;
}

export function AddHub({
	how: initialHow,
	onConnected,
	onHowChange,
}: {
	how: How;
	onConnected(): void;
	/** The way changed inside the page (scanning gave way to pasting). */
	onHowChange?(how: How): void;
}) {
	const { saveHub } = useConnection();
	const [how, setHow] = useState(initialHow);
	const [review, setReview] = useState<Review | null>(null);
	const [saving, setSaving] = useState(false);
	const [refused, setRefused] = useState(false);
	// Leaving mid-save (first run's Back) keeps the hub saved, but the page
	// that finishes must not act for a screen that's gone.
	const mounted = useRef(true);
	useEffect(
		() => () => {
			mounted.current = false;
		},
		[],
	);

	// The first pairing link wins: the camera keeps reporting codes until it
	// unmounts, and a later one must not replace the hub being reviewed.
	function startReview(target: PairingTarget) {
		setReview((current) => current ?? { target, name: suggestedHubName(target.origin) });
	}

	async function connect() {
		if (!review || saving || !review.name.trim()) return;
		setSaving(true);
		setRefused(false);
		try {
			// saveHub resolves false when a newer choice of hub superseded
			// selecting this one; the hub is saved either way.
			await saveHub({
				name: review.name.trim(),
				origin: review.target.origin,
				token: review.target.token,
			});
			if (mounted.current) onConnected();
		} catch {
			if (mounted.current) setRefused(true);
		} finally {
			if (mounted.current) setSaving(false);
		}
	}

	if (review)
		return (
			<GroupedPage>
				<Group label="Pair with">
					<MachineRow text={review.target.origin} />
				</Group>
				<Group label="Name">
					<Field
						label="Name"
						value={review.name}
						editable={!saving}
						onChangeText={(name) => setReview({ ...review, name })}
					/>
				</Group>
				<Group>
					<Row
						label={saving ? "Connecting…" : "Connect"}
						tone="accent"
						disabled={saving || !review.name.trim()}
						onPress={() => void connect()}
					/>
				</Group>
				{refused ? (
					<GroupFooter tone="danger">
						{`Couldn't save this hub. Check the name and the ${how === "address" ? "address" : "link"}, and try again.`}
					</GroupFooter>
				) : null}
			</GroupedPage>
		);
	if (how === "scan")
		return (
			<Scan
				onPairing={startReview}
				onPasteInstead={() => {
					setHow("paste");
					onHowChange?.("paste");
				}}
			/>
		);
	if (how === "paste") return <Paste onPairing={startReview} />;
	return <Address onPairing={startReview} />;
}

function Scan({ onPairing, onPasteInstead }: { onPairing(target: PairingTarget): void; onPasteInstead(): void }) {
	const [permission, requestPermission, getPermission] = useCameraPermissions();
	const [notPairing, setNotPairing] = useState(false);
	const asked = useRef(false);
	// Whether the one request this page makes has come back. Android can
	// leave canAskAgain true after a refusal, so that alone can't end the wait.
	const [answered, setAnswered] = useState(false);
	const mounted = useRef(true);
	useEffect(
		() => () => {
			mounted.current = false;
		},
		[],
	);
	const canAsk = permission !== null && !permission.granted && permission.canAskAgain;

	useEffect(() => {
		if (!canAsk || asked.current) return;
		asked.current = true;
		void requestPermission().finally(() => {
			if (mounted.current) setAnswered(true);
		});
	}, [canAsk, requestPermission]);

	// The hook reads the permission on mount and after a request only; a
	// grant made in Settings shows once the app comes back to the front.
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			if (state === "active") void getPermission();
		});
		return () => subscription.remove();
	}, [getPermission]);

	if (permission === null || (canAsk && !answered))
		return (
			<GroupedPage>
				<GroupFooter>Waiting for camera access…</GroupFooter>
			</GroupedPage>
		);
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
			<Group label="Pairing link">
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
					That isn't an Evener pairing link. Copy it again from Settings, then Mobile app, in Evener on your computer.
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
			<Group label="Address">
				<Field
					label="Address"
					value={address}
					keyboardType="url"
					placeholder="https://hub.example.com:9180"
					onChangeText={setAddress}
				/>
			</Group>
			<Group label="Token (optional)">
				<Field label="Token (optional)" value={token} secure onChangeText={setToken} />
			</Group>
			<Group>
				<Row
					label="Continue"
					tone="accent"
					disabled={!address.trim()}
					onPress={() => onPairing({ origin: address.trim(), token: token.trim() })}
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
			style={{
				minHeight: 44,
				paddingHorizontal: space.rowInset,
				color: palette.inkHi,
				fontSize: uiType.listRow.fontSize * scale,
			}}
			{...input}
		/>
	);
}

/** Machine text on a quiet row: the hub's origin, in Menlo. */
function MachineRow({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				minHeight: 44,
				justifyContent: "center",
				paddingHorizontal: space.rowInset,
				paddingVertical: space.rowPadding,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkMid, fontFamily: fonts.mono, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{text}
			</Text>
		</View>
	);
}
