// Drives the real AppwireClient at its socket boundary. Responses stay held
// until a test supplies a literal page or error for the observed request.
import { AppwireClient } from "../client";
import { FakeSocket } from "./fakeSocket";

export interface JobOutputRequest {
  id: number;
  method: string;
  params: Record<string, unknown>;
}

export class JobOutputPeer extends FakeSocket {
  private readonly waiters = new Set<() => void>();

  constructor() {
    super({ autoInitialize: true });
  }

  override send(data: string): void {
    super.send(data);
    for (const notify of [...this.waiters]) notify();
  }

  requests(method: string): JobOutputRequest[] {
    return this.sent.map((raw) => JSON.parse(raw) as JobOutputRequest).filter((request) => request.method === method);
  }

  request(method: string, index = 0): Promise<JobOutputRequest> {
    return new Promise((resolve) => {
      const check = () => {
        const request = this.requests(method)[index];
        if (!request) return;
        this.waiters.delete(check);
        resolve(request);
      };
      this.waiters.add(check);
      check();
    });
  }

  reply(request: JobOutputRequest, data: unknown): void {
    this.receive({ jsonrpc: "2.0", id: request.id, result: { data } });
  }

  fail(request: JobOutputRequest, message: string, data?: unknown, code = -32014): void {
    this.receive({ jsonrpc: "2.0", id: request.id, error: { code, message, data } });
  }
}

export async function connectJobOutputPeer(): Promise<{ client: AppwireClient; peer: JobOutputPeer }> {
  const peer = new JobOutputPeer();
  const client = new AppwireClient({ url: "ws://job-output.test/rpc", socketFactory: () => peer });
  const connecting = client.connect();
  peer.open();
  await connecting;
  return { client, peer };
}
