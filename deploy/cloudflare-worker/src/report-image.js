import { SHARE_FONT } from "./share-font.js";

export const PREVIEW_WIDTH = 1200;
export const PREVIEW_HEIGHT = 630;
const BACKGROUND = [12, 18, 25];
const COLORS = { white: [234, 242, 247], muted: [145, 163, 178], green: [158, 255, 110],
  orange: [255, 165, 0], red: [252, 95, 90], cyan: [200, 250, 244], line: [40, 54, 65] };
const palette = new Uint8Array(256 * 3);
palette.set(BACKGROUND);
const colorIndex = {};
for (const [i, [name, rgb]] of Object.entries(COLORS).entries()) {
  colorIndex[name] = i * 16;
  for (let alpha = 0; alpha < 16; alpha++) {
    palette.set(rgb.map((v, channel) => Math.round(BACKGROUND[channel] + (v - BACKGROUND[channel]) * alpha / 15)),
      (i * 16 + alpha) * 3);
  }
}

const decoded = new Map();
function glyph(size, character) {
  const key = `${size}:${character}`;
  if (decoded.has(key)) return decoded.get(key);
  const entry = SHARE_FONT[size][character] || SHARE_FONT[size]["?"];
  const [width, height, advance, left, top, encoded] = entry;
  const pixels = new Uint8Array(width * height);
  let offset = 0;
  for (const ch of atob(encoded)) {
    const value = ch.charCodeAt(0), count = (value >> 4) + 1;
    pixels.fill(value & 15, offset, offset + count); offset += count;
  }
  const result = { width, height, advance, left, top, pixels, tinted: new Map() };
  decoded.set(key, result);
  return result;
}

const crcTable = Uint32Array.from({ length: 256 }, (_, value) => {
  for (let i = 0; i < 8; i++) value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1;
  return value >>> 0;
});
function chunk(type, data) {
  const result = new Uint8Array(data.length + 12), view = new DataView(result.buffer);
  view.setUint32(0, data.length);
  result.set(new TextEncoder().encode(type), 4); result.set(data, 8);
  let crc = 0xffffffff;
  for (let i = 4; i < result.length - 4; i++) crc = crcTable[(crc ^ result[i]) & 255] ^ (crc >>> 8);
  view.setUint32(result.length - 4, (crc ^ 0xffffffff) >>> 0);
  return result;
}

// Native compression and a bundled alphabet keep this portable between Workers
// and Node without browser rendering, image services, or a runtime font fetch.
export async function renderPreviewPNG(model) {
  const width = PREVIEW_WIDTH, height = PREVIEW_HEIGHT, stride = width + 1;
  const raster = new Uint8Array(stride * height); // Indexed PNG scanlines, filter 0.
  const line = (y) => raster.fill(colorIndex.line + 15, y * stride + 65, y * stride + width - 63);
  const text = (value, x, baseline, size = 24, color = "white", align = "left") => {
    const characters = [...String(value)].slice(0, 160);
    if (align === "right") x -= characters.reduce((sum, ch) => sum + glyph(size, ch).advance, 0);
    const tint = colorIndex[color];
    for (const ch of characters) {
      const g = glyph(size, ch);
      if (!g.tinted.has(color)) g.tinted.set(color, g.pixels.map((alpha) => alpha ? tint + alpha : 0));
      const pixels = g.tinted.get(color);
      const left = Math.max(0, x + g.left), right = Math.min(width, x + g.left + g.width);
      const skip = left - x - g.left;
      if (right <= left) { x += g.advance; continue; }
      for (let gy = 0; gy < g.height; gy++) {
        const y = baseline + g.top + gy;
        if (y < 0 || y >= height) continue;
        const start = gy * g.width + skip;
        raster.set(pixels.subarray(start, start + right - left), y * stride + 1 + left);
      }
      x += g.advance;
    }
  };
  text(model.brand, 64, 64, 32, "green");
  text("测速报告", 1136, 64, 24, "muted", "right");
  text(model.heading, 64, 130, 40, "white");
  text(model.configuration, 64, 177, 24, "muted");
  line(203);
  if (model.rows.length) {
    text("运营商 / IP", 64, 244, 24, "cyan");
    text("延迟", 560, 244, 24, "cyan", "right");
    text("上传 Mbps", 846, 244, 24, "cyan", "right");
    text("下载 Mbps", 1136, 244, 24, "cyan", "right");
    const step = model.rows.length > 3 ? 41 : 65;
    model.rows.forEach((row, index) => {
      const y = 297 + index * step;
      text(row.label, 64, y, 24, "white");
      text(row.latency.text, 560, y, 24, row.latency.color, "right");
      text(row.upload.text, 846, y, 24, row.upload.color, "right");
      text(row.download.text, 1136, y, 24, row.download.color, "right");
    });
  } else {
    text("打开报告查看完整结果", 64, 345, 32, "cyan");
    text("本报告暂无测速数据", 64, 392, 24, "muted");
  }
  line(532);
  text(model.note, 64, 568, 20, model.warning ? "orange" : "muted");
  text(model.time, 64, 606, 20, "muted");
  text(model.id, 1136, 606, 20, "muted", "right");
  const ihdr = new Uint8Array(13), header = new DataView(ihdr.buffer);
  header.setUint32(0, width); header.setUint32(4, height); ihdr[8] = 8; ihdr[9] = 3;
  const compressed = new Uint8Array(await new Response(new Blob([raster]).stream()
    .pipeThrough(new CompressionStream("deflate"))).arrayBuffer());
  const parts = [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr),
    chunk("PLTE", palette), chunk("IDAT", compressed), chunk("IEND", new Uint8Array())];
  const png = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) { png.set(part, offset); offset += part.length; }
  return png;
}
