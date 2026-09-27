// A scripted fake HubPort for driving the tool layer without a hub: tests
// register responders per method, emit notifications by hand, and inspect the
// recorded calls. It implements the same seam the real adapter does, so these
// tests exercise the tool logic, not a mock of it.

import type { AnyNotification, ConnectionState, MethodName, MethodTypes } from "@evener/appwire-client";

import type { HubInfo, HubPort } from "../src/hub.js";

export class FakeHub implements HubPort {
  readonly calls: Array<{ method: string; params: unknown }> = [];
  private readonly responders = new Map<string, (params: unknown) => unknown>();
  private readonly notifCbs = new Set<(n: AnyNotification) => void>();
  private readonly readyCbs = new Set<() => void>();
  private state: ConnectionState = "ready";

  on(method: string, respond: (params: unknown) => unknown): void {
    this.responders.set(method, respond);
  }

  emit(notification: AnyNotification): void {
    for (const cb of this.notifCbs) cb(notification);
  }

  fireReady(): void {
    for (const cb of this.readyCbs) cb();
  }

  setState(state: ConnectionState): void {
    this.state = state;
  }

  request<M extends MethodName>(method: M, params: MethodTypes[M]["params"]): Promise<MethodTypes[M]["result"]> {
    this.calls.push({ method, params: JSON.parse(JSON.stringify(params)) });
    const respond = this.responders.get(method);
    if (!respond) return Promise.reject(new Error(`no fake responder registered for ${method}`));
    return Promise.resolve(respond(params) as MethodTypes[M]["result"]);
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    this.notifCbs.add(cb);
    return () => this.notifCbs.delete(cb);
  }

  onReady(cb: () => void): () => void {
    this.readyCbs.add(cb);
    return () => this.readyCbs.delete(cb);
  }

  connectionState(): ConnectionState {
    return this.state;
  }

  url(): string {
    return "ws://fake/rpc";
  }

  info(): HubInfo | undefined {
    return { name: "evener hub", version: "test", sourceId: "local" };
  }

  close(): void {
    this.setState("closed");
  }

  callsOf(method: string): Array<Record<string, unknown>> {
    return this.calls.filter((c) => c.method === method).map((c) => c.params as Record<string, unknown>);
  }
}
