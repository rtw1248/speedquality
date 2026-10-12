import { normalizeNodeInfo } from "./node-info.js";

const regions = new Set("bj tj he sx nm ln jl hl sh js zj ah fj jx sd ha hb hn gd gx hi cq sc gz yn xz sn gs qh nx xj hk mo tw".split(" "));
const pendingReasons = new Set(["location_mismatch", "verification_pending", "verification_expired",
  "node_unavailable", "no_success", "recent_failure", "success_expired"]);

export function sanitizeCoverage(value) {
  if (value?.version !== 1 || !Array.isArray(value.nodes) || value.nodes.length > 4000 ||
      !Number.isSafeInteger(value.generated_at) || value.generated_at < 1) throw new Error("invalid_coverage");
  const nodes = [];
  for (const node of value.nodes) {
    const info = normalizeNodeInfo(node);
    if (!info || !/^[a-f0-9]{32}$/.test(node.id || "") || !regions.has(node.region) ||
        !["ct", "cu", "cm", "edu"].includes(node.carrier) || !["v4", "v6"].includes(node.family) ||
        !["verified", "pending"].includes(node.status) || !["third_party", "community"].includes(info.source) ||
        (node.carrier === "edu" && info.source !== "community")) continue;
    nodes.push({ id: node.id, region: node.region, carrier: node.carrier, family: node.family,
      ...info, status: node.status,
      ...(node.status === "pending" && pendingReasons.has(node.pending_reason) ? { pending_reason: node.pending_reason } : {}),
      last_success_at: Number.isSafeInteger(node.last_success_at) && node.last_success_at > 0 ? node.last_success_at : null,
      last_checked_at: Number.isSafeInteger(node.last_checked_at) && node.last_checked_at > 0 ? node.last_checked_at : null,
      public_testing: node.carrier !== "edu" });
  }
  return { version: 1, generated_at: value.generated_at, success_window_days: 7,
    education_public_testing: false, nodes };
}

export async function serveCoverage(request, env, context) {
  const headers = { "content-type": "application/json; charset=utf-8", "x-content-type-options": "nosniff",
    "cache-control": "public, max-age=60, s-maxage=300" };
  if (!["GET", "HEAD"].includes(request.method)) return new Response(null, { status: 405, headers: { allow: "GET, HEAD" } });
  const key = new Request(new URL("/api/nodes/coverage", request.url).href);
  const cache = globalThis.caches?.default;
  let response = await cache?.match(key);
  if (!response) {
    try {
      if (!env.NODE_CORE?.fetch) throw new Error("no_core");
      const upstream = await env.NODE_CORE.fetch(new Request("https://node-core.internal/coverage", {
        headers: { "x-node-core-secret": String(env.NODE_CORE_SECRET || "") },
        signal: AbortSignal.timeout(10000),
      }));
      if (!upstream.ok || !upstream.body) throw new Error("coverage_unavailable");
      const reader = upstream.body.getReader();
      const chunks = [];
      let length = 0;
      try {
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          length += value.byteLength;
          if (length > 2 * 1024 * 1024) { await reader.cancel(); throw new Error("oversized_coverage"); }
          chunks.push(value);
        }
      } finally { reader.releaseLock(); }
      const data = sanitizeCoverage(JSON.parse(await new Blob(chunks).text()));
      response = new Response(JSON.stringify(data), { headers });
      if (cache) {
        const put = cache.put(key, response.clone()).catch(() => {});
        if (context?.waitUntil) context.waitUntil(put); else await put;
      }
    } catch {
      return new Response(JSON.stringify({ error: "coverage_unavailable" }), {
        status: 503, headers: { ...headers, "cache-control": "no-store", "retry-after": "60" },
      });
    }
  }
  return new Response(request.method === "HEAD" ? null : response.body, response);
}
