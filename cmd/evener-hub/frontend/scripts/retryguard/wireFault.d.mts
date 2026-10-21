export interface RetryWireState {
  mode: string;
  armed: boolean;
  requests: { method: string; params: { input: unknown; clientMutationId: string } }[];
  dropped: number;
  connections: number;
}
export function installRetryWireFault(): RetryWireState;
