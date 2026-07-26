// SHA-256 helper for binding an overlay to its source bytes.
//
// crypto.subtle.digest is available in secure contexts (https + localhost). In
// a plain http:// deployment to a non-localhost host it is unavailable, so we
// degrade to a non-cryptographic 32-bit fingerprint (FNV-1a over the bytes).
// The doc model treats a mismatch as a SOFT warning (never a hard fail), so a
// weak hash at worst softens the warning — it never opens a security hole. The
// authoritative security property is the host-side capability gate, not this
// digest.

export async function pdfSha256(bytes: Uint8Array): Promise<string> {
  const subtle = (globalThis as { crypto?: { subtle?: DigestSubtle } }).crypto?.subtle;
  if (subtle && typeof subtle.digest === "function") {
    try {
      const digest = await subtle.digest("SHA-256", bytes.slice());
      return toHex(new Uint8Array(digest));
    } catch {
      // fall through to fingerprint
    }
  }
  return "fnv:" + fnv1aHex(bytes);
}

interface DigestSubtle {
  digest(algorithm: string, data: ArrayBuffer | Uint8Array): Promise<ArrayBuffer>;
}

const HEX = "0123456789abcdef";

function toHex(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i++) {
    out += HEX[bytes[i] >> 4] + HEX[bytes[i] & 0x0f];
  }
  return out;
}

function fnv1aHex(bytes: Uint8Array): string {
  // 32-bit FNV-1a — NOT cryptographic. Used only as a stable fingerprint when
  // crypto.subtle is unavailable, so the sha256 field still changes when the
  // bytes change.
  let h = 0x811c9dc5;
  for (let i = 0; i < bytes.length; i++) {
    h ^= bytes[i];
    h = Math.imul(h, 0x01000193);
  }
  // Force unsigned 32-bit and render as 8 hex chars.
  return (h >>> 0).toString(16).padStart(8, "0");
}
