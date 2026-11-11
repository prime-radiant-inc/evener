export function isHistoryReadMethod(method) {
  return method === "thread/read" || method === "evener/thread/history/read";
}

export function isHistoryPageMethod(method) {
  return method === "thread/turns/list" || method === "evener/thread/history/turns/list";
}
