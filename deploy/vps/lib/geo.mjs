import { isIP } from "node:net";
import { MemoryCache } from "./storage.mjs";

function publicAddress(address) {
  return isIP(address) && !/^(?:127\.|10\.|192\.168\.|169\.254\.|172\.(?:1[6-9]|2\d|3[01])\.|0\.|::1$|::$|f[cd][0-9a-f]{2}:|fe[89ab][0-9a-f]:)/i.test(address);
}

export function createGeoResolver({ url = "https://ipwho.is/{ip}", fetcher = fetch } = {}) {
  const cache = new MemoryCache({ maxEntries: 10000, maxBytes: 8 * 1024 * 1024 });
  const pending = new Map();
  return async (address) => {
    if (!publicAddress(address)) return {};
    const cached = await cache.get(address, "json");
    if (cached) return cached;
    if (pending.has(address)) return pending.get(address);
    const operation = (async () => {
      let result = {};
      let ttl = 60;
      try {
        const target = new URL(url.replaceAll("{ip}", encodeURIComponent(address)));
        if (target.protocol !== "https:") throw new Error("Geo lookup requires HTTPS");
        const response = await fetcher(target, { signal: AbortSignal.timeout(2500),
          headers: { accept: "application/json", "user-agent": "SpeedQuality-VPS/1.1" } });
        if (!response.ok) { await response.body?.cancel(); throw new Error("Geo lookup unavailable"); }
        const reader = response.body.getReader();
        const chunks = [];
        let size = 0;
        try {
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            size += value.length;
            if (size > 32 * 1024) throw new Error("Geo response too large");
            chunks.push(Buffer.from(value));
          }
        } finally { await reader.cancel().catch(() => {}); }
        const data = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        if (data.success === false) throw new Error("Geo lookup failed");
        const asn = Number(String(data.connection?.asn ?? data.asn ?? "").replace(/^AS/i, ""));
        result = {
          country: String(data.country_code || "").slice(0, 2).toUpperCase(),
          regionCode: String(data.region_code || "").slice(0, 16),
          region: String(data.region || "").slice(0, 120),
          asn: Number.isSafeInteger(asn) && asn > 0 ? asn : undefined,
          asOrganization: String(data.connection?.org || data.connection?.isp || "").slice(0, 120),
        };
        ttl = 6 * 3600;
      } catch { /* A missing location remains unknown; do not invent a province. */ }
      await cache.put(address, JSON.stringify(result), { expirationTtl: ttl });
      return result;
    })();
    pending.set(address, operation);
    try { return await operation; } finally { pending.delete(address); }
  };
}
