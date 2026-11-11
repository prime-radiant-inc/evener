import { isHistoryReadMethod } from "./historyMethods.mjs";

export function createBackgroundJobsObserver(frames = []) {
  const requests = new Map();
  const methods = new Set(["evener/thread/jobs/list", "evener/thread/activity/read", "evener/jobs/output", "evener/jobs/get"]);
  const observe = async (event) => {
    const value = JSON.parse(event.data);
    if (!["Network.webSocketFrameSent", "Network.webSocketFrameReceived"].includes(value.method)) return;
    let message;
    try { message = JSON.parse(value.params.response.payloadData); } catch { return; }
    const key = JSON.stringify([value.params.requestId, message.id]);
    if (value.method === "Network.webSocketFrameSent") {
      if (!isHistoryReadMethod(message.method) && !methods.has(message.method)) return;
      requests.set(key, message.method);
    } else if (!requests.has(key)) return;
    // Subscription acknowledgements can include unrelated session configuration.
    if (isHistoryReadMethod(requests.get(key)) && Object.hasOwn(message, "result")) message.result = { subscribed: true };
    frames.push({ direction: value.method, socketId: value.params.requestId, ...message });
  };
  return { observe, frames, requests };
}
