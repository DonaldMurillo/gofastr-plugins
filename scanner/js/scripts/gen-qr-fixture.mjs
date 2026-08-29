// Generate the e2e QR fixture: encode with @zxing/library (pure JS, no wasm),
// write an 8-bit greyscale PNG with node's zlib. No image dependency, and the
// fixture is reproducible from this script.
//
// It REFUSES to write a fixture the decoder cannot read, which is not a
// theoretical guard: zxing's JS port fails to read some valid QR codes its own
// encoder produces. `GOFASTR_SCANNER_E2E` (19 bytes) is one of them — bisected
// against 17-, 18- and 20-byte payloads that all decode, and confirmed valid by
// the platform's BarcodeDetector, which reads it without complaint. A fixture
// like that would have looked like a scanner bug forever. See #19.
//
//   node scripts/gen-qr-fixture.mjs "GOFASTR-SCAN-OK" ../../e2e/fixtures/sample-qr.png
import { deflateSync, inflateSync } from "node:zlib";
import { writeFileSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
// The UMD build: node cannot resolve the ESM entry's directory imports.
const { MultiFormatWriter, BarcodeFormat, MultiFormatReader, BinaryBitmap, HybridBinarizer, RGBLuminanceSource, DecodeHintType } =
  require("@zxing/library/umd/index.min.js");

const TEXT = process.argv[2] ?? "GOFASTR-SCAN-OK";
const OUT = process.argv[3] ?? "../../e2e/fixtures/sample-qr.png";
const S = Number(process.argv[4] ?? 300);

const matrix = new MultiFormatWriter().encode(TEXT, BarcodeFormat.QR_CODE, S, S, new Map());

// One byte per pixel: 0 where a module is set, 255 elsewhere.
const gray = new Uint8ClampedArray(S * S);
for (let y = 0; y < S; y++) for (let x = 0; x < S; x++) gray[y * S + x] = matrix.get(x, y) ? 0 : 255;

// Round trip BEFORE writing. This is the whole point of the script.
const reader = new MultiFormatReader();
const hints = new Map();
hints.set(DecodeHintType.POSSIBLE_FORMATS, [BarcodeFormat.QR_CODE]);
reader.setHints(hints);
let decoded;
try {
  decoded = reader.decode(new BinaryBitmap(new HybridBinarizer(new RGBLuminanceSource(gray, S, S)))).getText();
} catch (err) {
  console.error(
    `refusing to write ${OUT}: the bundled decoder cannot read the code it just encoded for ${JSON.stringify(TEXT)}.\n` +
      `This is a known zxing defect on certain payloads, not a bug in this script — pick a different fixture string.\n` +
      `(${err && err.message ? err.message : err})`
  );
  process.exit(1);
}
if (decoded !== TEXT) {
  console.error(`refusing to write ${OUT}: round trip produced ${JSON.stringify(decoded)}, expected ${JSON.stringify(TEXT)}`);
  process.exit(1);
}

// --- PNG: 8-bit greyscale, one filter byte (none) per scanline ---------------
const raw = Buffer.alloc((S + 1) * S);
for (let y = 0; y < S; y++) {
  raw[y * (S + 1)] = 0;
  for (let x = 0; x < S; x++) raw[y * (S + 1) + 1 + x] = gray[y * S + x];
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};
const chunk = (type, data) => {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
};

const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(S, 0);
ihdr.writeUInt32BE(S, 4);
ihdr[8] = 8; // bit depth
ihdr[9] = 0; // colour type: greyscale
const idat = deflateSync(raw, { level: 9 });

// Cheap self-check of the writer: inflate what we are about to embed and
// compare. A silently mangled fixture would fail the e2e as "scanner cannot
// decode", which points at the wrong file.
if (!inflateSync(idat).equals(raw)) {
  console.error("refusing to write: the deflated scanlines do not inflate back to the source");
  process.exit(1);
}

const png = Buffer.concat([
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
  chunk("IHDR", ihdr),
  chunk("IDAT", idat),
  chunk("IEND", Buffer.alloc(0)),
]);
writeFileSync(OUT, png);
console.log(`${OUT}: ${png.length} bytes, ${S}x${S}, payload ${JSON.stringify(TEXT)} (round trip verified)`);
