const LEVELS = Object.freeze({ debug: 10, info: 20, warn: 30, error: 40, off: 100 });
const SENSITIVE_FIELD = /(?:token|authorization|route_key|api_key|client_ip|address|jwt|activation_key)/i;
const REQUEST_IDS = new WeakMap();

function configuredLevel(env) {
  const value = String(env?.LOG_LEVEL || "off").trim().toLowerCase();
  return Object.hasOwn(LEVELS, value) ? LEVELS[value] : LEVELS.info;
}

function redactText(value) {
  return String(value)
    .replace(/https?:\/\/[^\s"']+/gi, "[redacted-url]")
    .replace(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, "[redacted-ip]")
    .replace(/\b(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}\b/gi, "[redacted-ip]")
    .replace(/\b(?:sqn|sqa)_[A-Za-z0-9_-]{16,}\b/g, "[redacted-credential]")
    .replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, "[redacted-jwt]")
    .replace(/[\u0000-\u001f\u007f]/g, " ")
    .slice(0, 300);
}

function cleanFields(fields) {
  const output = {};
  for (const [key, raw] of Object.entries(fields || {})) {
    if (raw == null || SENSITIVE_FIELD.test(key)) continue;
    if (typeof raw === "string") {
      output[key] = redactText(raw);
    } else if (typeof raw === "number" || typeof raw === "boolean") {
      output[key] = raw;
    } else if (Array.isArray(raw)) {
      output[key] = raw.slice(0, 12).map((value) => redactText(value).slice(0, 80));
    }
  }
  return output;
}

export function logEvent(env, level, event, fields = {}) {
  const priority = LEVELS[level] ?? LEVELS.info;
  if (priority < configuredLevel(env)) return;
  const record = JSON.stringify({
    ts: new Date().toISOString(),
    level,
    event,
    ...cleanFields(fields),
  });
  if (level === "error") console.error(record);
  else if (level === "warn") console.warn(record);
  else console.log(record);
}

export function requestID(request) {
  const existing = REQUEST_IDS.get(request);
  if (existing) return existing;
  const internal = String(request.headers.get("x-speedquality-request-id") || "");
  if (/^[A-Za-z0-9._:-]{4,96}$/.test(internal)) {
    REQUEST_IDS.set(request, internal);
    return internal;
  }
  const ray = String(request.headers.get("cf-ray") || "").split("-", 1)[0];
  if (/^[a-f0-9]{8,32}$/i.test(ray)) {
    const value = `cf-${ray}`;
    REQUEST_IDS.set(request, value);
    return value;
  }
  const bytes = new Uint8Array(9);
  crypto.getRandomValues(bytes);
  const value = `edge-${[...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("")}`;
  REQUEST_IDS.set(request, value);
  return value;
}

export function routeLabel(pathname) {
  if (/^\/r\/[^/]+\/preview-v1\.png$/.test(pathname)) return "/r/:id/preview-v1.png";
  if (/^\/r\/[^/]+$/.test(pathname)) return "/r/:id";
  if (/^\/bin\/[^/]+\/[^/]+$/.test(pathname)) return "/bin/:version/:asset";
  return pathname.slice(0, 120);
}
