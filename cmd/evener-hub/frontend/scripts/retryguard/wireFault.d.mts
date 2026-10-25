export interface RetryWireState {
  mode: string;
  armed: boolean;
  requests: { method: string; params: { input: unknown; clientMutationId: string } }[];
  reads: { ref: string; subscribed: boolean; connection: number }[];
  dropped: number;
  connections: number;
  heldResponses: { method: string }[];
  heldNotifications: { method: string }[];
  responseHolds: string[];
  notificationHolds: string[];
  failNext: Record<string, number>;
  failed: number;
  methodCounts: Record<string, number>;
  holdResponses(...methods: string[]): void;
  holdNotifications(...methods: string[]): void;
  failNextResponse(method: string, count?: number): void;
  clearHolds(): void;
  releaseResponses(...methods: string[]): void;
  releaseNotifications(...methods: string[]): void;
  disconnect(): void;
}
export function installRetryWireFault(): RetryWireState;
