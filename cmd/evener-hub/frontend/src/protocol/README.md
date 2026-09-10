# @evener/appwire-client

The framework independent TypeScript client for Evener's AppWire protocol. It
has no runtime dependencies and exports the existing client, transport
contract, generated protocol types, wire errors, and pure question formatter.

Build and qualify from this directory with `npm run qualification`. The runner
packs the package, installs that tarball into a temporary consumer, checks ESM
and CommonJS TypeScript resolution, runs both runtime import forms, and checks
the tarball does not contain source files or dependencies.

Applications own credentials, caches, transcript storage, subscriptions, and
mutation reconciliation. Connection loss during a mutation leaves its outcome
uncertain; callers must follow the protocol mutation identity rules before
retrying. The examples use Node 22's platform WebSocket and therefore require
Node 22 or newer. For a hub requiring an Authorization header, provide an
authenticated `socketFactory` from the host application's WebSocket
implementation.
