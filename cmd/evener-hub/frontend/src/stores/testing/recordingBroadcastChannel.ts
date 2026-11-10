// A BroadcastChannel double that records every instance and can deliver a
// message to its own listeners. Shared by the store tests that assert the
// production default factory, without creating Node's real channel.
export class RecordingBroadcastChannel extends EventTarget {
  static instances: RecordingBroadcastChannel[] = [];
  constructor(readonly name: string) {
    super();
    RecordingBroadcastChannel.instances.push(this);
  }
  postMessage(): void {}
  close(): void {}
  deliver(message: unknown): void {
    this.dispatchEvent(new MessageEvent("message", { data: message }));
  }
}
