import { ScrollView, View } from "react-native";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export interface ForkEditorProps {
	title: string;
	hubName: string;
	preview: string;
	connected: boolean;
	canCreate: boolean;
	pending: boolean;
	hasCheckpoint: boolean;
	hasChild: boolean;
	storageUnavailable: boolean;
	error: string | null;
	sourceError: string | null;
	create: () => void;
	openChild: () => void;
	retry: () => void;
	browseSessions: () => void;
	allowAnother: () => void;
	close: () => void;
}

export function ForkEditor(props: ForkEditorProps) {
	const colors = useColors();
	const busy = props.pending || props.storageUnavailable;
	const preview =
		props.preview.length > 1000
			? `${props.preview.slice(0, 1000)}…`
			: props.preview;
	return (
		<ScrollView
			style={styles.fill}
			contentContainerStyle={{ padding: 20, gap: 16 }}
		>
			<View style={{ gap: 4 }}>
				<Copy>{props.title}</Copy>
				<Copy muted>{props.hubName}</Copy>
			</View>
			<ErrorMessage message={props.sourceError} />
			<ErrorMessage message={props.error} />
			{!props.connected ? (
				<Action onPress={props.retry}>Reconnect</Action>
			) : null}
			<View style={{ gap: 8 }}>
				<Copy>
					History before this message will be copied into a new session.
				</Copy>
				<Copy muted>
					The message opens as an editable draft. You can change it before
					sending.
				</Copy>
				<View
					style={{
						padding: 14,
						borderRadius: 16,
						backgroundColor: colors.surface,
					}}
				>
					<Copy>{preview || "No text in the selected message."}</Copy>
				</View>
			</View>
			{props.pending ? <Copy muted>Preparing the fork…</Copy> : null}
			{props.hasCheckpoint ? (
				<View style={{ gap: 8 }}>
					{!props.pending ? (
						<Copy>
							{props.hasChild
								? "This fork was created."
								: "A fork may already exist. Check Sessions before creating another one."}
						</Copy>
					) : null}
					{props.hasChild ? (
						<Action
							disabled={!props.connected || busy}
							onPress={props.openChild}
							tone="primary"
						>
							Open fork
						</Action>
					) : (
						<View style={[styles.row, { flexWrap: "wrap" }]}>
							<Action onPress={props.browseSessions}>Check Sessions</Action>
							<Action
								disabled={!props.connected || busy}
								onPress={props.allowAnother}
								tone="quiet"
							>
								Allow another fork
							</Action>
						</View>
					)}
				</View>
			) : (
				<Action
					disabled={!props.connected || !props.canCreate || busy}
					onPress={props.create}
					tone="primary"
				>
					Create fork
				</Action>
			)}
			{props.storageUnavailable || (props.sourceError && props.connected) ? (
				<Action disabled={props.pending} onPress={props.retry}>
					Retry
				</Action>
			) : null}
			<Action onPress={props.close} tone="quiet">
				Close
			</Action>
		</ScrollView>
	);
}
