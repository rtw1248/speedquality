import { renderPreviewPNG } from "./report-image.js";

const stores = new WeakMap();
const MAX_CACHE_BYTES = 4 * 1024 * 1024;
const MAX_CACHE_ITEMS = 64;
function stateFor(database) {
  if (!stores.has(database)) stores.set(database, { images: new Map(), pending: new Map(), bytes: 0 });
  return stores.get(database);
}
function failure(status, message) {
  return new Response(message + "\n", { status, headers: { "content-type": "text/plain; charset=utf-8",
    "cache-control": "no-store", "x-content-type-options": "nosniff", ...(status === 503 ? { "retry-after": "5" } : {}) } });
}
function headersFor(expiresAt, now) {
  const ttl = Math.max(0, Math.min(3600, expiresAt - now));
  return { "content-type": "image/png", "cache-control": `public, max-age=${ttl}, s-maxage=${ttl}`,
    expires: new Date((now + ttl) * 1000).toUTCString(),
    "x-content-type-options": "nosniff", "x-robots-tag": "noindex, noarchive" };
}

export async function reportImageResponse(request, env, id, makeModel, context) {
  if (!env.DB) return failure(503, "Report storage is not configured");
  const now = Math.floor(Date.now() / 1000);
  const url = new URL(request.url); url.search = "";
  const key = url.href;
  const cache = env.PREVIEW_CACHE || globalThis.caches?.default;
  try {
    const cached = await cache?.match(new Request(key));
    const until = cached && Date.parse(cached.headers.get("expires")) / 1000;
    if (until > now) return new Response(request.method === "HEAD" ? null : cached.body,
      { headers: headersFor(until, now) });
  } catch { /* Preview cache outages do not prevent report access. */ }
  const state = stateFor(env.DB);
  const old = state.images.get(key);
  if (old && old.until > now) {
    state.images.delete(key); state.images.set(key, old);
    return new Response(request.method === "HEAD" ? null : old.png, { headers: headersFor(old.until, now) });
  }
  if (old) { state.images.delete(key); state.bytes -= old.png.length; }
  try {
    if (request.method === "HEAD") {
      const report = await env.DB.prepare("SELECT * FROM reports WHERE id = ? AND expires_at > ?").bind(id, now).first();
      return report ? new Response(null, { headers: headersFor(report.expires_at, now) }) : failure(404, "Not Found");
    }
    if (!state.pending.has(key)) {
      if (state.pending.size >= 4) return failure(503, "Preview generation busy");
      const work = (async () => {
        const report = await env.DB.prepare("SELECT * FROM reports WHERE id = ? AND expires_at > ?").bind(id, now).first();
        if (!report) return null;
        const png = await renderPreviewPNG(makeModel(report));
        const completedAt = Math.floor(Date.now() / 1000);
        const until = Math.min(report.expires_at, now + 3600);
        if (until <= completedAt) return null;
        const entry = { png, until };
        // Bounded per-process fallback also works on a VPS without Cloudflare Cache API.
        if (png.length <= MAX_CACHE_BYTES) {
          while (state.images.size && (state.images.size >= MAX_CACHE_ITEMS || state.bytes + png.length > MAX_CACHE_BYTES)) {
            const oldest = state.images.keys().next().value;
            state.bytes -= state.images.get(oldest).png.length; state.images.delete(oldest);
          }
          state.images.set(key, entry); state.bytes += png.length;
        }
        if (cache) {
          const save = Promise.resolve().then(() => cache.put(new Request(key), new Response(png, {
            headers: headersFor(until, completedAt),
          }))).catch(() => {});
          if (context?.waitUntil) context.waitUntil(save); else await save;
        }
        return entry;
      })();
      state.pending.set(key, work);
      work.finally(() => state.pending.delete(key)).catch(() => {});
    }
    const result = await state.pending.get(key);
    return result ? new Response(result.png, { headers: headersFor(result.until, Math.floor(Date.now() / 1000)) })
      : failure(404, "Not Found");
  } catch {
    return failure(503, "Preview temporarily unavailable");
  }
}
