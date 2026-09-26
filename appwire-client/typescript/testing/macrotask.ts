// nextMacrotask yields to the task queue exactly once. A task callback runs
// only after the microtask checkpoint has drained completely, including
// microtasks queued by other microtasks, so once it resolves every promise
// chain that waits on no timer or I/O has settled, however many turns it takes.
// That makes it the wait for work a store starts on its own (a refresh kicked
// by a connection change, a notification's follow-up read) when the test holds
// no promise for it. It does not wait for a timer, an IndexedDB request or
// other I/O: a test waiting on one of those needs its own awaited condition.
// It also never resolves under fake timers that fake setTimeout. In-repo test
// support, not shipped.
export function nextMacrotask(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}
