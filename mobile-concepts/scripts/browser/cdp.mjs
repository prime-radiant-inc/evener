export async function connectCdp(webSocketUrl) {
  const socket = new WebSocket(webSocketUrl);
  const pending = new Map();
  const waiters = new Map();
  const listeners = new Map();
  let nextId = 1;
  let closed;
  const closedPromise = new Promise((resolve) => {
    closed = resolve;
  });

  await new Promise((resolve, reject) => {
    socket.addEventListener("open", resolve, { once: true });
    socket.addEventListener(
      "error",
      () => reject(new Error(`CDP WebSocket failed: ${webSocketUrl}`)),
      { once: true },
    );
  });

  socket.addEventListener("message", (event) => {
    const message = JSON.parse(String(event.data));
    if (message.id) {
      const request = pending.get(message.id);
      if (!request) return;
      pending.delete(message.id);
      if (message.error)
        request.reject(
          new Error(
            `${request.method}: ${message.error.message} (${message.error.code})`,
          ),
        );
      else request.resolve(message.result);
      return;
    }
    const queue = waiters.get(message.method);
    if (queue?.length) queue.shift()(message.params ?? {});
    for (const listener of listeners.get(message.method) ?? [])
      listener(message.params ?? {});
  });
  socket.addEventListener(
    "close",
    () => {
      const error = new Error("CDP WebSocket closed");
      for (const request of pending.values()) request.reject(error);
      pending.clear();
      closed();
    },
    { once: true },
  );

  return {
    send(method, params = {}) {
      if (socket.readyState !== WebSocket.OPEN)
        return Promise.reject(new Error(`CDP is not open for ${method}`));
      const id = nextId++;
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject, method });
        socket.send(JSON.stringify({ id, method, params }));
      });
    },
    once(method) {
      return new Promise((resolve) => {
        const queue = waiters.get(method) ?? [];
        queue.push(resolve);
        waiters.set(method, queue);
      });
    },
    on(method, listener) {
      const set = listeners.get(method) ?? new Set();
      set.add(listener);
      listeners.set(method, set);
      return () => set.delete(listener);
    },
    async close() {
      if (socket.readyState === WebSocket.CLOSED) return;
      socket.close();
      await closedPromise;
    },
  };
}

export async function navigate(client, url) {
  const loaded = client.once("Page.loadEventFired");
  const result = await client.send("Page.navigate", { url });
  if (result.errorText)
    throw new Error(`Navigation failed: ${result.errorText}`);
  await loaded;
}

export async function evaluate(
  client,
  expression,
  { awaitPromise = true, returnByValue = true } = {},
) {
  const result = await client.send("Runtime.evaluate", {
    expression,
    awaitPromise,
    returnByValue,
  });
  if (result.exceptionDetails)
    throw new Error(
      result.exceptionDetails.exception?.description ??
        result.exceptionDetails.text,
    );
  return result.result?.value;
}
