const encoder = new TextEncoder();
const sources = new Set(["third_party", "community", "static"]);
const cityStates = new Set(["verified", "reported", "unknown", "conflict"]);

export function normalizeNodeInfo(value, withProof = false) {
  if (!value || typeof value !== "object" || Array.isArray(value) || !sources.has(value.source)) return null;
  const city = String(value.city || "").trim();
  if (city.length > 48 || !/^[\p{L}\p{M} .·'-]*$/u.test(city) || !cityStates.has(value.city_status)) return null;
  const info = { city, city_status: value.city_status, source: value.source };
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
  return encoder.encode(JSON.stringify(["sq-node-info-v1", session, region, family, carrier, id,
    info.city, info.city_status, info.source]));
}

// Only clients that request this capability receive new lease fields. Old
// probes intentionally reject unknown JSON fields, so absence must be preserved.
export async function prepareNodeInfo(lease, request, env, session) {
  const requested = request.headers.get("x-sq-node-info") === "1";
  const key = requested ? await signingKey(env) : null;
  for (const target of lease.targets) {
    for (const candidate of target.candidates) {
      const info = normalizeNodeInfo(candidate.node_info);
      delete candidate.node_info;
      if (!info || !key) continue;
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
  const city = info?.city_status === "conflict" ? "城市待核实" : info?.city || "城市未知";
  const state = info?.city_status === "reported" && info.city ? "（来源标注）" : "";
  const source = { third_party: "第三方", community: "自建", static: "静态节点" }[info?.source] || "来源未知";
  return short ? `${city} · ${result.node_id.slice(0, 12)}`
    : `${city}${state} · ${result.node_id.slice(0, 12)} · ${source}`;
}
