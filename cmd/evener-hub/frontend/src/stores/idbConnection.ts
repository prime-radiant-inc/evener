type OpenDiagnosticPath = "open-timeout" | "open-blocked" | "upgrade-abandoned" | "versionchange-retire";

interface IDBConnectionOptions {
  indexedDB: IDBFactory;
  databaseName: string;
  databaseVersion: number;
  waitMs: number;
  upgrade: (database: IDBDatabase) => void;
  // The cache sweeps expired rows before installing a connection. The outbox
  // installs immediately; neither adapter closes a healthy connection per call.
  prepare?: (database: IDBDatabase) => Promise<void>;
  // Each adapter brands its own failures, so a caller can tell the storage
  // failing from anything else it might throw.
  errors: {
    open: () => Error;
    superseded: () => Error;
    timeout: () => Error;
    // The outbox rejects blocked opens immediately. The cache only reports
    // them and keeps waiting for success or its open watchdog.
    blocked?: () => Error;
  };
  reportDiagnostic: (path: OpenDiagnosticPath, versionchangeTransaction: boolean) => void;
  reportOpenError?: (error: Error) => void;
}

export function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("IndexedDB request failed")), {
      once: true,
    });
  });
}

// Outbox completion rejects on transaction error; cache completion waits for
// abort. The adapters' transaction runners still handle body/request failures.
export function transactionCompletion(
  transaction: IDBTransaction,
  failureEvent: "error" | "abort" = "error",
): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener(
      "abort",
      () => reject(transaction.error ?? new Error("IndexedDB transaction aborted")),
      { once: true },
    );
    transaction.addEventListener(
      "error",
      () => {
        if (failureEvent === "error") reject(transaction.error ?? new Error("IndexedDB transaction failed"));
      },
      { once: true },
    );
  });
}

// False means abort could not establish rollback (already committing or
// finished). The outbox then keeps a timed-out write pending for its outcome.
export function tryAbortTransaction(transaction: IDBTransaction): boolean {
  try {
    transaction.abort();
    return true;
  } catch {
    return false;
  }
}

// An adapter's own schema upgrade threw: a bug in the adapter, never storage
// failing. The engine answers a throwing upgradeneeded handler by aborting the
// versionchange transaction, and the open then fails with a bare AbortError
// that reads exactly like storage aborting it; this names the real cause.
export class IDBUpgradeError extends Error {
  constructor(cause: unknown) {
    super("IndexedDB schema upgrade failed", { cause });
    this.name = "IDBUpgradeError";
  }
}

export class IDBConnection {
  readonly #options: IDBConnectionOptions;
  #database: IDBDatabase | undefined;
  #databasePromise: Promise<IDBDatabase> | undefined;

  constructor(options: IDBConnectionOptions) {
    this.#options = options;
  }

  isOpen(): boolean {
    return this.#database !== undefined;
  }

  close(): void {
    // Forget the connection and any pending attempt. An in-flight open keeps
    // running, commits its upgrade, and closes its superseded late connection.
    this.#database?.close();
    this.#database = undefined;
    this.#databasePromise = undefined;
  }

  retire(database: IDBDatabase): void {
    database.close();
    if (this.#database !== database) return;
    this.#database = undefined;
    this.#databasePromise = undefined;
  }

  async open(): Promise<IDBDatabase> {
    // One attempt per call, shared by concurrent callers. A failed open is
    // forgotten so a later call can recover without retrying this operation.
    if (this.#database) return this.#database;
    if (this.#databasePromise) return this.#databasePromise;
    const options = this.#options;
    const opening = new Promise<IDBDatabase>((resolve, reject) => {
      const request = options.indexedDB.open(options.databaseName, options.databaseVersion);
      let abandoned = false;
      let upgradeFailure: IDBUpgradeError | undefined;
      const fail = (error: unknown) => {
        abandoned = true;
        clearTimeout(timer);
        reject(error);
      };
      const timer = setTimeout(() => {
        options.reportDiagnostic("open-timeout", Boolean(request.transaction));
        fail(options.errors.timeout());
      }, options.waitMs);
      request.addEventListener(
        "upgradeneeded",
        () => {
          // Always let the schema upgrade commit, even for an abandoned or
          // superseded attempt. Aborting versionchange transactions can wedge
          // Chromium's connection coordinator (crbug 40278488). Identity only
          // decides whether the later success installs its connection.
          if (abandoned || this.#databasePromise !== opening) {
            options.reportDiagnostic("upgrade-abandoned", Boolean(request.transaction));
          }
          try {
            options.upgrade(request.result);
          } catch (error) {
            // Rethrow so the engine aborts the half-built schema.
            upgradeFailure = new IDBUpgradeError(error);
            throw error;
          }
        },
        { once: true },
      );
      request.addEventListener(
        "success",
        () => {
          const database = request.result;
          const superseded = () => {
            if (!abandoned && this.#databasePromise === opening) return false;
            clearTimeout(timer);
            database.close();
            reject(options.errors.superseded());
            return true;
          };
          if (superseded()) return;
          database.addEventListener("versionchange", () => {
            // The upgrade belongs to the other connection's request, not ours.
            options.reportDiagnostic("versionchange-retire", false);
            this.retire(database);
          });
          database.addEventListener("close", () => this.retire(database));
          if (options.prepare) {
            // Keep the open watchdog live through the cache's initial sweep.
            // A close or timeout during it must still reject the late result.
            void options.prepare(database).then(
              () => {
                clearTimeout(timer);
                if (superseded()) return;
                this.#database = database;
                this.#databasePromise = undefined;
                resolve(database);
              },
              (error: unknown) => {
                clearTimeout(timer);
                database.close();
                reject(error);
              },
            );
          } else {
            clearTimeout(timer);
            this.#database = database;
            resolve(database);
          }
        },
        { once: true },
      );
      request.addEventListener(
        "error",
        () => {
          if (abandoned) return;
          const error = upgradeFailure ?? request.error ?? options.errors.open();
          options.reportOpenError?.(error);
          fail(error);
        },
        { once: true },
      );
      request.addEventListener(
        "blocked",
        () => {
          options.reportDiagnostic("open-blocked", Boolean(request.transaction));
          if (options.errors.blocked !== undefined) fail(options.errors.blocked());
        },
        { once: true },
      );
    });
    this.#databasePromise = opening;
    try {
      return await opening;
    } catch (error) {
      // A newer attempt must survive this attempt's late failure.
      if (this.#databasePromise === opening) this.#databasePromise = undefined;
      throw error;
    }
  }
}
