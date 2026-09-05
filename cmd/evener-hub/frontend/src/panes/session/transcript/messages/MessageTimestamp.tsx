import { Timestamp } from "../../../../widgets/timestamp";
import { useSessionNow } from "../../liveness";

// Only this leaf consumes clock ticks, keeping message prose out of timer renders.
export function MessageTimestamp({ value }: { value: number }) {
  const now = useSessionNow();
  return <Timestamp value={value} now={now} />;
}
