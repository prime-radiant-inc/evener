// Temporary re-export seam for SDK migration row A3: the AppWire TypeScript
// package now lives at appwire-client/typescript/, and the web tree's imports
// still spell it `protocol/<module>`. SDK A4 rewrites those imports to
// `@evener/appwire-client` and deletes this whole directory. Do not add
// anything here that is not a re-export, and do not import from here in new
// code.
export * from "../../../../../appwire-client/typescript/errors";
