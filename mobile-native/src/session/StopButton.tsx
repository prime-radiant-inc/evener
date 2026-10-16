// The composer's slot while the agent works and the field is empty (spec
// 8.5): a stop square on the danger fill, in the slot's shared 44pt button,
// labeled for VoiceOver. It mirrors the one Send it stands in for, so the slot
// reads as the same control changing its mind.
import { FillIconButton } from "./FillIconButton";

export function StopButton({ disabled, onPress }: { disabled: boolean; onPress(): void }) {
	return (
		<FillIconButton label="Stop" disabled={disabled} onPress={onPress} symbol="stop.fill" size={15} fill="danger" />
	);
}
