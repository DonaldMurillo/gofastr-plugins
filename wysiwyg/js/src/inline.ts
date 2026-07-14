// Trusted in-page entry (assets/editor-inline.js). Exposes the mount API on a
// window global for host pages that OPTED OUT of the sandbox (DECISIONS.md
// "secure by default, opt out"): the editor runs with full page access, so
// this bundle must only ever be included by hosts that vouch for it
// (wysiwyg.WithTrustedMount on the Go side — never a default).
import { mountTrusted } from "./editor.ts";
import { SCHEMA_VERSION, PROTOCOL_VERSION } from "./schema.ts";

(window as unknown as Record<string, unknown>).__gofastrWysiwyg = {
  mountTrusted,
  schemaVersion: SCHEMA_VERSION,
  protocolVersion: PROTOCOL_VERSION,
};
