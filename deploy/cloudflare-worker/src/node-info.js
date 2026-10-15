const encoder = new TextEncoder();
const sources = new Set(["third_party", "community", "static"]);
const cityStates = new Set(["verified", "reported", "unknown", "conflict"]);

export function normalizeNodeInfo(value, withProof = false) {
  if (!value || typeof value !== "object" || Array.isArray(value) || !sources.has(value.source)) return null;
  const city = String(value.city || "").trim();
  if (city.length > 48 || !/^[\p{L}\p{M} .·'-]*$/u.test(city) || !cityStates.has(value.city_status)) return null;
  const info = { city, city_status: value.city_status, source: value.source };
  if (value.name !== undefined) {
    if (typeof value.name !== 'string' || value.name.length > 80 ||
        !/^[\p{L}\p{M} .·'-]+[1-9][0-9]*号$/u.test(value.name)) return null;
    info.name = value.name;
  }
  if (withProof) {
    if (!/^[a-f0-9]{64}$/.test(String(value.proof || ""))) return null;
    info.proof = value.proof;
  }
  return info;
}

async function signingKey(env) {
  const secret = String(env.NODE_CORE_SECRET || env.RATE_LIMIT_SALT || "");
  if (secret.length < 16) return null;
  return crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]);
}

function signedData(session, region, family, carrier, id, info) {
  return encoder.encode(JSON.stringify([info.name ? "sq-node-info-v2" : "sq-node-info-v1", session, region, family, carrier, id,
    info.city, info.city_status, info.source, ...(info.name ? [info.name] : [])]));
}

// Only clients that request this capability receive new lease fields. Old
// probes intentionally reject unknown JSON fields, so absence must be preserved.
export async function prepareNodeInfo(lease, request, env, session) {
  const capability = request.headers.get("x-sq-node-info");
  const requested = ["1", "2"].includes(capability);
  const key = requested ? await signingKey(env) : null;
  for (const target of lease.targets) {
    for (const candidate of target.candidates) {
      const info = normalizeNodeInfo(candidate.node_info);
      delete candidate.node_info;
      if (!info || !key) continue;
      if (capability !== "2") delete info.name;
      const signature = await crypto.subtle.sign("HMAC", key,
        signedData(session, lease.region.code, lease.family, target.carrier, candidate.id, info));
      candidate.node_info = { ...info, proof: [...new Uint8Array(signature)].map((b) => b.toString(16).padStart(2, "0")).join("") };
    }
  }
  return lease;
}

export async function verifyReportNodeInfo(reports, env, session) {
  const key = await signingKey(env);
  const sessions = Array.isArray(session) ? session : [session];
  for (const report of reports) {
    for (const result of report.results) {
      const info = normalizeNodeInfo(result.node_info, true);
      delete result.node_info;
      if (!key || !info || !result.node_used) continue;
      const proof = Uint8Array.from(info.proof.match(/../g), (byte) => parseInt(byte, 16));
      let verified = false;
      for (const hash of sessions) {
        if (await crypto.subtle.verify("HMAC", key, proof,
          signedData(hash, report.region.code, report.family, result.carrier, result.node_id, info))) { verified = true; break; }
      }
      if (!verified) continue;
      delete info.proof;
      result.node_info = info;
    }
  }
}

export function nodeDescription(result, short = false) {
  // Failed preparation can contain a candidate ID for health accounting. It
  // does not mean that candidate was actually activated or measured.
  if (!/^[a-f0-9]{32}$/.test(result.node_id || "") ||
      !(result.node_used === true || (result.node_used == null && result.single))) return "";
  const info = normalizeNodeInfo(result.node_info);
  if (!info) return "";
  const city = info.city_status === "conflict" ? "城市待核实" : info.city;
  const source = { third_party: "第三方", community: "自建", static: "静态节点" }[info.source];
  if (info.name) return [info.name, ...(short ? [] : [source])].join(" · ");
  return [city, result.node_id.slice(0, 12), ...(short ? [] : [source])].filter(Boolean).join(" · ");
}

// Enrich historical reports only through exact, authenticated registry matches.
// No lookup is ever made using a shortened ID, client IP or guessed city.
export async function enrichNodeNames(reports, env) {
  if (!env.NODE_CORE?.fetch) return;
  const entries = reports.flatMap(report => (report.results || []).filter(result =>
    !result.node_info?.name && /^[a-f0-9]{32}$/.test(result.node_id || '') &&
    (result.node_used === true || (result.node_used == null && result.single))).map(result => ({
      id: result.node_id, family: report.family, region: report.region?.code, carrier: result.carrier,
    }))).slice(0, 120);
  if (!entries.length) return;
  try {
    const response = await env.NODE_CORE.fetch(new Request('https://node-core.internal/node-names', {
      method: 'POST', headers: { 'content-type': 'application/json', 'x-node-core-secret': String(env.NODE_CORE_SECRET || '') },
      body: JSON.stringify({ nodes: entries }), signal: AbortSignal.timeout(1500),
    }));
    if (!response.ok) return;
    const value = await response.json();
    if (!Array.isArray(value.names) || value.names.length > 120) return;
    const key = entry => [entry.id, entry.family, entry.region, entry.carrier].join('|');
    const names = new Map(value.names.map(entry => [key(entry), entry]));
    for (const report of reports) for (const result of report.results || []) {
      if (result.node_info?.name || !(result.node_used === true || (result.node_used == null && result.single))) continue;
      const found = names.get(key({ id: result.node_id, family: report.family, region: report.region?.code, carrier: result.carrier }));
      if (!found || (result.node_info?.source && result.node_info.source !== found.source)) continue;
      const info = normalizeNodeInfo({ city: found.city, city_status: 'reported', source: found.source,
        ...result.node_info, name: found.name });
      if (info) result.node_info = info;
    }
  } catch { /* Report reading remains available if Core is being upgraded. */ }
}
