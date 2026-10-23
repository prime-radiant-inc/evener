export interface RetryWireState {
  mode: string;
  armed: boolean;
  requests: { method: string; params: { input: unknown; clientMutationId: string } }[];
  reads: { ref: string; subscribed: boolean; connection: number }[];
  dropped: number;
  connections: number;
  disconnect(): void;
}
export function installRetryWireFault(): RetryWireState;
