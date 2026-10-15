export const SQ_REPORT_LIMIT = 2;
const PLAN_LIFETIME = 6 * 60 * 60;
const ID_PATTERN = /^[A-Za-z0-9_-]{12}$/;
const encoder = new TextEncoder();

async function key(env) {
  if (!env.RATE_LIMIT_SALT) throw new Error("Report identity key is not configured");
  return crypto.subtle.importKey("raw", encoder.encode(env.RATE_LIMIT_SALT),
    { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]);
}

function hex(bytes) {
  return [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function reportIdentity(clientHash, env) {
  if (!env.RATE_LIMIT_SALT) return "";
  return hex(await crypto.subtle.sign("HMAC", await key(env), encoder.encode(`sq-report-identity-v1:${clientHash}`)));
}

function response(value, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: {
    "content-type": "application/json; charset=utf-8", "cache-control": "no-store",
    "x-content-type-options": "nosniff",
  } });
}

function failure(error, message, status, extra = {}) {
  return response({ error, message, sq_limit: SQ_REPORT_LIMIT, ...extra }, status);
}

export async function preflightReportLink(request, env, identity) {
  if (!env.DB || !identity) return failure("link_unavailable", "报告关联服务暂不可用", 503);
  if (!request.headers.get("content-type")?.startsWith("application/x-www-form-urlencoded")) {
    return failure("invalid_link", "SQ 报告链接格式无效", 400);
  }
  if (Number(request.headers.get("content-length")) > 4096) {
    return failure("invalid_link", "只能通过 --sq 关联一份历史 SQ 报告", 413);
  }
  const chunks = [];
  let size = 0;
  const reader = request.body?.getReader();
  if (reader) {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 4096) {
        await reader.cancel();
        return failure("invalid_link", "只能通过 --sq 关联一份历史 SQ 报告", 413);
      }
      chunks.push(value);
    }
  }
  const body = await new Blob(chunks).text();
  const params = new URLSearchParams(body);
  const links = params.getAll("sq_url");
  if (links.length !== 1) return failure("too_many_links", "--sq 只能指定一个链接，合计最多 2 份 SQ（含本次）", 400);
  let sourceID;
  try {
    const url = new URL(links[0]);
    const match = url.pathname.match(/^\/r\/([A-Za-z0-9_-]{12})\/?$/);
    if (url.origin !== new URL(request.url).origin || url.username || url.password || !match) throw new Error();
    sourceID = match[1];
  } catch {
    return failure("invalid_link", "请使用当前 SQ 平台的报告链接", 400);
  }
  const now = Math.floor(Date.now() / 1000);
  const report = await env.DB.prepare("SELECT * FROM reports WHERE id = ? AND expires_at > ?").bind(sourceID, now).first();
  if (!report) return failure("source_unavailable", "SQ 报告不存在或已过期", 404);
  if (report.sq_source_id) {
    return failure("sq_limit_exceeded", "该链接已包含 2 份 SQ，不能继续叠加；--sq 仅能关联一份历史报告", 409,
      { source_id: report.sq_source_id });
  }
  let identities;
  try { identities = JSON.parse(report.source_identity_hashes || "[]"); } catch { identities = []; }
  if (!Array.isArray(identities) || identities.length === 0) {
    return failure("source_identity_unavailable", "该旧报告缺少可核验的出口身份摘要，请先生成一份新版独立 SQ 报告", 422);
  }
  if (!identities.includes(identity)) {
    return failure("source_identity_mismatch", "SQ 报告与本次连接的出口身份不匹配", 403);
  }
  const plan = {
    v: 1, source: sourceID, tested_at: report.tested_at || null,
    subject: identity, expires: now + PLAN_LIFETIME,
  };
  const payload = btoa(JSON.stringify(plan)).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
  const signature = hex(await crypto.subtle.sign("HMAC", await key(env), encoder.encode(`sq-link-plan-v1:${payload}`)));
  return response({ version: 1, sq_limit: SQ_REPORT_LIMIT, sq_count: 2,
    token: `${payload}.${signature}`, nq_url: report.nq_url || "",
    message: "--sq 预检通过：一份历史 SQ 加本次测速，共 2 份；不支持继续叠加" });
}

export async function verifyReportLink(token, env, identities) {
  if (typeof token !== "string" || token.length > 2048) return null;
  const match = token.match(/^([A-Za-z0-9_-]+)\.([a-f0-9]{64})$/);
  if (!match || !env.RATE_LIMIT_SALT) return null;
  const signature = Uint8Array.from(match[2].match(/../g), (part) => parseInt(part, 16));
  if (!await crypto.subtle.verify("HMAC", await key(env), signature, encoder.encode(`sq-link-plan-v1:${match[1]}`))) return null;
  try {
    const plan = JSON.parse(atob(match[1].replaceAll("-", "+").replaceAll("_", "/")));
    const now = Math.floor(Date.now() / 1000);
    if (plan.v !== 1 || !ID_PATTERN.test(plan.source) || !identities.includes(plan.subject) ||
        !Number.isSafeInteger(plan.expires) || plan.expires <= now || plan.expires > now + PLAN_LIFETIME ||
        (plan.tested_at != null && (!Number.isSafeInteger(plan.tested_at) || plan.tested_at <= 0))) return null;
    return plan;
  } catch { return null; }
}

// Date is retained for display, but deliberately absent from the sort key.
export function sortSQReports(reports) {
  const seconds = (report) => Number.isSafeInteger(report.tested_at) && report.tested_at > 0
    ? (report.tested_at + 8 * 3600) % 86400 : 86400;
  return [...reports].sort((left, right) => seconds(left) - seconds(right));
}

export function linkedNQContext(root, measurement) {
  if (root === measurement || (root.id && root.id === measurement.id)) return { ...root };
  const value = { ...measurement, nq_url: root.nq_url, nq_tested_at: root.nq_tested_at,
    nq_identity_reason: root.nq_url ? "linked_sq" : "", nq_time_source: root.nq_time_source,
    bind_status: root.bind_status, time_gap_seconds: null };
  if (root.nq_url && root.nq_tested_at && measurement.tested_at) {
    value.time_gap_seconds = Math.abs(root.nq_tested_at - measurement.tested_at);
    value.bind_status = value.time_gap_seconds > 3600 ? "verified_stale" : "verified";
  } else if (root.nq_url) value.bind_status = "verified_time_unknown";
  return value;
}
