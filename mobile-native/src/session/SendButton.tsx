// The composer's one Send (spec 8.5): a paper airplane on the accent fill, in
// the slot's shared 44pt button, labeled for VoiceOver with what pressing it
// does. The composer and the Review sheet both send with it.
import { FillIconButton } from "./FillIconButton";

export function SendButton({ label, disabled, onPress }: { label: string; disabled: boolean; onPress(): void }) {
	return (
		<FillIconButton
			label={label}
			disabled={disabled}
			onPress={onPress}
			symbol="paperplane.fill"
			size={17}
			fill="accentFill"
		/>
	);
}
