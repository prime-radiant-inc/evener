// React Native's Modal, holding in-app alerts while it is up (ruling 7): a
// banner can't show above it. A Modal is visible unless told otherwise (its
// defaultProps, react-native Libraries/Modal/Modal.js), so an unset `visible`
// holds, as the Modal shows.
import { Modal, type ModalProps } from "react-native";
import { useHoldAlerts } from "./alertsContext";

export function HoldingModal(props: ModalProps) {
	useHoldAlerts(props.visible ?? true, "covered");
	return <Modal {...props} />;
}
