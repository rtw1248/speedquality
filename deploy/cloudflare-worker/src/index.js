import { logEvent, requestID, routeLabel } from "./observability.js";

const SCRIPT_MARKER = "__SPEEDQUALITY_REPORT_BASE__";
const PROBE_VERSION_MARKER = "__SPEEDQUALITY_PROBE_VERSION__";
const NQ_BINDING_MARKER = "__SPEEDQUALITY_NQ_BINDING_ENABLED__";
const MAX_BODY_BYTES = 512 * 1024;
const MAX_SNAPSHOT_BYTES = 256 * 1024;
const MAX_SNAPSHOT_TEXT_BYTES = 192 * 1024;
const MAX_SNAPSHOT_FILE_BYTES = 96 * 1024;
const MAX_SNAPSHOT_FILES = 24;
const DEFAULT_TTL_DAYS = 90;
const DEFAULT_DAILY_LIMIT = 100;
const DEFAULT_DAILY_SESSION_LIMIT = 20;
const MAX_SPEED_DATA_BYTES = 192 * 1024;
const MAX_SESSION_REGIONS = 5;
const NODEQUALITY_TIME_GAP_SECONDS = 60 * 60;
const REPORT_ID_PATTERN = /^[A-Za-z0-9_-]{12}$/;
const SESSION_TOKEN_PATTERN = /^[A-Za-z0-9_-]{32,128}$/;
const NODE_ROUTE_PATTERN = /^sqn_[A-Za-z0-9_-]{24,96}$/;
const NODE_API_TOKEN_PATTERN = /^sqa_[A-Za-z0-9_-]{24,128}$/;
const COMMUNITY_NODE_ID_PATTERN = /^[a-f0-9]{32}$/;
const PUBLIC_LEASE_ERRORS = new Map([
  ["node_directory_unavailable", 502],
  ["node_capacity_exhausted", 503],
  ["node_verification_failed", 503],
  ["specified_node_unavailable", 503],
  ["capacity_store_unavailable", 503],
  ["community_node_service_unavailable", 503],
  ["lease_preparation_unavailable", 410],
]);
const REGION_CODES = new Set([
  "bj", "tj", "he", "sx", "nm", "ln", "jl", "hl", "sh", "js", "zj", "ah",
  "fj", "jx", "sd", "ha", "hb", "hn", "gd", "gx", "hi", "cq", "sc", "gz",
  "yn", "xz", "sn", "gs", "qh", "nx", "xj", "tw", "hk", "mo",
]);
const PUBLIC_REGION_CODES = new Set([...REGION_CODES].filter((code) =>
  !["hk", "mo", "tw"].includes(code)));
const REGION_NAMES = Object.freeze({
  bj: "北京", tj: "天津", he: "河北", sx: "山西", nm: "内蒙古", ln: "辽宁",
  jl: "吉林", hl: "黑龙江", sh: "上海", js: "江苏", zj: "浙江", ah: "安徽",
  fj: "福建", jx: "江西", sd: "山东", ha: "河南", hb: "湖北", hn: "湖南",
  gd: "广东", gx: "广西", hi: "海南", cq: "重庆", sc: "四川", gz: "贵州",
  yn: "云南", xz: "西藏", sn: "陕西", gs: "甘肃", qh: "青海", nx: "宁夏",
  xj: "新疆", tw: "台湾", hk: "香港", mo: "澳门",
});
const CARRIER_NAMES = Object.freeze({ ct: "电信", cu: "联通", cm: "移动" });
const CARRIER_CODES = new Set(Object.keys(CARRIER_NAMES));
const REGION_ALIASES = Object.freeze({
  beijing: "bj", tianjin: "tj", hebei: "he", shanxi: "sx", "inner mongolia": "nm",
  liaoning: "ln", jilin: "jl", heilongjiang: "hl", shanghai: "sh", jiangsu: "js",
  zhejiang: "zj", anhui: "ah", fujian: "fj", jiangxi: "jx", shandong: "sd",
  henan: "ha", hubei: "hb", hunan: "hn", guangdong: "gd", guangxi: "gx",
  hainan: "hi", chongqing: "cq", sichuan: "sc", guizhou: "gz", yunnan: "yn",
  tibet: "xz", shaanxi: "sn", gansu: "gs", qinghai: "qh", ningxia: "nx",
  xinjiang: "xj", taiwan: "tw", "hong kong": "hk", macao: "mo", macau: "mo",
});
const CARRIER_ASNS = Object.freeze({
  ct: new Set([4134, 4809]),
  cu: new Set([4837, 9929, 10099]),
  cm: new Set([9808, 58453]),
});
const MODES = new Set(["s"]);
const IP_MODES = new Set(["v4", "v6"]);
const TARGET_SPEEDS = new Set([100, 200, 400]);
const VERIFIED_STATUSES = new Set([
  "verified",
  "verified_stale",
  "verified_time_unknown",
]);
const REJECTED_BIND_STATUSES = new Set(["mismatch", "unverified"]);
const RESULT_FIELDS = [
  "tested_at",
  "regions",
  "mode",
  "ip_mode",
  "speed_url",
  "speed_text",
  "speed_data",
  "duration_seconds",
  "target_mbps",
  "traffic_rx_bytes",
  "traffic_tx_bytes",
  "bind_status",
  "version",
  "nq_url",
  "nq_tested_at",
  "nq_time_source",
  "time_gap_seconds",
  "nq_identity_reason",
];
const encoder = new TextEncoder();
const decoder = new TextDecoder();

function commonHeaders(extra = {}) {
  return {
    "x-content-type-options": "nosniff",
    "referrer-policy": "no-referrer",
    "x-frame-options": "DENY",
    ...extra,
  };
}

function reportContentSecurityPolicy(scriptNonce = "") {
  const scriptSource = /^[A-Za-z0-9_-]{16,64}$/.test(scriptNonce)
    ? ` script-src 'nonce-${scriptNonce}';`
    : "";
  return "default-src 'none'; img-src https: data:; style-src 'unsafe-inline';" +
    scriptSource + " base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
}

function textResponse(body, status, extraHeaders = {}) {
  return new Response(body, {
    status,
    headers: commonHeaders({
      "content-type": "text/plain; charset=utf-8",
      ...extraHeaders,
    }),
  });
}

function htmlResponse(body, status = 200, extraHeaders = {}) {
  return new Response(body, {
    status,
    headers: commonHeaders({
      "content-type": "text/html; charset=utf-8",
      "content-security-policy": reportContentSecurityPolicy(),
      "x-robots-tag": "noindex, nofollow, noarchive",
      ...extraHeaders,
    }),
  });
}

function jsonResponse(value, status = 200, extraHeaders = {}) {
  return new Response(JSON.stringify(value), {
    status,
    headers: commonHeaders({
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
      ...extraHeaders,
    }),
  });
}

function resolveUpstream(env, filename = "run.sh") {
  const owner = String(env.GITHUB_OWNER || "").trim();
  const repository = String(env.GITHUB_REPO || "").trim();
  const ref = String(env.GITHUB_REF || "main").trim();
  const componentPattern = /^[A-Za-z0-9_.-]+$/;
  const refPattern = /^[A-Za-z0-9._/-]+$/;

  if (
    !componentPattern.test(owner) ||
    !componentPattern.test(repository) ||
    !refPattern.test(ref) ||
    ref.includes("..") ||
    ref.startsWith("/") ||
    owner === "YOUR_GITHUB_USER" ||
    repository === "YOUR_GITHUB_REPO" ||
    !["run.sh", "install-node.sh"].includes(filename)
  ) {
    return null;
  }

  return `https://raw.githubusercontent.com/${owner}/${repository}/${ref}/${filename}`;
}

function reportPromotion(env) {
  const configuredText = boundedText(env.PROMOTION_TEXT, 120);
  const configuredUrl = normalizeHttpsUrl(env.PROMOTION_URL, 2048);
  const owner = String(env.GITHUB_OWNER || "").trim();
  const repository = String(env.GITHUB_REPO || "").trim();
  const componentPattern = /^[A-Za-z0-9_.-]+$/;
  const repositoryUrl = componentPattern.test(owner) && componentPattern.test(repository) &&
    owner !== "YOUR_GITHUB_USER" && repository !== "YOUR_GITHUB_REPO"
    ? `https://github.com/${owner}/${repository}`
    : "";
  return {
    text: configuredText,
    url: configuredUrl,
    projectUrl: repositoryUrl,
  };
}

function configuredProbeVersion(env) {
  const version = String(env.PROBE_VERSION || "v1.0.13").trim();
  return /^v\d+\.\d+\.\d+$/.test(version) ? version : null;
}

function resolveProbeAsset(env, version, asset) {
  if (![
    "sqprobe-linux-amd64",
    "sqprobe-linux-arm64",
    "sq-node-linux-amd64",
    "sq-node-linux-arm64",
    "checksums.txt",
    "release-manifest.json",
    "release-manifest.json.sig",
  ].includes(asset)) {
    return null;
  }
  const owner = String(env.GITHUB_OWNER || "").trim();
  const repository = String(env.GITHUB_REPO || "").trim();
  const configuredVersion = configuredProbeVersion(env);
  const componentPattern = /^[A-Za-z0-9_.-]+$/;
  if (!componentPattern.test(owner) || !componentPattern.test(repository) ||
      !configuredVersion || version !== configuredVersion || owner === "YOUR_GITHUB_USER" ||
      repository === "YOUR_GITHUB_REPO") return null;
  return `https://github.com/${owner}/${repository}/releases/download/${version}/${asset}`;
}

function boundedText(value, maxLength) {
  const valueText = String(value || "").replaceAll("\0", "").trim();
  return valueText.length <= maxLength ? valueText : null;
}

function parseEpoch(value) {
  if (value === "" || value == null) return null;
  if (!/^\d{1,12}$/.test(String(value))) return Number.NaN;
  const epoch = Number(value);
  if (!Number.isSafeInteger(epoch) || epoch < 946684800 || epoch > 4102444800) {
    return Number.NaN;
  }
  return epoch;
}

function parseOptionalInteger(value, min, max) {
  const text = String(value ?? "").trim();
  if (!text) return null;
  if (!/^\d{1,16}$/.test(text)) return Number.NaN;
  const parsed = Number(text);
  return Number.isSafeInteger(parsed) && parsed >= min && parsed <= max
    ? parsed
    : Number.NaN;
}

function normalizeHttpsUrl(value, maxLength = 2048) {
  const valueText = boundedText(value, maxLength);
  if (valueText == null || valueText === "") return "";
  try {
    const url = new URL(valueText);
    if (url.protocol !== "https:" || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

function normalizeNodeQualityUrl(value) {
  const normalized = normalizeHttpsUrl(value);
  if (!normalized) return null;
  const url = new URL(normalized);
  if (!/^(?:www\.)?nodequality\.com$/i.test(url.hostname)) return null;
  const match = url.pathname.match(/^\/r\/([A-Za-z0-9_-]{16,128})\/?$/);
  if (!match || url.search || url.hash) return null;
  return `https://nodequality.com/r/${match[1]}`;
}

function stripUnsafeTerminalText(value) {
  return String(value ?? "")
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/g, "")
    .replace(/[\u202a-\u202e\u2066-\u2069]/g, "")
    .replace(/\r\n?/g, "\n")
    .replace(/\n{4,}/g, "\n\n\n")
    .trim();
}

function sanitizeAnsiTerminalText(value) {
  const sequences = [];
  let text = String(value ?? "")
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "")
    .replace(/[\ue000-\uf8ff]/g, "");
  text = text.replace(/\x1b\[[0-9;]*m/g, (sequence) => {
    const marker = `\ue000${sequences.length}\ue001`;
    sequences.push(sequence);
    return marker;
  });
  text = text
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/g, "")
    .replace(/[\u202a-\u202e\u2066-\u2069]/g, "")
    .replace(/\ue000(\d+)\ue001/g, (_match, index) => sequences[Number(index)] || "");
  return text.replace(/\n+$/g, "");
}

function byteLength(value) {
  return encoder.encode(value).byteLength;
}

export function validateNodeQualitySnapshot(snapshotText) {
  if (!snapshotText) return { value: null, text: "" };
  if (byteLength(snapshotText) > MAX_SNAPSHOT_BYTES) {
    return { error: "NodeQuality snapshot is too large" };
  }

  let input;
  try {
    input = JSON.parse(snapshotText);
  } catch {
    return { error: "Invalid NodeQuality snapshot" };
  }
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    return { error: "Invalid NodeQuality snapshot" };
  }

  let inputPages;
  if (input.version === 2 && Array.isArray(input.pages)) {
    inputPages = input.pages;
  } else if (input.version === 1 && Array.isArray(input.sections)) {
    inputPages = [];
    for (let index = 0; index < input.sections.length; index += 1) {
      const section = input.sections[index];
      if (!section || typeof section !== "object" || Array.isArray(section)) {
        return { error: "Invalid NodeQuality snapshot section" };
      }
      const name = stripUnsafeTerminalText(section.name);
      const kind = String(section.kind || "").toLowerCase();
      const parts = name.split("/");
      const expectedSuffix = kind === "log" ? ".log" : kind === "json" ? ".json" : "";
      if (
        !name ||
        name.length > 160 ||
        /[\n\t]/.test(name) ||
        name.startsWith("/") ||
        name.includes("\\") ||
        parts.some((part) => !part || part === "." || part === "..") ||
        !expectedSuffix ||
        !name.toLowerCase().endsWith(expectedSuffix)
      ) {
        return { error: "Invalid NodeQuality snapshot section" };
      }
      const lowerName = name.toLowerCase();
      let id = `nodequality-${index + 1}`;
      let title = name.split("/").pop().replace(/\.(?:log|json)$/i, "");
      if (/(?:header[_-]?info|headerinfo)/.test(lowerName)) {
        id = "all";
        title = "全部";
      } else if (/(?:hardware|basic)/.test(lowerName)) {
        id = "basic";
        title = "基本信息";
      } else if (/(?:ip[_-]?quality|ipquality)/.test(lowerName)) {
        id = "ip-quality";
        title = "IP质量";
      } else if (/(?:net[_-]?quality|network[_-]?quality|networkquality|network\.log)/.test(lowerName)) {
        id = "network-quality";
        title = "网络质量";
      } else if (/(?:backtrace|return|route)/.test(lowerName)) {
        id = "return-route";
        title = "回程路由";
      }
      inputPages.push({
        id,
        title,
        format: "ansi",
        content: section.content,
        image_url: "",
        source: name,
        truncated: section.truncated === true,
      });
    }
  } else {
    return { error: "Invalid NodeQuality snapshot" };
  }
  if (inputPages.length < 1 || inputPages.length > MAX_SNAPSHOT_FILES) {
    return { error: "Invalid NodeQuality snapshot" };
  }

  const pages = [];
  const pageIds = new Set();
  let totalTextBytes = 0;
  for (const page of inputPages) {
    if (!page || typeof page !== "object" || Array.isArray(page)) {
      return { error: "Invalid NodeQuality snapshot page" };
    }
    const id = String(page.id || "").toLowerCase();
    const title = stripUnsafeTerminalText(page.title);
    const format = String(page.format || "").toLowerCase();
    const content = sanitizeAnsiTerminalText(page.content);
    const imageUrl = normalizeHttpsUrl(page.image_url, 2048);
    const source = stripUnsafeTerminalText(page.source);
    if (
      !/^[a-z0-9](?:[a-z0-9-]{0,31})$/.test(id) ||
      pageIds.has(id) ||
      !title ||
      title.length > 32 ||
      /[\n\t]/.test(title) ||
      !["ansi", "image"].includes(format) ||
      source.length > 160 ||
      /[\n\t]/.test(source) ||
      (format === "ansi" && (!content || imageUrl)) ||
      (format === "image" && (!imageUrl || content))
    ) {
      return { error: "Invalid NodeQuality snapshot page" };
    }
    pageIds.add(id);
    const contentBytes = byteLength(content);
    totalTextBytes += contentBytes;
    if (
      contentBytes > MAX_SNAPSHOT_FILE_BYTES ||
      totalTextBytes > MAX_SNAPSHOT_TEXT_BYTES
    ) {
      return { error: "NodeQuality snapshot text is too large" };
    }
    pages.push({
      id,
      title,
      format,
      content,
      image_url: imageUrl || "",
      source,
      truncated: page.truncated === true,
    });
  }

  const omittedFiles = Number(input.omitted_files || 0);
  if (!Number.isInteger(omittedFiles) || omittedFiles < 0 || omittedFiles > 10000) {
    return { error: "Invalid NodeQuality snapshot metadata" };
  }
  const value = {
    version: 2,
    pages,
    truncated: input.truncated === true,
    omitted_files: omittedFiles,
  };
  const text = JSON.stringify(value);
  if (byteLength(text) > MAX_SNAPSHOT_BYTES) {
    return { error: "NodeQuality snapshot is too large" };
  }
  return { value, text };
}

async function parseResultPayload(request) {
  const declaredLength = Number(request.headers.get("content-length") || 0);
  if (declaredLength > MAX_BODY_BYTES) {
    return { error: "Request Too Large", status: 413 };
  }
  const contentType = request.headers.get("content-type") || "";
  const body = await request.arrayBuffer();
  if (body.byteLength > MAX_BODY_BYTES) {
    return { error: "Request Too Large", status: 413 };
  }

  if (contentType.toLowerCase().startsWith("application/x-www-form-urlencoded")) {
    return { params: new URLSearchParams(decoder.decode(body)), snapshotText: "" };
  }
  if (!contentType.toLowerCase().startsWith("multipart/form-data")) {
    return { error: "Unsupported Media Type", status: 415 };
  }

  let form;
  try {
    form = await new Response(body, {
      headers: { "content-type": contentType },
    }).formData();
  } catch {
    return { error: "Invalid multipart data", status: 400 };
  }
  const params = new URLSearchParams();
  for (const field of RESULT_FIELDS) {
    const value = form.get(field);
    if (value == null) continue;
    if (typeof value !== "string") {
      return { error: "Invalid result data", status: 400 };
    }
    params.set(field, value);
  }

  const snapshotPart = form.get("nq_snapshot");
  let snapshotText = "";
  if (snapshotPart != null) {
    if (typeof snapshotPart === "string") {
      snapshotText = snapshotPart;
    } else if (
      typeof snapshotPart === "object" &&
      typeof snapshotPart.text === "function" &&
      Number(snapshotPart.size) <= MAX_SNAPSHOT_BYTES
    ) {
      snapshotText = await snapshotPart.text();
    } else {
      return { error: "NodeQuality snapshot is too large", status: 413 };
    }
  }
  return { params, snapshotText };
}

function finiteMetric(value, min, max) {
  if (value == null || value === "") return null;
  const number = Number(value);
  return Number.isFinite(number) && number >= min && number <= max ? number : null;
}

export function validateSpeedData(value) {
  const input = String(value || "").trim();
  if (!input) return { value: "", reports: [] };
  if (byteLength(input) > MAX_SPEED_DATA_BYTES) {
    return { error: "Speed data is too large" };
  }
  const lines = input.split("\n").filter((line) => line.trim() !== "");
  if (lines.length < 1 || lines.length > 80) {
    return { error: "Invalid speed data" };
  }
  const reports = [];
  let totalMeasurements = 0;
  const reportKeys = new Set();
  for (const line of lines) {
    let source;
    try {
      source = JSON.parse(line);
    } catch {
      return { error: "Invalid speed data" };
    }
    if (!source || typeof source !== "object" || Array.isArray(source) ||
        source.version !== 1 || !REGION_CODES.has(String(source.region?.code || "")) ||
        !["v4", "v6"].includes(source.family) ||
        !Array.isArray(source.modes) || source.modes.length !== 1 || source.modes[0] !== "s" ||
        source.duration_seconds !== 5 || !TARGET_SPEEDS.has(source.target_mbps) ||
        !Array.isArray(source.results) || source.results.length < 1 || source.results.length > CARRIER_CODES.size) {
      return { error: "Invalid speed data" };
    }
    const regionName = boundedText(source.region?.name, 24);
    const leaseID = String(source.lease_id || "");
    const startedAt = parseEpoch(source.started_at);
    const completedAt = parseEpoch(source.completed_at);
    const reportKey = `${source.region.code}:${source.family}`;
    if (!regionName || !/^[A-Za-z0-9_-]{8,96}$/.test(leaseID) ||
        startedAt == null || completedAt == null || Number.isNaN(startedAt) ||
        Number.isNaN(completedAt) || completedAt < startedAt || reportKeys.has(reportKey)) {
      return { error: "Invalid speed data" };
    }
    reportKeys.add(reportKey);
    const results = [];
    const carriers = new Set();
    for (const item of source.results) {
      totalMeasurements += 1;
      const carrier = String(item?.carrier || "");
      const label = boundedText(item?.label, 32);
      const status = item?.status === "ok" ? "ok" : item?.status === "failed" ? "failed" : "";
      const nodeID = String(item?.node_id || "");
      const latency = item?.latency_ms == null ? null : finiteMetric(item.latency_ms, 0, 120000);
      if (!CARRIER_CODES.has(carrier) || !label || !status ||
          carriers.has(carrier) ||
          (nodeID && !/^[a-f0-9]{32}$/.test(nodeID)) ||
          (item?.latency_ms != null && latency == null)) {
        return { error: "Invalid speed data" };
      }
      carriers.add(carrier);
      const normalizeMode = (mode) => {
        if (mode == null) return null;
        if (!mode || typeof mode !== "object" || Array.isArray(mode)) return undefined;
        const downloadMbps = finiteMetric(mode.download_mbps, 0, 1000000);
        const uploadMbps = finiteMetric(mode.upload_mbps, 0, 1000000);
        const downloadBytes = finiteMetric(mode.download_bytes, 0, Number.MAX_SAFE_INTEGER);
        const uploadBytes = finiteMetric(mode.upload_bytes, 0, Number.MAX_SAFE_INTEGER);
        if ([downloadMbps, uploadMbps, downloadBytes, uploadBytes].some((metric) => metric == null) ||
            !Number.isSafeInteger(downloadBytes) || !Number.isSafeInteger(uploadBytes)) return undefined;
        return { download_mbps: downloadMbps, upload_mbps: uploadMbps, download_bytes: downloadBytes, upload_bytes: uploadBytes };
      };
      const single = normalizeMode(item.single);
      const multi = normalizeMode(item.multi);
      if (single === undefined || multi !== null || (status === "ok" && single == null)) {
        return { error: "Invalid speed data" };
      }
      if (single && (single.download_mbps > source.target_mbps * 1.05 ||
          single.upload_mbps > source.target_mbps * 1.05)) {
        return { error: "Invalid speed data" };
      }
      results.push({
        carrier,
        label,
        node_id: nodeID,
        latency_ms: latency,
        status,
        error: status === "failed" ? boundedText(item.error, 200) || "测速失败" : "",
        single,
        multi,
      });
    }
    reports.push({
      version: 1,
      lease_id: leaseID,
      started_at: startedAt,
      completed_at: completedAt,
      region: { code: source.region.code, name: regionName },
      family: source.family,
      duration_seconds: source.duration_seconds,
      target_mbps: source.target_mbps,
      modes: source.modes,
      results,
    });
  }
  if (totalMeasurements > 240) return { error: "Invalid speed data" };
  return { value: JSON.stringify(reports), reports };
}

export function validateResultInput(params) {
  const regions = boundedText(params.get("regions"), 300);
  const speedText = boundedText(params.get("speed_text"), 16000);
  const speedData = validateSpeedData(params.get("speed_data"));
  const nqTimeSource = boundedText(params.get("nq_time_source"), 160);
  const rawIdentityReason = String(params.get("nq_identity_reason") || "");
  const version = boundedText(params.get("version"), 32);
  const mode = String(params.get("mode") || "s");
  const ipMode = String(params.get("ip_mode") || "v4");
  const speedUrl = normalizeHttpsUrl(params.get("speed_url"));
  const testedAt = parseEpoch(params.get("tested_at"));
  const durationSeconds = parseOptionalInteger(params.get("duration_seconds"), 5, 5);
  const targetMbps = parseOptionalInteger(params.get("target_mbps"), 100, 400);
  const trafficRxBytes = parseOptionalInteger(
    params.get("traffic_rx_bytes"), 0, Number.MAX_SAFE_INTEGER,
  );
  const trafficTxBytes = parseOptionalInteger(
    params.get("traffic_tx_bytes"), 0, Number.MAX_SAFE_INTEGER,
  );
  const rawBindStatus = String(params.get("bind_status") || "standalone");

  if (
    !regions ||
    speedText == null ||
    speedData.error ||
    nqTimeSource == null ||
    !version ||
    !MODES.has(mode) ||
    !IP_MODES.has(ipMode) ||
    speedUrl == null ||
    testedAt == null || Number.isNaN(testedAt) ||
    durationSeconds == null || Number.isNaN(durationSeconds) ||
    targetMbps == null || Number.isNaN(targetMbps) || !TARGET_SPEEDS.has(targetMbps) ||
    Number.isNaN(trafficRxBytes) ||
    Number.isNaN(trafficTxBytes) ||
    ((trafficRxBytes == null) !== (trafficTxBytes == null)) ||
    (trafficRxBytes != null && !Number.isSafeInteger(trafficRxBytes + trafficTxBytes)) ||
    !speedData.value
  ) {
    return { error: "Invalid result data" };
  }

  const bindStatus = VERIFIED_STATUSES.has(rawBindStatus) || REJECTED_BIND_STATUSES.has(rawBindStatus)
    ? rawBindStatus
    : "standalone";
  let nqUrl = "";
  let nqTestedAt = null;
  let timeGapSeconds = null;
  let nqIdentityReason = "";

  if (VERIFIED_STATUSES.has(bindStatus)) {
    nqUrl = normalizeNodeQualityUrl(params.get("nq_url"));
    nqTestedAt = parseEpoch(params.get("nq_tested_at"));
    const gapText = String(params.get("time_gap_seconds") || "");
    timeGapSeconds = /^\d{1,12}$/.test(gapText) ? Number(gapText) : null;
    nqIdentityReason = ["full_ip", "masked_ip_and_asn"].includes(rawIdentityReason)
      ? rawIdentityReason
      : "";
    if (rawIdentityReason && !nqIdentityReason) {
      return { error: "Invalid NodeQuality identity data" };
    }
    if (
      !nqUrl ||
      Number.isNaN(nqTestedAt) ||
      (timeGapSeconds != null && !Number.isSafeInteger(timeGapSeconds))
    ) {
      return { error: "Invalid NodeQuality data" };
    }
    if (bindStatus !== "verified_time_unknown") {
      if (testedAt == null || nqTestedAt == null || timeGapSeconds == null) {
        return { error: "Verified reports require timestamps" };
      }
      if (Math.abs(Math.abs(testedAt - nqTestedAt) - timeGapSeconds) > 2) {
        return { error: "Invalid time gap" };
      }
      if (
        (bindStatus === "verified" && timeGapSeconds > NODEQUALITY_TIME_GAP_SECONDS) ||
        (bindStatus === "verified_stale" && timeGapSeconds <= NODEQUALITY_TIME_GAP_SECONDS)
      ) {
        return { error: "Invalid time verification status" };
      }
    } else if (nqTestedAt != null || timeGapSeconds != null) {
      return { error: "Unknown-time reports cannot include a time gap" };
    }
  }

  return {
    value: {
      regions,
      mode,
      ipMode,
      speedUrl,
      speedText,
      speedData: speedData.value,
      speedReports: speedData.reports,
      durationSeconds,
      targetMbps,
      trafficRxBytes,
      trafficTxBytes,
      testedAt,
      nqUrl,
      nqTestedAt,
      nqTimeSource: VERIFIED_STATUSES.has(bindStatus) ? nqTimeSource : "",
      timeGapSeconds,
      nqIdentityReason,
      bindStatus,
      version,
    },
  };
}

function randomId() {
  const bytes = new Uint8Array(9);
  crypto.getRandomValues(bytes);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

async function clientHash(request, env) {
  const forwarded = clientAddress(request);
  const salt = String(env.RATE_LIMIT_SALT || "speedquality-local");
  return hashText(`${salt}|${forwarded}`, 32);
}

function clientAddress(request) {
  return request.headers.get("cf-connecting-ip") ||
    request.headers.get("x-forwarded-for")?.split(",", 1)[0]?.trim() ||
    "unknown";
}

function ipv4Parts(value) {
  const parts = String(value || "").split(".");
  if (parts.length !== 4 || parts.some((part) => !/^\d{1,3}$/.test(part))) return null;
  const numbers = parts.map(Number);
  return numbers.every((part) => part >= 0 && part <= 255) ? numbers : null;
}

function ipv6Parts(value) {
  let address = String(value || "").trim().toLowerCase().split("%", 1)[0];
  if (!address || !/^[0-9a-f:.]+$/.test(address)) return null;
  if (address.includes(".")) {
    const separator = address.lastIndexOf(":");
    const embedded = ipv4Parts(address.slice(separator + 1));
    if (separator < 0 || !embedded) return null;
    address = `${address.slice(0, separator)}:${((embedded[0] << 8) | embedded[1]).toString(16)}:${((embedded[2] << 8) | embedded[3]).toString(16)}`;
  }
  const halves = address.split("::");
  if (halves.length > 2) return null;
  const parseHalf = (half) => half ? half.split(":") : [];
  const left = parseHalf(halves[0]);
  const right = parseHalf(halves[1]);
  if ([...left, ...right].some((part) => !/^[0-9a-f]{1,4}$/.test(part))) return null;
  if (halves.length === 1) return left.length === 8 ? left : null;
  const missing = 8 - left.length - right.length;
  if (missing < 1) return null;
  return [...left, ...Array(missing).fill("0"), ...right];
}

export function maskedReportAddress(value) {
  const address = String(value || "").trim();
  const mappedIPv4 = address.match(/^::ffff:(\d{1,3}(?:\.\d{1,3}){3})$/i)?.[1] || "";
  const ipv4 = ipv4Parts(mappedIPv4 || address);
  if (ipv4) return `${ipv4[0]}.${ipv4[1]}.*.*`;
  const ipv6 = ipv6Parts(address);
  if (!ipv6) return "";
  return `${ipv6.slice(0, 3).map((part) => Number.parseInt(part, 16).toString(16)).join(":")}::/48`;
}

function detectedRegion(cf) {
  const code = String(cf?.regionCode || "").trim().toLowerCase().replace(/^cn-/, "");
  if (REGION_CODES.has(code)) return code;
  const rawName = String(cf?.region || "").trim();
  const name = rawName.toLowerCase()
    .replace(/\s+(province|autonomous region|municipality)$/i, "");
  if (REGION_ALIASES[name]) return REGION_ALIASES[name];
  for (const [regionCode, regionName] of Object.entries(REGION_NAMES)) {
    if (rawName.includes(regionName)) return regionCode;
  }
  return "";
}

function detectedCarrier(cf) {
  const asn = Number(cf?.asn || 0);
  for (const [carrier, values] of Object.entries(CARRIER_ASNS)) {
    if (values.has(asn)) return carrier;
  }
  const organization = String(cf?.asOrganization || "").toLowerCase();
  if (/chinanet|china telecom|ctgnet/.test(organization)) return "ct";
  if (/china unicom|cncgroup|china169|\bunicom\b/.test(organization)) return "cu";
  if (/china mobile|\bcmnet\b|cmi limited/.test(organization)) return "cm";
  return "";
}

function detectNodeEnvironment(request) {
  const address = clientAddress(request);
  const family = address.includes(":")
    ? "v6"
    : /^\d{1,3}(?:\.\d{1,3}){3}$/.test(address) ? "v4" : "";
  const region = detectedRegion(request.cf);
  const carrier = detectedCarrier(request.cf);
  return jsonResponse({
    address: family ? address : "",
    family,
    country_code: String(request.cf?.country || "").toUpperCase().slice(0, 2),
    region,
    region_name: region ? REGION_NAMES[region] : "",
    carrier,
    carrier_name: carrier ? CARRIER_NAMES[carrier] : "",
    asn: Number(request.cf?.asn || 0) || null,
    as_organization: boundedText(request.cf?.asOrganization, 120) || "",
  });
}

function nodeUpdateInfo(request, env) {
  const url = new URL(request.url);
  if ((url.searchParams.get("channel") || "stable") !== "stable") {
    return jsonResponse({ error: "unsupported_update_channel" }, 400);
  }
  const releaseVersion = configuredProbeVersion(env);
  if (!releaseVersion || !resolveProbeAsset(env, releaseVersion, "release-manifest.json") ||
      !resolveProbeAsset(env, releaseVersion, "release-manifest.json.sig")) {
    return jsonResponse({ error: "node_update_unavailable" }, 503);
  }
  const base = new URL(`/bin/${releaseVersion}/`, url.origin).href;
  return jsonResponse({
    channel: "stable",
    version: releaseVersion,
    manifest_url: `${base}release-manifest.json`,
    signature_url: `${base}release-manifest.json.sig`,
  });
}

async function hashText(value, length = 64) {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    encoder.encode(value),
  );
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("")
    .slice(0, length);
}

function numberSetting(value, fallback, min, max) {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed >= min && parsed <= max ? parsed : fallback;
}

function nodeQualityBindingEnabled(env) {
  return !new Set(["0", "false", "off", "no", "disabled"])
    .has(String(env.NQ_BINDING_ENABLED ?? "true").trim().toLowerCase());
}

function randomToken() {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

async function parseSmallForm(request) {
  const length = Number(request.headers.get("content-length") || 0);
  if (Number.isFinite(length) && length > 8192) return null;
  const type = (request.headers.get("content-type") || "").split(";", 1)[0].trim().toLowerCase();
  if (type !== "application/x-www-form-urlencoded") return null;
  const text = await request.text();
  if (byteLength(text) > 8192) return null;
  return new URLSearchParams(text);
}

async function parseSmallJSON(request) {
  const length = Number(request.headers.get("content-length") || 0);
  if (Number.isFinite(length) && length > 32 * 1024) return null;
  const type = (request.headers.get("content-type") || "").split(";", 1)[0].trim().toLowerCase();
  if (type !== "application/json") return null;
  const bytes = await readStreamLimited(request.body, 32 * 1024).catch(() => null);
  if (bytes == null) return null;
  try {
    const value = JSON.parse(decoder.decode(bytes));
    return value && typeof value === "object" && !Array.isArray(value) ? value : null;
  } catch {
    return null;
  }
}

async function callCommunityCore(env, pathname, input, options = {}) {
  if (!env.NODE_CORE || typeof env.NODE_CORE.fetch !== "function") {
    return { error: "node_service_unavailable", status: 503 };
  }
  const headers = {
    "content-type": "application/json",
    "x-node-core-secret": String(env.NODE_CORE_SECRET || ""),
  };
  if (options.clientIP) headers["x-community-client-ip"] = options.clientIP;
  if (options.region) headers["x-community-region"] = options.region;
  if (options.carrier) headers["x-community-carrier"] = options.carrier;
  if (options.apiToken) headers["x-node-api-token"] = options.apiToken;
  if (options.requestID) headers["x-request-id"] = options.requestID;
  let response;
  try {
    response = await env.NODE_CORE.fetch(new Request(`https://node-core.internal${pathname}`, {
      method: "POST",
      headers,
      body: JSON.stringify(input),
    }));
  } catch {
    return { error: "node_service_unavailable", status: 502 };
  }
  const body = await readStreamLimited(response.body, 128 * 1024).catch(() => null);
  if (body == null) return { error: "node_service_invalid_response", status: 502 };
  let value;
  try {
    value = JSON.parse(decoder.decode(body));
  } catch {
    return { error: "node_service_invalid_response", status: 502 };
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { error: "node_service_invalid_response", status: 502 };
  }
  return { value, status: response.status, ok: response.ok };
}

function nodeAPIToken(request) {
  const match = String(request.headers.get("authorization") || "")
    .match(/^Bearer (sqa_[A-Za-z0-9_-]{24,128})$/);
  return match && NODE_API_TOKEN_PATTERN.test(match[1]) ? match[1] : "";
}

async function proxyCommunityNodeRequest(request, env, operation) {
  const input = await parseSmallJSON(request);
  if (!input) return jsonResponse({ error: "invalid_request" }, 400);
  const paths = {
    register: "/community/register",
    heartbeat: "/community/heartbeat",
    unregister: "/community/unregister",
    routeKey: "/community/route-key",
    control: "/community/control",
    controlAck: "/community/control/ack",
    resolve: "/community/resolve",
  };
  const authenticatedOperations = new Set(["heartbeat", "unregister", "routeKey", "control", "controlAck"]);
  const apiToken = authenticatedOperations.has(operation)
    ? nodeAPIToken(request)
    : "";
  if (authenticatedOperations.has(operation) && !apiToken) {
    return jsonResponse({ error: "invalid_node_token" }, 401);
  }
  if (operation === "resolve" && !NODE_ROUTE_PATTERN.test(String(input.route_key || ""))) {
    return jsonResponse({ error: "invalid_route_key" }, 400);
  }
  const result = await callCommunityCore(env, paths[operation], input, {
    apiToken,
    clientIP: clientAddress(request),
    region: detectedRegion(request.cf),
    carrier: detectedCarrier(request.cf),
    requestID: requestID(request),
  });
  if (!result.value) return jsonResponse({ error: result.error }, result.status);
  return jsonResponse(result.value, result.status, result.status === 429
    ? { "retry-after": "3600" }
    : {});
}

function parseSessionRegions(value) {
  const regions = String(value || "").split(",").map((region) => region.trim()).filter(Boolean);
  if (regions.length < 1 || regions.length > MAX_SESSION_REGIONS || new Set(regions).size !== regions.length ||
      !regions.every((region) => REGION_CODES.has(region))) return null;
  return regions;
}

async function enforceDailySessionLimit(request, env, now) {
  const limit = numberSetting(env.DAILY_SESSION_LIMIT, DEFAULT_DAILY_SESSION_LIMIT, 1, 1000);
  const day = new Date(now * 1000).toISOString().slice(0, 10);
  const hash = await clientHash(request, env);
  const row = await env.DB.prepare(
    `INSERT INTO session_rate_limits (day, client_hash, count, updated_at)
     VALUES (?, ?, 1, ?)
     ON CONFLICT(day, client_hash) DO UPDATE SET
       count = count + 1,
       updated_at = excluded.updated_at
     RETURNING count`,
  ).bind(day, hash, now).first();
  return Number(row?.count || 0) <= limit;
}

async function createSession(request, env) {
  const started = Date.now();
  const currentRequestID = requestID(request);
  if (!env.DB) return textResponse("Session storage is not configured\n", 503, { "cache-control": "no-store" });
  const params = await parseSmallForm(request);
  if (!params) return textResponse("Invalid session request\n", 400, { "cache-control": "no-store" });
  const regions = parseSessionRegions(params.get("regions"));
  const mode = String(params.get("mode") || "s");
  const ipMode = String(params.get("ip_mode") || "v4");
  const duration = parseOptionalInteger(params.get("duration_seconds"), 5, 5);
  const targetMbps = parseOptionalInteger(params.get("target_mbps"), 100, 400);
  const nodeRoute = String(params.get("node_route") || "");
  if (!regions || !MODES.has(mode) || !IP_MODES.has(ipMode) || duration == null ||
      Number.isNaN(duration) || targetMbps == null || Number.isNaN(targetMbps) ||
      !TARGET_SPEEDS.has(targetMbps) || (nodeRoute && !NODE_ROUTE_PATTERN.test(nodeRoute))) {
    return textResponse("Invalid session request\n", 400, { "cache-control": "no-store" });
  }
  let communityNodeID = "";
  if (!nodeRoute && regions.some((region) => !PUBLIC_REGION_CODES.has(region))) {
    return textResponse("Requested region is not open for public speed tests\n", 400, {
      "cache-control": "no-store",
    });
  }
  if (nodeRoute) {
    const resolved = await callCommunityCore(env, "/community/resolve", {
      route_key: nodeRoute,
    }, { clientIP: clientAddress(request) });
    if (!resolved.value || !resolved.ok) {
      const status = resolved.status === 404 ? 404 : 503;
      return textResponse("Specified node is unavailable\n", status, { "cache-control": "no-store" });
    }
    const node = resolved.value;
    const families = Array.isArray(node.families) ? new Set(node.families) : new Set();
    if (!COMMUNITY_NODE_ID_PATTERN.test(String(node.node_id || "")) ||
        !REGION_CODES.has(String(node.region || "")) || regions.length !== 1 ||
        regions[0] !== node.region ||
        (ipMode === "v4" && !families.has("v4")) ||
        (ipMode === "v6" && !families.has("v6")) ||
        !TARGET_SPEEDS.has(Number(node.max_mbps)) || targetMbps > Number(node.max_mbps)) {
      return textResponse("Session does not match the specified node\n", 400, {
        "cache-control": "no-store",
      });
    }
    communityNodeID = node.node_id;
  }
  const now = Math.floor(Date.now() / 1000);
  if (!(await enforceDailySessionLimit(request, env, now))) {
    return textResponse("Daily session limit exceeded\n", 429, {
      "cache-control": "no-store",
      "retry-after": "3600",
    });
  }
  const token = randomToken();
  const tokenHash = await hashText(token);
  const sourceHash = await clientHash(request, env);
  const familyCount = ipMode === "v4" ? 1 : 2;
  const secondsPerLease = 3 * 2 * (duration + 2) + 30;
  const ttl = Math.min(
    6 * 3600,
    Math.max(20 * 60, regions.length * familyCount * secondsPerLease + 10 * 60),
  );
  const expiresAt = now + ttl;
  await env.DB.prepare(
    `INSERT INTO sessions (
      token_hash, created_at, expires_at, client_hash, regions, mode, ip_mode,
      duration_seconds, target_mbps, lease_limit, community_node_id, completed_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
  ).bind(
    tokenHash,
    now,
    expiresAt,
    sourceHash,
    regions.join(","),
    mode,
    ipMode,
    duration,
    targetMbps,
    regions.length * familyCount * 2,
    communityNodeID,
  ).run();
  logEvent(env, "info", "session.created", {
    request_id: currentRequestID,
    session_id: tokenHash.slice(0, 12),
    regions,
    ip_mode: ipMode,
    target_mbps: targetMbps,
    exact_node: Boolean(communityNodeID),
    lease_limit: regions.length * familyCount * 2,
    expires_at: expiresAt,
    duration_ms: Date.now() - started,
  });
  return textResponse(`${token}\n`, 201, {
    "cache-control": "no-store",
    "x-session-expires-at": String(expiresAt),
  });
}

async function authorizeSession(request, env, now = Math.floor(Date.now() / 1000)) {
  if (!env.DB) return { error: "Session storage is not configured", status: 503 };
  const authorization = request.headers.get("authorization") || "";
  const match = authorization.match(/^Bearer ([A-Za-z0-9_-]{32,128})$/);
  if (!match || !SESSION_TOKEN_PATTERN.test(match[1])) return { error: "Invalid session", status: 401 };
  const tokenHash = await hashText(match[1]);
  const session = await env.DB.prepare(
    "SELECT * FROM sessions WHERE token_hash = ? AND expires_at > ?",
  ).bind(tokenHash, now).first();
  if (!session || session.client_hash !== await clientHash(request, env)) {
    return { error: "Invalid session", status: 401 };
  }
  return { session, tokenHash };
}

function requestedModes(mode) {
  return mode === "s" ? ["s"] : [];
}

function parseStaticNodeConfiguration(env) {
  const raw = String(env.STATIC_NODES || "").trim();
  if (!raw || byteLength(raw) > 128 * 1024) return null;
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function hasStaticNodeConfiguration(env) {
  const configuration = parseStaticNodeConfiguration(env);
  return Boolean(configuration && Object.entries(configuration).some(
    ([region, nodes]) => REGION_CODES.has(region) && Array.isArray(nodes) && nodes.length > 0,
  ));
}

function staticAddressFamily(address) {
  const value = String(address || "");
  const ipv4 = value.split(".");
  if (ipv4.length === 4 && ipv4.every((part) =>
    /^(?:0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255)) return "v4";
  if (!value.includes(":") || !/^[0-9a-f:.]+$/i.test(value)) return "";
  try {
    const parsed = new URL(`http://[${value}]:80/`);
    return parsed.hostname ? "v6" : "";
  } catch {
    return "";
  }
}

function normalizeStaticNode(node, input) {
  if (!node || typeof node !== "object" || Array.isArray(node) || node.enabled === false) return null;
  const carrier = String(node.carrier || "").toLowerCase();
  const address = String(node.address || "");
  const port = Number(node.port);
  const scheme = String(node.scheme || "http").toLowerCase();
  const maxMbps = Number(node.max_mbps);
  const readyDelay = Number(node.ready_delay_ms ?? 0);
  const configuredLabel = boundedText(node.label, 32);
  if (!CARRIER_CODES.has(carrier) || staticAddressFamily(address) !== input.family ||
      !Number.isInteger(port) || port < 1 || port > 65535 ||
      !["http", "https"].includes(scheme) || !TARGET_SPEEDS.has(maxMbps) ||
      maxMbps < input.target_mbps || !Number.isInteger(readyDelay) ||
      readyDelay < 0 || readyDelay > 10000 || configuredLabel == null) return null;
  return {
    carrier,
    address,
    port,
    scheme,
    maxMbps,
    readyDelay,
    label: configuredLabel || `${REGION_NAMES[input.region]}${CARRIER_NAMES[carrier] || carrier.toUpperCase()}`,
  };
}

function staticNodeEndpoint(node, family) {
  const host = family === "v6" ? `[${node.address}]` : node.address;
  return `${node.scheme}://${host}:${node.port}`;
}

async function createStaticNodeLease(input, configuration, now) {
  const configured = Array.isArray(configuration?.[input.region])
    ? configuration[input.region]
    : [];
  const groups = new Map();
  const seen = new Set();
  for (const value of configured) {
    const node = normalizeStaticNode(value, input);
    if (!node) continue;
    const nodeKey = `${node.carrier}|${node.address}|${node.port}`;
    if (seen.has(nodeKey)) continue;
    seen.add(nodeKey);
    if (!groups.has(node.carrier)) {
      if (groups.size >= CARRIER_CODES.size) continue;
      groups.set(node.carrier, { carrier: node.carrier, label: node.label, candidates: [] });
    }
    const group = groups.get(node.carrier);
    if (group.candidates.length >= 4) continue;
    const endpoint = staticNodeEndpoint(node, input.family);
    group.candidates.push({
      id: await hashText(`static|${input.region}|${nodeKey}`, 32),
      address: node.address,
      port: node.port,
      activate: {
        method: "POST",
        url: `${endpoint}/activate`,
        key_prefix_bytes: 0,
        ready_delay_ms: node.readyDelay,
      },
      download: {
        method: "GET",
        url: `${endpoint}/download?key={key}&nonce={nonce}`,
      },
      upload: {
        method: "POST",
        url: `${endpoint}/upload?key={key}`,
        content_length: 900000000,
      },
      release: {
        method: "POST",
        url: `${endpoint}/release?key={key}`,
      },
    });
  }
  const targets = [...groups.values()].filter((group) => group.candidates.length > 0);
  if (targets.length === 0) return null;
  return {
    version: 1,
    lease_id: `static_${input.region}_${input.family}_${randomId()}`,
    issued_at: now,
    expires_at: now + 300,
    region: { code: input.region, name: REGION_NAMES[input.region] },
    family: input.family,
    duration_seconds: input.duration_seconds,
    target_mbps: input.target_mbps,
    modes: input.modes,
    targets,
  };
}

async function reserveLeaseAttempt(env, tokenHash, region, family, now) {
  const row = await env.DB.prepare(
    `INSERT INTO session_leases (token_hash, region, family, attempts, updated_at)
     VALUES (?, ?, ?, 1, ?)
     ON CONFLICT(token_hash, region, family) DO UPDATE SET
       attempts = attempts + 1,
       updated_at = excluded.updated_at
     RETURNING attempts`,
  ).bind(tokenHash, region, family, now).first();
  return Number(row?.attempts || 0);
}

function validCoreLease(lease, expected, now) {
  if (!lease || typeof lease !== "object" || Array.isArray(lease) || lease.version !== 1 ||
      !/^[A-Za-z0-9_-]{8,96}$/.test(String(lease.lease_id || "")) ||
      lease.region?.code !== expected.region || lease.family !== expected.family ||
      lease.duration_seconds !== expected.duration ||
      lease.target_mbps !== expected.targetMbps ||
      !Array.isArray(lease.modes) || lease.modes.join(",") !== expected.modes.join(",") ||
      !Number.isInteger(lease.issued_at) || !Number.isInteger(lease.expires_at) ||
      lease.issued_at <= 0 || lease.expires_at <= lease.issued_at ||
      lease.expires_at - lease.issued_at > 900 ||
      lease.issued_at > now + 60 || lease.expires_at <= now || lease.expires_at > now + 900 ||
      typeof lease.region?.name !== "string" || !lease.region.name.trim() ||
      lease.region.name.length > 24 ||
      !Array.isArray(lease.targets) || lease.targets.length < 1 || lease.targets.length > CARRIER_CODES.size) return false;
  const carriers = new Set();
  for (const target of lease.targets) {
    if (typeof target?.carrier !== "string" ||
        !CARRIER_CODES.has(target.carrier) ||
        carriers.has(target.carrier) || typeof target?.label !== "string" ||
        !target.label.trim() || target.label.length > 32 ||
        !Array.isArray(target.candidates) || target.candidates.length < 1 || target.candidates.length > 4) return false;
    carriers.add(target.carrier);
    const candidateIDs = new Set();
    for (const candidate of target.candidates) {
      const address = String(candidate?.address || "");
      const ipv4Parts = address.split(".");
      const addressMatchesFamily = expected.family === "v4"
        ? ipv4Parts.length === 4 && ipv4Parts.every((part) =>
          /^\d{1,3}$/.test(part) && Number(part) <= 255)
        : address.includes(":") && /^[0-9a-f:.]+$/i.test(address);
      if (!/^[a-f0-9]{32}$/.test(String(candidate?.id || "")) ||
          candidateIDs.has(candidate.id) ||
          typeof candidate.address !== "string" || candidate.address.length > 64 ||
          !addressMatchesFamily ||
          !Number.isInteger(candidate.port) || candidate.port < 1 || candidate.port > 65535 ||
          !candidate.activate || !candidate.download || !candidate.upload || !candidate.release ||
          (candidate.activate.ready_delay_ms != null &&
            (!Number.isInteger(candidate.activate.ready_delay_ms) ||
              candidate.activate.ready_delay_ms < 0 || candidate.activate.ready_delay_ms > 10000))) return false;
      candidateIDs.add(candidate.id);
    }
  }
  return true;
}

async function requestNodeLease(request, env) {
  const started = Date.now();
  const currentRequestID = requestID(request);
  const now = Math.floor(Date.now() / 1000);
  const authorized = await authorizeSession(request, env, now);
  if (authorized.error) return textResponse(`${authorized.error}\n`, authorized.status, { "cache-control": "no-store" });
  const params = await parseSmallForm(request);
  if (!params) return textResponse("Invalid lease request\n", 400, { "cache-control": "no-store" });
  const region = String(params.get("region") || "");
  const family = String(params.get("family") || "");
  const preparation = String(params.get("preparation") || "");
  const allowedRegions = String(authorized.session.regions || "").split(",");
  const familyAllowed = family === "v4" || (family === "v6" && authorized.session.ip_mode !== "v4");
  if (!REGION_CODES.has(region) || !allowedRegions.includes(region) || !familyAllowed) {
    return textResponse("Lease is outside this session\n", 403, { "cache-control": "no-store" });
  }
  if (preparation && !/^lease_[A-Za-z0-9_-]{16,80}$/.test(preparation)) {
    return textResponse("Invalid lease preparation\n", 400, { "cache-control": "no-store" });
  }
  let attempts;
  if (preparation) {
    const prepared = await env.DB.prepare(
      `SELECT attempts, preparation_id FROM session_leases
       WHERE token_hash = ? AND region = ? AND family = ?`,
    ).bind(authorized.tokenHash, region, family).first();
    if (!prepared || prepared.preparation_id !== preparation) {
      return textResponse("Invalid lease preparation\n", 403, { "cache-control": "no-store" });
    }
    attempts = Number(prepared.attempts || 0);
  } else {
    attempts = await reserveLeaseAttempt(env, authorized.tokenHash, region, family, now);
  }
  if (attempts < 1 || attempts > 2) {
    logEvent(env, "warn", "lease.attempt_rejected", {
      request_id: currentRequestID,
      session_id: authorized.tokenHash.slice(0, 12),
      region,
      family,
      attempts,
      reason: "retry_limit",
      duration_ms: Date.now() - started,
    });
    return textResponse("Lease retry limit exceeded\n", 429, { "cache-control": "no-store" });
  }
  const nodeCoreConfigured = env.NODE_CORE && typeof env.NODE_CORE.fetch === "function";
  const staticConfiguration = parseStaticNodeConfiguration(env);
  if (!nodeCoreConfigured && !staticConfiguration) {
    return textResponse("Node service is not configured\n", 503, { "cache-control": "no-store" });
  }
  const input = {
    region,
    family,
    client_ip: clientAddress(request),
    client_asn: Number(request.cf?.asn || 0) || null,
    duration_seconds: Number(authorized.session.duration_seconds),
    target_mbps: Number(authorized.session.target_mbps),
    modes: requestedModes(authorized.session.mode),
  };
  const communityNodeID = String(authorized.session.community_node_id || "");
  if (communityNodeID) input.community_node_id = communityNodeID;
  if (preparation) input.preparation_id = preparation;
  let lease;
  if (nodeCoreConfigured) {
    let response;
    try {
      response = await env.NODE_CORE.fetch(new Request("https://node-core.internal/lease", {
        method: "POST",
        headers: {
          "content-type": "application/json",
          "x-node-core-secret": String(env.NODE_CORE_SECRET || ""),
          "x-request-id": currentRequestID,
        },
        body: JSON.stringify(input),
      }));
    } catch {
      return textResponse("Node service is unavailable\n", 502, { "cache-control": "no-store" });
    }
    const body = await readStreamLimited(response.body, 256 * 1024).catch(() => null);
    if (body == null) {
      return textResponse("Node service is unavailable\n", 502, { "cache-control": "no-store" });
    }
    try {
      lease = JSON.parse(decoder.decode(body));
    } catch {
      return textResponse("Node service returned invalid data\n", 502, { "cache-control": "no-store" });
    }
    if (response.status === 202) {
      const preparationID = String(lease?.preparation || "");
      if (lease?.status !== "preparing" || !/^lease_[A-Za-z0-9_-]{16,80}$/.test(preparationID) ||
          !Number.isInteger(lease.expires_at) || lease.expires_at <= now || lease.expires_at > now + 900) {
        return textResponse("Node service returned invalid preparation\n", 502, { "cache-control": "no-store" });
      }
      if (!preparation) {
        await env.DB.prepare(
          `UPDATE session_leases SET preparation_id = ?, updated_at = ?
           WHERE token_hash = ? AND region = ? AND family = ?`,
        ).bind(preparationID, now, authorized.tokenHash, region, family).run();
      } else if (preparationID !== preparation) {
        return textResponse("Node service returned mismatched preparation\n", 502, { "cache-control": "no-store" });
      }
      return jsonResponse(lease, 202, { "retry-after": "1" });
    }
    if (!response.ok) {
      const publicError = String(lease?.error || "");
      if (PUBLIC_LEASE_ERRORS.has(publicError)) {
        return jsonResponse({ error: publicError }, PUBLIC_LEASE_ERRORS.get(publicError));
      }
      return jsonResponse({ error: "node_service_unavailable" }, 502);
    }
  } else {
    lease = await createStaticNodeLease(input, staticConfiguration, now);
    if (!lease) {
      return textResponse("No matching static nodes are available\n", 503, {
        "cache-control": "no-store",
      });
    }
  }
  if (!validCoreLease(lease, {
    region,
    family,
    duration: input.duration_seconds,
    targetMbps: input.target_mbps,
    modes: input.modes,
  }, now)) {
    return textResponse("Node service returned invalid data\n", 502, { "cache-control": "no-store" });
  }
  logEvent(env, "info", "lease.delivered", {
    request_id: currentRequestID,
    session_id: authorized.tokenHash.slice(0, 12),
    lease_id: lease.lease_id,
    region,
    family,
    attempt: attempts,
    target_mbps: input.target_mbps,
    target_groups: lease.targets.length,
    candidate_count: lease.targets.reduce((sum, target) => sum + target.candidates.length, 0),
    source: nodeCoreConfigured ? "node_core" : "static",
    duration_ms: Date.now() - started,
  });
  return new Response(JSON.stringify(lease), {
    status: 200,
    headers: commonHeaders({
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    }),
  });
}

async function enforceDailyLimit(request, env, now) {
  const limit = numberSetting(env.DAILY_RESULT_LIMIT, DEFAULT_DAILY_LIMIT, 1, 10000);
  const day = new Date(now * 1000).toISOString().slice(0, 10);
  const hash = await clientHash(request, env);
  const row = await env.DB.prepare(
    `INSERT INTO rate_limits (day, client_hash, count, updated_at)
     VALUES (?, ?, 1, ?)
     ON CONFLICT(day, client_hash) DO UPDATE SET
       count = count + 1,
       updated_at = excluded.updated_at
     RETURNING count`,
  ).bind(day, hash, now).first();
  return Number(row?.count || 0) <= limit;
}

async function insertReport(env, report, now, expiresAt) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const id = randomId();
    try {
      const result = await env.DB.prepare(
        `INSERT INTO reports (
          id, created_at, expires_at, tested_at, regions, mode, ip_mode,
          source_ip_masked,
          speed_url, speed_text, speed_data, duration_seconds, target_mbps,
          traffic_rx_bytes, traffic_tx_bytes,
          nq_url, nq_tested_at, nq_time_source, time_gap_seconds, nq_identity_reason,
          bind_status, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      ).bind(
        id,
        now,
        expiresAt,
        report.testedAt,
        report.regions,
        report.mode,
        report.ipMode,
        report.sourceIPMasked,
        report.speedUrl,
        report.speedText,
        report.speedData,
        report.durationSeconds,
        report.targetMbps,
        report.trafficRxBytes,
        report.trafficTxBytes,
        report.nqUrl,
        report.nqTestedAt,
        report.nqTimeSource,
        report.timeGapSeconds,
        report.nqIdentityReason,
        report.bindStatus,
        report.version,
      ).run();
      if (result?.success !== false) return id;
    } catch (error) {
      if (!String(error).toLowerCase().includes("unique")) throw error;
    }
  }
  throw new Error("Unable to allocate report id");
}

function usageDay(now) {
  return new Date((now + 8 * 3600) * 1000).toISOString().slice(0, 10);
}

async function incrementUsageCounters(env, now) {
  const statement = (key) => env.DB.prepare(
    `INSERT INTO usage_counters (key, count, updated_at)
     VALUES (?, 1, ?)
     ON CONFLICT(key) DO UPDATE SET
       count = count + 1,
       updated_at = excluded.updated_at`,
  ).bind(key, now);
  try {
    await env.DB.batch([
      statement("total"),
      statement(`day:${usageDay(now)}`),
    ]);
  } catch {
    // Report creation must still succeed if optional public counters are unavailable.
  }
}

async function readUsageCounters(env, now) {
  const dailyKey = `day:${usageDay(now)}`;
  try {
    const response = await env.DB.prepare(
      "SELECT key, count FROM usage_counters WHERE key IN (?, ?)",
    ).bind("total", dailyKey).all();
    const counts = new Map((response?.results || []).map((row) => [row.key, Number(row.count)]));
    return {
      today: Number.isSafeInteger(counts.get(dailyKey)) ? counts.get(dailyKey) : 0,
      total: Number.isSafeInteger(counts.get("total")) ? counts.get("total") : 0,
    };
  } catch {
    return { today: null, total: null };
  }
}

function snapshotObjectKey(id) {
  return `nodequality/${id}.json.gz`;
}

async function gzipText(value) {
  const stream = new Blob([value], { type: "application/json" })
    .stream()
    .pipeThrough(new CompressionStream("gzip"));
  return new Response(stream).arrayBuffer();
}

async function storeNodeQualitySnapshot(env, id, snapshotText, expiresAt) {
  if (!snapshotText) return "missing";
  if (!env.SNAPSHOTS) return "unconfigured";
  try {
    const compressed = await gzipText(snapshotText);
    await env.SNAPSHOTS.put(snapshotObjectKey(id), compressed, {
      httpMetadata: {
        contentType: "application/json; charset=utf-8",
        contentEncoding: "gzip",
      },
      customMetadata: {
        reportId: id,
        expiresAt: String(expiresAt),
        schema: "2",
      },
    });
    return "stored";
  } catch {
    return "failed";
  }
}

function sessionMatchesReport(session, report) {
  if (session.mode !== report.mode || session.ip_mode !== report.ipMode ||
      Number(session.duration_seconds) !== Number(report.durationSeconds) ||
      Number(session.target_mbps) !== Number(report.targetMbps)) return false;
  const allowedRegions = new Set(String(session.regions || "").split(","));
  const expectedModes = requestedModes(session.mode).join(",");
  for (const speedReport of report.speedReports) {
    if (!allowedRegions.has(speedReport.region.code)) return false;
    if (session.ip_mode === "v4" && speedReport.family !== "v4") return false;
    if (speedReport.modes.join(",") !== expectedModes ||
        Number(speedReport.duration_seconds) !== Number(session.duration_seconds) ||
        Number(speedReport.target_mbps) !== Number(session.target_mbps)) return false;
  }
  return true;
}

function healthMeasurements(speedReports) {
  const measurements = [];
  for (const report of speedReports) {
    for (const result of report.results) {
      if (!result.node_id) continue;
      const modes = [result.single, result.multi].filter(Boolean);
      measurements.push({
        lease_id: report.lease_id,
        node_id: result.node_id,
        status: result.status,
        latency_ms: result.latency_ms,
        target_mbps: report.target_mbps,
        download_mbps: modes.length ? Math.max(...modes.map((mode) => mode.download_mbps)) : null,
        upload_mbps: modes.length ? Math.max(...modes.map((mode) => mode.upload_mbps)) : null,
        download_bytes: modes.reduce((sum, mode) => sum + Number(mode.download_bytes || 0), 0),
        upload_bytes: modes.reduce((sum, mode) => sum + Number(mode.upload_bytes || 0), 0),
      });
    }
  }
  return measurements;
}

async function forwardHealthFeedback(env, speedReports, currentRequestID = "") {
  if (!env.NODE_CORE || typeof env.NODE_CORE.fetch !== "function") return;
  const measurements = healthMeasurements(speedReports);
  if (measurements.length === 0) return;
  try {
    await env.NODE_CORE.fetch(new Request("https://node-core.internal/feedback", {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-node-core-secret": String(env.NODE_CORE_SECRET || ""),
        "x-request-id": currentRequestID,
      },
      body: JSON.stringify({ measurements }),
    }));
  } catch {
    // Report creation must not fail when optional health feedback is unavailable.
  }
}

async function createReport(request, env) {
  const started = Date.now();
  const currentRequestID = requestID(request);
  if (!env.DB) {
    return textResponse("Report storage is not configured\n", 503, {
      "cache-control": "no-store",
    });
  }
  const payload = await parseResultPayload(request);
  if (payload.error) {
    return textResponse(`${payload.error}\n`, payload.status, {
      "cache-control": "no-store",
    });
  }
  const parsed = validateResultInput(payload.params);
  if (parsed.error) {
    return textResponse(`${parsed.error}\n`, 400, { "cache-control": "no-store" });
  }

  if (!nodeQualityBindingEnabled(env) && parsed.value.bindStatus !== "standalone") {
    parsed.value.nqUrl = "";
    parsed.value.nqTestedAt = null;
    parsed.value.nqTimeSource = "";
    parsed.value.timeGapSeconds = null;
    parsed.value.nqIdentityReason = "";
    parsed.value.bindStatus = "standalone";
    payload.snapshotText = "";
  }

  const sessionAuthorization = await authorizeSession(request, env);
  if (sessionAuthorization.error) {
    return textResponse(`${sessionAuthorization.error}\n`, sessionAuthorization.status, {
      "cache-control": "no-store",
    });
  }
  if (!sessionMatchesReport(sessionAuthorization.session, parsed.value)) {
    return textResponse("Result is outside this session\n", 403, {
      "cache-control": "no-store",
    });
  }
  parsed.value.sourceIPMasked = maskedReportAddress(clientAddress(request));
  let snapshot = { value: null, text: "" };
  if (VERIFIED_STATUSES.has(parsed.value.bindStatus) && payload.snapshotText) {
    snapshot = validateNodeQualitySnapshot(payload.snapshotText);
    if (snapshot.error) {
      return textResponse(`${snapshot.error}\n`, 400, {
        "cache-control": "no-store",
      });
    }
  }

  const now = Math.floor(Date.now() / 1000);
  try {
    if (!(await enforceDailyLimit(request, env, now))) {
      return textResponse("Daily report limit exceeded\n", 429, {
        "cache-control": "no-store",
        "retry-after": "3600",
      });
    }
    const ttlDays = numberSetting(env.RESULT_TTL_DAYS, DEFAULT_TTL_DAYS, 1, 3650);
    const expiresAt = now + ttlDays * 86400;
    const id = await insertReport(env, parsed.value, now, expiresAt);
    await incrementUsageCounters(env, now);
    const snapshotStatus = await storeNodeQualitySnapshot(
      env,
      id,
      snapshot.text,
      expiresAt,
    );
    await env.DB.prepare(
      "UPDATE sessions SET completed_at = ? WHERE token_hash = ?",
    ).bind(now, sessionAuthorization.tokenHash).run();
    await forwardHealthFeedback(env, parsed.value.speedReports, currentRequestID);
    const reportUrl = `${new URL(request.url).origin}/r/${id}`;
    logEvent(env, "info", "report.created", {
      request_id: currentRequestID,
      session_id: sessionAuthorization.tokenHash.slice(0, 12),
      report_id: id,
      regions: parsed.value.speedReports.map((report) => report.region.code),
      report_count: parsed.value.speedReports.length,
      measurement_count: parsed.value.speedReports.reduce((sum, report) => sum + report.results.length, 0),
      target_mbps: parsed.value.targetMbps,
      bind_status: parsed.value.bindStatus,
      snapshot_status: snapshotStatus,
      duration_ms: Date.now() - started,
    });
    return textResponse(`${reportUrl}\n`, 201, {
      "cache-control": "no-store",
      "x-snapshot-store": snapshotStatus,
    });
  } catch (error) {
    logEvent(env, "error", "report.create_failed", {
      request_id: currentRequestID,
      error_type: error?.name || "Error",
      duration_ms: Date.now() - started,
    });
    return textResponse("Unable to save report\n", 500, { "cache-control": "no-store" });
  }
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[character]);
}

function formatTime(epoch) {
  if (!epoch) return "无法确认";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(new Date(Number(epoch) * 1000)) + "（北京时间）";
}

function formatGap(seconds) {
  if (seconds == null || !Number.isFinite(Number(seconds))) return "无法确认";
  const minutes = Math.round(Number(seconds) / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟`;
  return `${Math.floor(minutes / 1440)} 天 ${Math.floor((minutes % 1440) / 60)} 小时`;
}

function formatBytes(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return "";
  if (value >= 1_000_000_000_000) return `${(value / 1_000_000_000_000).toFixed(2)} TB`;
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)} GB`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)} MB`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)} KB`;
  return `${Math.round(value)} B`;
}

const ANSI_BASE_COLORS = [
  "#050404", "#bd0013", "#4ab118", "#e7741e",
  "#0f4ac6", "#665993", "#70a598", "#f8dcc0",
];
const ANSI_BRIGHT_COLORS = [
  "#4e7cbf", "#fc5f5a", "#9eff6e", "#efc11a",
  "#1997c6", "#9b5953", "#c8faf4", "#f6f5fb",
];

function ansi256Color(value) {
  const number = Number(value);
  if (!Number.isInteger(number) || number < 0 || number > 255) return null;
  if (number < 8) return ANSI_BASE_COLORS[number];
  if (number < 16) return ANSI_BRIGHT_COLORS[number - 8];
  if (number >= 232) {
    const level = 8 + (number - 232) * 10;
    return `rgb(${level},${level},${level})`;
  }
  const cube = number - 16;
  const levels = [0, 95, 135, 175, 215, 255];
  return `rgb(${levels[Math.floor(cube / 36)]},${levels[Math.floor(cube / 6) % 6]},${levels[cube % 6]})`;
}

function ansiStateStyle(state) {
  let foreground = state.foreground;
  let background = state.background;
  if (state.inverse) {
    [foreground, background] = [background || "#1f1d45", foreground || "#f8dcc0"];
  }
  const styles = [];
  if (foreground) styles.push(`color:${foreground}`);
  if (background) styles.push(`background-color:${background}`);
  if (state.bold) styles.push("font-weight:700");
  if (state.dim) styles.push("opacity:.72");
  if (state.italic) styles.push("font-style:italic");
  const decorations = [];
  if (state.underline) decorations.push("underline");
  if (state.strike) decorations.push("line-through");
  if (decorations.length) styles.push(`text-decoration:${decorations.join(" ")}`);
  if (state.hidden) styles.push("visibility:hidden");
  return styles.join(";");
}

export function ansiToHtml(value) {
  const input = sanitizeAnsiTerminalText(value);
  const state = {
    foreground: null,
    background: null,
    bold: false,
    dim: false,
    italic: false,
    underline: false,
    inverse: false,
    hidden: false,
    strike: false,
  };
  const reset = () => {
    state.foreground = null;
    state.background = null;
    state.bold = false;
    state.dim = false;
    state.italic = false;
    state.underline = false;
    state.inverse = false;
    state.hidden = false;
    state.strike = false;
  };
  const renderChunk = (text) => {
    if (!text) return "";
    const escaped = escapeHtml(text);
    const style = ansiStateStyle(state);
    return style ? `<span style="${style}">${escaped}</span>` : escaped;
  };
  let output = "";
  let offset = 0;
  const expression = /\x1b\[([0-9;]*)m/g;
  for (let match = expression.exec(input); match; match = expression.exec(input)) {
    output += renderChunk(input.slice(offset, match.index));
    const codes = match[1] === "" ? [0] : match[1].split(";").map(Number);
    for (let index = 0; index < codes.length; index += 1) {
      const code = Number.isFinite(codes[index]) ? codes[index] : 0;
      if (code === 0) reset();
      else if (code === 1) state.bold = true;
      else if (code === 2) state.dim = true;
      else if (code === 3) state.italic = true;
      else if (code === 4) state.underline = true;
      else if (code === 7) state.inverse = true;
      else if (code === 8) state.hidden = true;
      else if (code === 9) state.strike = true;
      else if (code === 22) { state.bold = false; state.dim = false; }
      else if (code === 23) state.italic = false;
      else if (code === 24) state.underline = false;
      else if (code === 27) state.inverse = false;
      else if (code === 28) state.hidden = false;
      else if (code === 29) state.strike = false;
      else if (code >= 30 && code <= 37) state.foreground = ANSI_BASE_COLORS[code - 30];
      else if (code >= 90 && code <= 97) state.foreground = ANSI_BRIGHT_COLORS[code - 90];
      else if (code >= 40 && code <= 47) state.background = ANSI_BASE_COLORS[code - 40];
      else if (code >= 100 && code <= 107) state.background = ANSI_BRIGHT_COLORS[code - 100];
      else if (code === 39) state.foreground = null;
      else if (code === 49) state.background = null;
      else if ((code === 38 || code === 48) && codes[index + 1] === 5) {
        const color = ansi256Color(codes[index + 2]);
        if (code === 38) state.foreground = color;
        else state.background = color;
        index += 2;
      } else if ((code === 38 || code === 48) && codes[index + 1] === 2) {
        const rgb = codes.slice(index + 2, index + 5);
        if (rgb.length === 3 && rgb.every((part) => Number.isInteger(part) && part >= 0 && part <= 255)) {
          const color = `rgb(${rgb.join(",")})`;
          if (code === 38) state.foreground = color;
          else state.background = color;
        }
        index += 4;
      }
    }
    offset = expression.lastIndex;
  }
  output += renderChunk(input.slice(offset));
  return output;
}

function storedSpeedReports(value) {
  if (!value) return [];
  try {
    const reports = JSON.parse(value);
    return Array.isArray(reports) ? reports : [];
  } catch {
    return [];
  }
}

function formatMbps(value, targetMbps = null) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) return "-";
  if (TARGET_SPEEDS.has(targetMbps) && number >= targetMbps * 0.98) {
    return `${targetMbps}Mbps ✓`;
  }
  return `${number.toFixed(2)}Mbps`;
}

function displayWidth(value) {
  let width = 0;
  for (const character of String(value ?? "")) {
    const codePoint = character.codePointAt(0);
    width += character === "✓" ? 1 : codePoint >= 0x1100 ? 2 : 1;
  }
  return width;
}

function padDisplay(value, width, align = "left") {
  const text = String(value ?? "");
  const padding = Math.max(0, width - displayWidth(text));
  return align === "right" ? `${" ".repeat(padding)}${text}` : `${text}${" ".repeat(padding)}`;
}

function centerIndent(value, width = 80) {
  const text = String(value ?? "");
  const padding = Math.max(0, width - displayWidth(text));
  return " ".repeat(Math.floor(padding / 2));
}

function centerDisplay(value, width = 80) {
  const text = String(value ?? "");
  return `${centerIndent(text, width)}${text}`;
}

function ansiText(value, code, colored) {
  return colored ? `\x1b[${code}m${value}\x1b[0m` : value;
}

function latencyAnsiCode(value, failed) {
  const latency = Number(value);
  if (failed || !Number.isFinite(latency)) return "1;91";
  if (latency <= 100) return "92";
  if (latency <= 200) return "38;2;255;165;0";
  return "1;91";
}

function speedAnsiCode(value, targetMbps, failed) {
  const speed = Number(value);
  if (failed || !Number.isFinite(speed) || !targetMbps) return "1;91";
  const ratio = speed / targetMbps;
  if (ratio >= 0.8) return "1;92";
  if (ratio >= 0.3) return "38;2;255;165;0";
  return "1;91";
}

function renderStructuredSpeedText(value, options = {}) {
  const reports = storedSpeedReports(value);
  if (reports.length === 0) return "";
  const colored = options.colored === true;
  const groups = [];
  const groupByRegion = new Map();
  for (const report of reports) {
    const code = String(report.region?.code || "");
    const name = String(report.region?.name || "未知地区");
    const key = `${code}\u0000${name}`;
    let group = groupByRegion.get(key);
    if (!group) {
      group = { name, reports: [] };
      groupByRegion.set(key, group);
      groups.push(group);
    }
    group.reports.push(report);
  }
  return groups.map((group) => {
    const lines = [];
    lines.push(ansiText(group.name, "1;96", colored));
    for (const [reportIndex, report] of group.reports.entries()) {
      if (reportIndex > 0) lines.push("");
      const rows = Array.isArray(report.results) ? report.results : [];
      const familyValue = String(report.family || "").toLowerCase();
      const family = familyValue === "v4" ? "IPv4" : familyValue === "v6"
        ? "IPv6" : String(report.family || "").toUpperCase() || "IP";
      const targetMbps = TARGET_SPEEDS.has(Number(report.target_mbps))
        ? Number(report.target_mbps)
        : null;
      const headings = [
        ansiText(padDisplay(family, 8, "right"), "1;96", colored),
        ansiText(padDisplay("延迟", 10, "right"), "1;96", colored),
        ansiText(padDisplay("单线程上传", 18, "right"), "1;96", colored),
        ansiText(padDisplay("单线程下载", 18, "right"), "1;96", colored),
      ];
      lines.push(headings.join("  "));
      for (const result of rows) {
        const failed = result.status !== "ok";
        const unavailable = failed && result.error === "没有可连接的候选节点";
        const latencyNumber = Number(result.latency_ms);
        const latency = result.latency_ms != null && Number.isFinite(latencyNumber)
          ? `${Math.round(latencyNumber)}ms`
          : "-";
        const uploadMbps = Number(result.single?.upload_mbps);
        const downloadMbps = Number(result.single?.download_mbps);
        const uploadValue = unavailable ? "-"
          : failed && !(uploadMbps > 0) ? "失败" : formatMbps(uploadMbps, targetMbps);
        const downloadValue = unavailable ? "-"
          : failed && !(downloadMbps > 0) ? "失败" : formatMbps(downloadMbps, targetMbps);
        const carrier = CARRIER_NAMES[result.carrier] || result.label || result.carrier || "-";
        const columns = [
          ansiText(padDisplay(carrier, 8, "right"), "36", colored),
          ansiText(padDisplay(latency, 10, "right"), latencyAnsiCode(latencyNumber, failed), colored),
          ansiText(padDisplay(uploadValue, 18, "right"), speedAnsiCode(uploadMbps, targetMbps, failed), colored),
          ansiText(padDisplay(downloadValue, 18, "right"), speedAnsiCode(downloadMbps, targetMbps, failed), colored),
        ];
        lines.push(columns.join("  "));
        if (failed && result.error && !unavailable) {
          lines.push(ansiText(`  [失败] ${carrier}：${result.error}`, "1;91", colored));
        }
      }
    }
    return lines.join("\n");
  }).join("\n\n");
}

function trafficReportText(
  hasTraffic,
  trafficRxBytes,
  trafficTxBytes,
  options = {},
) {
  if (!hasTraffic) return "";
  const colored = options.colored === true;
  return `${ansiText("实际流量：", "1;37", colored)}` +
    `${ansiText("下载流量 ", "36", colored)}${ansiText(formatBytes(trafficRxBytes), "92", colored)} / ` +
    `${ansiText("上传流量 ", "36", colored)}${ansiText(formatBytes(trafficTxBytes), "92", colored)} / ` +
    `${ansiText("合计流量 ", "36", colored)}${ansiText(formatBytes(trafficRxBytes + trafficTxBytes), "1;92", colored)}`;
}

function markdownCodeBlock(value, language = "text") {
  const text = String(value || "");
  let longestRun = 0;
  for (const match of text.matchAll(/`+/g)) longestRun = Math.max(longestRun, match[0].length);
  const fence = "`".repeat(Math.max(3, longestRun + 1));
  const safeLanguage = language === "ansi" ? "ansi" : "text";
  return `${fence}${safeLanguage}\n${text}\n${fence}`;
}

function nodeQualityCopyPages(snapshot) {
  const pages = Array.isArray(snapshot?.pages) ? snapshot.pages : [];
  const normalized = pages.map((page) => {
    const title = stripUnsafeTerminalText(page?.title) || "NodeQuality";
    if (page?.format === "ansi") {
      const ansi = sanitizeAnsiTerminalText(page.content).trim();
      if (!ansi) return null;
      return {
        id: String(page.id || ""),
        title,
        format: "ansi",
        ansi,
        text: stripUnsafeTerminalText(ansi),
        url: "",
      };
    }
    if (page?.format === "image") {
      const url = normalizeHttpsUrl(page.image_url, 2048);
      if (!url) return null;
      return { id: String(page.id || ""), title, format: "image", ansi: "", text: "", url };
    }
    return null;
  }).filter(Boolean);
  const allPage = normalized.find((page) => page.id === "all" && page.format === "ansi");
  let exportPages = normalized.filter((page) => page.id !== "all");
  if (exportPages.length === 0 && allPage) exportPages = [allPage];
  const imageLines = exportPages
    .filter((page) => page.format === "image")
    .map((page) => `${page.title}：${page.url}`);
  const plainText = allPage
    ? [allPage.text, ...imageLines].filter(Boolean).join("\n\n")
    : exportPages.map((page) => page.format === "ansi"
      ? `[${page.title}]\n${page.text}`
      : `${page.title}：${page.url}`).join("\n\n");
  return { plainText, exportPages };
}

function markdownImage(page, prefix = "NodeQuality") {
  const title = page.title.replace(/[\[\]]/g, "");
  return `![${prefix} ${title}](${page.url})`;
}

function speedReportHeaderText(report, options = {}) {
  const colored = options.colored === true;
  const targetMbps = Number(report.target_mbps);
  const maskedIP = stripUnsafeTerminalText(report.source_ip_masked || "") || "IP 段未知";
  const reportVersion = boundedText(report.version, 32);
  const projectUrl = normalizeHttpsUrl(options.projectUrl, 2048);
  const reportUrl = normalizeHttpsUrl(options.reportUrl, 2048);
  let runUrl = "";
  if (reportUrl) {
    try {
      runUrl = `${new URL(reportUrl).origin}/run`;
    } catch {}
  }
  const centeredStyled = (parts) => {
    const plain = parts.map((part) => part.text).join("");
    return centerIndent(plain) + parts.map((part) => ansiText(part.text, part.code, colored)).join("");
  };
  const lines = [ansiText("#".repeat(80), "36", colored)];
  lines.push(centeredStyled([
    { text: "SpeedQuality 测速报告：", code: "1;37" },
    { text: maskedIP, code: "1;96" },
  ]));
  if (projectUrl) lines.push(centeredStyled([
    { text: projectUrl, code: "4;36" },
  ]));
  if (runUrl) lines.push(ansiText(centerDisplay(`bash <(curl -fsSL ${runUrl})`), "36", colored));
  const reportDetails = [
    `报告时间：${formatTime(report.tested_at)}`,
    reportVersion ? `脚本版本：${stripUnsafeTerminalText(reportVersion)}` : "",
  ].filter(Boolean).join("  ");
  lines.push(ansiText(centerDisplay(reportDetails), "37", colored));
  const configuration = [
    "单线程",
    TARGET_SPEEDS.has(targetMbps) ? `${targetMbps} Mbps 档位` : "",
  ].filter(Boolean).join(" / ");
  lines.push(ansiText(centerDisplay(`测速配置：${configuration}`), "37", colored));
  lines.push(ansiText("#".repeat(80), "36", colored));
  return lines.join("\n");
}

function reportLinkLines(nodeQualityUrl, reportUrl) {
  const links = [];
  if (nodeQualityUrl) links.push(`[NodeQuality链接](${nodeQualityUrl})`);
  if (reportUrl) links.push(`[SpeedQuality链接](${reportUrl})`);
  return links.join("\n");
}

export function formatReportCopies(report, options = {}) {
  const hasNodeQuality = Boolean(report.nq_url);
  const trafficRxBytes = Number(report.traffic_rx_bytes);
  const trafficTxBytes = Number(report.traffic_tx_bytes);
  const hasTraffic = report.traffic_rx_bytes != null && report.traffic_rx_bytes !== "" &&
    report.traffic_tx_bytes != null && report.traffic_tx_bytes !== "" &&
    Number.isSafeInteger(trafficRxBytes) && trafficRxBytes >= 0 &&
    Number.isSafeInteger(trafficTxBytes) && trafficTxBytes >= 0 &&
    Number.isSafeInteger(trafficRxBytes + trafficTxBytes);
  const reportUrl = normalizeHttpsUrl(options.reportUrl, 2048) ||
    stripUnsafeTerminalText(options.reportPath || "").split("\n", 1)[0];
  const nodeQualityUrl = hasNodeQuality ? normalizeHttpsUrl(report.nq_url, 2048) : "";
  const structuredAnsi = renderStructuredSpeedText(report.speed_data, { colored: true });
  const speedAnsi = sanitizeAnsiTerminalText(structuredAnsi || report.speed_text).trim() ||
    "测速内容暂不可用。";
  const speedText = stripUnsafeTerminalText(speedAnsi);
  const trafficText = trafficReportText(hasTraffic, trafficRxBytes, trafficTxBytes);
  const trafficAnsiText = trafficReportText(
    hasTraffic,
    trafficRxBytes,
    trafficTxBytes,
    { colored: true },
  );
  const headerText = speedReportHeaderText(report, {
    reportUrl,
    projectUrl: options.projectUrl,
  });
  const headerAnsi = speedReportHeaderText(report, {
    colored: true,
    reportUrl,
    projectUrl: options.projectUrl,
  });
  const speedPlainMaterial = [headerText, speedText, trafficText].filter(Boolean).join("\n\n");
  const speedAnsiMaterial = [headerAnsi, speedAnsi, trafficAnsiText].filter(Boolean).join("\n\n");
  const nodeQuality = nodeQualityCopyPages(options.snapshot);
  const links = reportLinkLines(nodeQualityUrl, reportUrl);

  const plain = [];
  if (hasNodeQuality && nodeQuality.plainText) plain.push(nodeQuality.plainText);
  plain.push(speedPlainMaterial);
  if (links) plain.push(links);

  const nodeSeek = [":::: tabs"];
  if (hasNodeQuality) {
    for (const page of nodeQuality.exportPages) {
      const body = page.format === "image"
        ? markdownImage(page)
        : markdownCodeBlock(page.ansi, "ansi");
      nodeSeek.push(`::: tab-item ${page.title}\n${body}\n:::`);
    }
  }
  nodeSeek.push(`::: tab-item 速度质量\n${markdownCodeBlock(speedAnsiMaterial, "ansi")}\n:::`);
  nodeSeek.push("::::");
  if (links) nodeSeek.push(links);

  const markdown = [];
  if (hasNodeQuality) {
    for (const page of nodeQuality.exportPages) {
      const body = page.format === "image"
        ? markdownImage(page)
        : markdownCodeBlock(page.text);
      markdown.push(`# ${page.title}\n${body}`);
    }
  }
  const speedUrl = normalizeHttpsUrl(report.speed_url, 2048);
  const speedMarkdown = speedUrl
    ? `![SpeedQuality 测速结果](${speedUrl})`
    : markdownCodeBlock(speedPlainMaterial);
  markdown.push(`# 速度质量\n${speedMarkdown}`);
  if (links) markdown.push(links);

  return {
    text: plain.join("\n\n"),
    nodeseek: nodeSeek.join("\n\n"),
    markdown: markdown.join("\n\n"),
  };
}

function usageCount(value) {
  return Number.isSafeInteger(value) && value >= 0 ? value.toLocaleString("zh-CN") : "--";
}

export function renderReport(report, options = {}) {
  const hasNodeQuality = Boolean(report.nq_url);
  const trafficRxBytes = Number(report.traffic_rx_bytes);
  const trafficTxBytes = Number(report.traffic_tx_bytes);
  const hasTraffic = report.traffic_rx_bytes != null && report.traffic_rx_bytes !== "" &&
    report.traffic_tx_bytes != null && report.traffic_tx_bytes !== "" &&
    Number.isSafeInteger(trafficRxBytes) && trafficRxBytes >= 0 &&
    Number.isSafeInteger(trafficTxBytes) && trafficTxBytes >= 0 &&
    Number.isSafeInteger(trafficRxBytes + trafficTxBytes);
  const snapshotPages = Array.isArray(options.snapshot?.pages) ? options.snapshot.pages : [];
  const tabs = [];
  if (hasNodeQuality && snapshotPages.length) {
    for (const page of snapshotPages) {
      tabs.push({ key: `nq-${page.id}`, label: page.title, page });
    }
  } else if (hasNodeQuality) {
    tabs.push({ key: "nodequality", label: "NodeQuality", page: null });
  }
  tabs.push({ key: "sq", label: "速度质量", page: null });

  let requestedTab = String(options.tab || "");
  if (["overview", "nodequality"].includes(requestedTab)) requestedTab = tabs[0].key;
  if (requestedTab === "speed") requestedTab = "sq";
  const activeTab = tabs.some((tab) => tab.key === requestedTab) ? requestedTab : tabs[0].key;
  const activeEntry = tabs.find((tab) => tab.key === activeTab);
  const isSpeedPage = activeTab === "sq";
  const isNodeQualityPage = hasNodeQuality && !isSpeedPage;
  const reportPath = options.reportPath || `/r/${report.id || ""}`;
  const tabHref = (tab, index) => {
    if (options.tabLinks?.[tab.key]) return options.tabLinks[tab.key];
    return index === 0 ? reportPath : `${reportPath}?tab=${tab.key}`;
  };
  const speedTabIndex = tabs.findIndex((tab) => tab.key === "sq");
  const speedTabHref = tabHref(tabs[speedTabIndex], speedTabIndex);

  const identityMatch = report.nq_identity_reason === "full_ip"
    ? "NodeQuality 与本次测速的完整 IP 一致"
    : report.nq_identity_reason === "masked_ip_and_asn"
      ? "NodeQuality 的脱敏 IP 网段与本次测速匹配，且 ASN 一致"
      : "NodeQuality 与本次测速的服务器身份信息匹配";
  let verification = "服务器身份与时间均已校验";
  let noticeClass = "ok";
  let notice = "";
  if (report.bind_status === "verified") {
    notice = `${identityMatch}，检测时间相差 ${formatGap(report.time_gap_seconds)}。`;
  } else if (report.bind_status === "verified_stale") {
    verification = "服务器身份已校验，检测时间差较大";
    noticeClass = "warning";
    notice = `${identityMatch}；两次检测相差 ${formatGap(report.time_gap_seconds)}，超过 60 分钟。`;
  } else if (report.bind_status === "verified_time_unknown") {
    verification = "服务器身份已校验，时间无法确认";
    noticeClass = "warning";
    notice = `${identityMatch}；无法确认两次检测的时间差，请结合原报告时间判断。`;
  } else if (report.bind_status === "mismatch") {
    verification = "服务器身份校验未通过";
    noticeClass = "danger";
    notice = "NodeQuality 与本次测速的服务器身份不一致，已拒绝绑定；本页仅展示 SpeedQuality 结果。";
  } else if (report.bind_status === "unverified") {
    verification = "NodeQuality 报告无法校验";
    noticeClass = "danger";
    notice = "无法读取、解析或取得足够的服务器身份信息，已拒绝绑定；本页仅展示 SpeedQuality 结果。";
  }

  const tabsHtml = tabs.map((tab, index) => `
      <a href="${escapeHtml(tabHref(tab, index))}"${tab.key === activeTab ? ' aria-current="page"' : ""}>${escapeHtml(tab.label)}</a>`).join("");
  const showTabs = tabs.length > 1;

  const copyFormats = formatReportCopies(report, {
    snapshot: options.snapshot,
    reportUrl: options.reportUrl,
    reportPath,
    projectUrl: options.promotion?.projectUrl,
  });
  const copyActions = (position) => `
      <div class="copy-actions copy-actions-${position}" role="group" aria-label="${position === "top" ? "上方" : "下方"}复制报告">
        <button type="button" data-copy-source="copy-report-text">复制文本</button>
        <button type="button" data-copy-source="copy-report-nodeseek" data-download="SpeedQualityResult.md">复制为NodeSeek格式</button>
        <button class="general-md" type="button" data-copy-source="copy-report-markdown" data-download="SpeedQualityResult.md">复制为通用Markdown</button>
      </div>`;
  const copyActionsTop = copyActions("top");
  const copyActionsBottom = copyActions("bottom");
  const copySources = `
      <textarea id="copy-report-text" hidden readonly>${escapeHtml(copyFormats.text)}</textarea>
      <textarea id="copy-report-nodeseek" hidden readonly>${escapeHtml(copyFormats.nodeseek)}</textarea>
      <textarea id="copy-report-markdown" hidden readonly>${escapeHtml(copyFormats.markdown)}</textarea>
      <p class="copy-status" role="status" aria-live="polite"></p>`;

  let content;
  if (isSpeedPage) {
    const structuredText = renderStructuredSpeedText(report.speed_data, { colored: true });
    const speedOutput = structuredText
      ? ansiToHtml(structuredText)
      : ansiToHtml(report.speed_text);
    const headerOutput = ansiToHtml(speedReportHeaderText(report, {
      colored: true,
      reportUrl: options.reportUrl,
      projectUrl: options.promotion?.projectUrl,
    }));
    const trafficOutput = ansiToHtml(trafficReportText(
      hasTraffic,
      trafficRxBytes,
      trafficTxBytes,
      { colored: true },
    ));
    const speedImage = report.speed_url ? `
        <a class="result-image" href="${escapeHtml(report.speed_url)}" rel="noreferrer">
          <img src="${escapeHtml(report.speed_url)}" alt="SpeedQuality 测速结果">
        </a>` : "";
    const terminalOutput = [headerOutput, speedOutput, trafficOutput].filter(Boolean).join("\n\n");
    const terminal = terminalOutput ? `
        <pre class="ansi-output sq-output">${terminalOutput}</pre>` : "";
    content = `
    <section class="report-pane sq-addon-pane">
      ${speedImage}${terminal ? `<div class="sq-terminal-scroll">${terminal}</div>` : (!speedImage ? '<p class="empty-state">测速内容暂不可用。</p>' : "")}
    </section>`;
  } else if (activeEntry?.page) {
    const page = activeEntry.page;
    const pageBody = page.format === "image" ? `
      <a class="result-image nq-image" href="${escapeHtml(page.image_url)}" rel="noreferrer">
        <img src="${escapeHtml(page.image_url)}" alt="${escapeHtml(page.title)}">
      </a>` : `<div class="nq-terminal-scroll"><pre class="ansi-output nq-output">${ansiToHtml(page.content)}</pre></div>`;
    content = `
    <section class="report-pane">
      ${pageBody}
      ${page.truncated ? '<p class="snapshot-note">该页内容因安全大小限制已截断。</p>' : ""}
    </section>`;
  } else {
    content = `
    <section class="report-pane empty-state">
      <h2>NodeQuality 快照暂不可用</h2>
      <p>可以继续查看 <a href="${escapeHtml(report.nq_url)}" rel="noreferrer">原始 NQ 报告</a>，或切换到 SpeedQuality。</p>
    </section>`;
  }

  const activeLabel = activeEntry?.label || "速度质量";
  const promotionText = boundedText(options.promotion?.text, 120);
  const promotionUrl = normalizeHttpsUrl(options.promotion?.url, 2048);
  const promotionContent = promotionUrl
    ? `<a href="${escapeHtml(promotionUrl)}" rel="noreferrer">${escapeHtml(promotionText)}</a>`
    : `<span>${escapeHtml(promotionText)}</span>`;
  const todayUses = usageCount(options.usage?.today);
  const totalUses = usageCount(options.usage?.total);
  const documentTitle = isNodeQualityPage
    ? `${activeLabel} · NodeQuality 关联报告`
    : hasNodeQuality
      ? "SpeedQuality · 关联报告"
      : "SpeedQuality 测速报告";
  const headerHtml = hasNodeQuality
    ? `<div class="site-header"><header class="nq-header">
        <div class="combined-wordmark" aria-label="NodeQuality + SpeedQuality 联合报告">
          <a class="brand-wordmark nq-wordmark" href="${escapeHtml(report.nq_url)}" rel="noreferrer" aria-label="查看 NodeQuality 原始报告">Node<span>Quality</span></a>
          <span class="wordmark-plus" aria-hidden="true">+</span>
          <a class="brand-wordmark sq-wordmark" href="${escapeHtml(speedTabHref)}" aria-label="查看 SpeedQuality 测速结果">Speed<span>Quality</span></a>
        </div>
      </header></div>`
    : `<div class="site-header"><header class="sq-header">
        <a class="brand-wordmark sq-wordmark" href="${escapeHtml(reportPath)}" aria-label="SpeedQuality 测试报告">Speed<span>Quality</span></a>
      </header></div>`;
  const tabsShell = showTabs ? `
  <div class="tabs-shell"><nav aria-label="报告分页">${tabsHtml}
  </nav></div>` : "";
  const noticeHtml = isSpeedPage && (hasNodeQuality || REJECTED_BIND_STATUSES.has(report.bind_status))
    ? `<p class="notice ${noticeClass}"><strong>${escapeHtml(verification)}</strong>　${escapeHtml(notice)}</p>`
    : "";
  const canonicalReportUrl = normalizeHttpsUrl(options.reportUrl, 2048) || reportPath;
  const promotionHtml = isSpeedPage && promotionText ? `
    <aside class="promotion" aria-label="推广"><span class="promotion-label">推广</span>${promotionContent}</aside>` : "";
  const summaryHtml = isSpeedPage ? `
    <section class="report-summary" aria-label="SpeedQuality 使用统计">
      <p>今日速度检测量：<strong>${todayUses}</strong>；总检测量：<strong>${totalUses}</strong>。感谢使用 SpeedQuality！</p>
      <p>报告链接：<a href="${escapeHtml(canonicalReportUrl)}">${escapeHtml(canonicalReportUrl)}</a></p>
    </section>` : "";
  const footerHtml = isSpeedPage
    ? `<footer>报告将在 ${escapeHtml(formatTime(report.expires_at))} 后自动清理。</footer>`
    : "";
  const scriptNonce = /^[A-Za-z0-9_-]{16,64}$/.test(String(options.scriptNonce || ""))
    ? String(options.scriptNonce)
    : "";
  const copyScript = `
  <script${scriptNonce ? ` nonce="${scriptNonce}"` : ""}>
    (() => {
      const buttons = document.querySelectorAll("[data-copy-source]");
      const statuses = document.querySelectorAll(".copy-status");
      let statusTimer;
      const setStatus = (message) => {
        for (const status of statuses) status.textContent = message;
      };
      const fallbackCopy = (text) => {
        const field = document.createElement("textarea");
        field.value = text;
        field.setAttribute("readonly", "");
        field.style.position = "fixed";
        field.style.inset = "0 auto auto 0";
        field.style.opacity = "0";
        document.body.append(field);
        field.select();
        field.setSelectionRange(0, field.value.length);
        const copied = document.execCommand("copy");
        field.remove();
        if (!copied) throw new Error("copy_failed");
      };
      const copy = async (text) => {
        if (navigator.clipboard && window.isSecureContext) {
          try {
            await navigator.clipboard.writeText(text);
            return;
          } catch {}
        }
        fallbackCopy(text);
      };
      const download = (text, filename) => {
        const link = document.createElement("a");
        const objectUrl = URL.createObjectURL(new Blob([text], { type: "text/markdown;charset=utf-8" }));
        link.href = objectUrl;
        link.download = filename;
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(objectUrl), 0);
      };
      for (const button of buttons) {
        button.addEventListener("click", async () => {
          const source = document.getElementById(button.dataset.copySource || "");
          if (!source) return;
          const label = button.textContent.trim();
          try {
            await copy(source.value);
            if (button.dataset.download) download(source.value, button.dataset.download);
            setStatus("已复制：" + label);
          } catch {
            setStatus("复制失败，请重试。");
          }
          clearTimeout(statusTimer);
          statusTimer = setTimeout(() => { setStatus(""); }, 2400);
        });
      }
    })();
  </script>`;
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>${escapeHtml(documentTitle)}</title>
  <style>
    :root { color-scheme:dark; --bg:#050508; --surface:#ffffff1a; --text:#f8dcc0; --muted:#9aa7a7; --line:#ffffff30; --accent:#37ff8b; --ok:#9eff6e; --okbg:#18341f99; --warn:#ffa500; --warnbg:#3a240f99; --danger:#fc5f5a; --dangerbg:#481a1a99; --terminal:transparent; --terminal-text:#f8dcc0; }
    * { box-sizing:border-box; }
    html { min-height:100%; background:#000; }
    body { min-height:100vh; margin:0; background:var(--bg); color:var(--text); font:14px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif; letter-spacing:0; }
    a { color:var(--accent); text-underline-offset:3px; }
    header,main,footer { width:min(1200px,70%); margin-inline:auto; }
    .site-header { background:transparent; }
    header { display:flex; align-items:center; justify-content:center; min-height:119px; padding:28px 0 20px; }
    h1 { margin:0; font-size:22px; line-height:1.3; letter-spacing:0; }
    h2 { margin:0; font-size:18px; letter-spacing:0; }
    header .muted { margin:0; color:var(--muted); }
    .tabs-shell { max-width:100%; margin:0 0 10px; overflow-x:auto; scrollbar-width:thin; }
    nav { display:inline-flex; min-height:40px; margin:0; padding:4px; align-items:center; gap:2px; border-radius:8px; background:#202020; box-shadow:0 0 0 1px #0000000f; }
    nav a { display:flex; min-width:96px; height:32px; padding:0 12px; align-items:center; justify-content:center; border:0; border-radius:8px; color:#00ff7b; font-size:14px; font-weight:400; text-decoration:none; white-space:nowrap; transition:color .15s ease-in-out,background-color .15s ease-in-out; }
    nav a:hover { color:#f8e5a7; }
    nav a[aria-current="page"] { background:#474747; color:#00ff7b; font-weight:600; }
    main { min-height:520px; padding:10px 20px; margin-top:10px; margin-bottom:10px; overflow:hidden; border-radius:10px; background:#ffffff1a; backdrop-filter:blur(3px); }
    .notice { margin:16px 0 0; padding:9px 12px; border-left:4px solid var(--line); background:var(--surface); }
    .notice.ok { border-color:var(--ok); background:var(--okbg); color:#105a38; }
    .notice.warning { border-color:#ffa500; background:var(--warnbg); color:var(--warn); }
    .notice.danger { border-color:var(--danger); background:var(--dangerbg); color:var(--danger); font-weight:650; }
    .report-pane { padding:0; }
    .header-meta { text-align:right; }
    .header-meta p { margin:0; }
    .header-meta a { display:inline-block; margin-top:3px; }
    .copy-actions { display:flex; width:min(1200px,70%); margin-inline:auto; flex-wrap:wrap; gap:8px; }
    .copy-actions-top { margin-top:10px; }
    .copy-actions-bottom { margin-bottom:10px; }
    .copy-actions button { display:inline-block; height:1.8rem; padding:0 1.2em; border:.1em solid #fff; border-radius:.12em; background:transparent; color:#fff; font:300 13px/1.8rem Roboto,sans-serif; letter-spacing:0; text-align:center; cursor:pointer; transition:all .2s; }
    .copy-actions button:hover { border-color:#fff; background:#fff; color:#000; }
    .copy-actions button:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
    .copy-actions .general-md { margin-left:auto; }
    .copy-status { position:fixed; z-index:100; left:20px; bottom:14px; margin:0; padding:10px 14px; border-radius:2px; background:#2a2a2a; box-shadow:0 1px 4px #00000080; color:#eee; font-size:14px; }
    .copy-status:empty { display:none; }
    .result-image { display:block; margin:0; overflow:auto; }
    img { display:block; max-width:none; width:auto; min-width:100%; height:auto; border:0; background:transparent; }
    .ansi-output { width:max-content; min-width:100%; max-height:none; overflow:visible; margin:0; padding:0; border:0; background:transparent; color:var(--terminal-text); font:14px/1.25 Consolas,"Liberation Mono","Courier New",monospace; letter-spacing:0; white-space:pre; tab-size:8; }
    .snapshot-note { margin:16px 0 4px; padding:10px 12px; background:var(--warnbg); color:var(--warn); }
    .empty-state { padding:24px 0; color:var(--muted); }
    .empty-state h2 { color:var(--text); }
    .empty-state p { margin:5px 0 0; }
    .promotion { display:flex; align-items:center; justify-content:center; gap:10px; min-height:42px; margin:16px 0 6px; padding:9px 12px 4px; border:0; border-top:1px solid #ffffff30; background:transparent; text-align:center; }
    .promotion-label { flex:0 0 auto; color:#70a598; font-size:12px; }
    .promotion a,.promotion > span:last-child { overflow-wrap:anywhere; font-weight:650; }
    .report-summary { padding:5px 0 2px; color:#f8dcc0; font:13px/1.5 Consolas,"Liberation Mono","Courier New",monospace; }
    .report-summary p { margin:0; }
    .report-summary strong { color:#9eff6e; font-weight:700; font-variant-numeric:tabular-nums; }
    .report-summary a { color:#70a598; overflow-wrap:anywhere; }
    footer { padding:2px 0 24px; color:#8f98a4; font-size:12px; text-align:center; }
    body.linked-report,body.standalone-report { padding-bottom:18px; background:radial-gradient(ellipse 80% 80% at 50% -20%,#7877c64d,#fff0),radial-gradient(125% 125% at 50% 10%,#000 40%,#63e); background-attachment:fixed; }
    .brand-wordmark { position:relative; display:inline-block; padding-left:11px; border-left:6px solid #37ff8b; color:#fff; font:400 48px/1.2 Arial,sans-serif; text-decoration:none; }
    .brand-wordmark span { color:inherit; }
    .combined-wordmark { display:flex; align-items:center; justify-content:center; }
    .combined-wordmark .brand-wordmark { padding-left:11px; font-size:42px; }
    .combined-wordmark .sq-wordmark { padding-left:0; border-left:0; }
    .sq-header .sq-wordmark { padding-left:0; border-left:0; }
    .wordmark-plus { margin:0 11px; color:#fff; font:300 34px/1 Arial,sans-serif; }
    .nq-terminal-scroll,.sq-terminal-scroll { overflow:auto; scrollbar-color:#efefef #797979; scrollbar-width:thin; }
    .linked-report .nq-terminal-scroll { min-height:500px; max-height:min(760px,calc(100vh - 205px)); }
    .sq-terminal-scroll { overflow-x:auto; overflow-y:hidden; }
    .linked-report .nq-output { min-width:1000px; }
    .notice { margin:0 0 10px; padding:7px 10px; border-radius:2px; font:13px/1.45 Consolas,"Liberation Mono","Courier New",monospace; }
    .notice.ok { border-color:#4ab118; background:var(--okbg); color:#9eff6e; }
    .notice.warning { border-color:#ffa500; background:var(--warnbg); color:#ffa500; }
    .notice.danger { border-color:#bd0013; background:var(--dangerbg); color:#fc5f5a; }
    :is(.nq-terminal-scroll,.sq-terminal-scroll,.tabs-shell)::-webkit-scrollbar { width:4px; height:4px; }
    :is(.nq-terminal-scroll,.sq-terminal-scroll,.tabs-shell)::-webkit-scrollbar-track { border-radius:2px; background:#797979; }
    :is(.nq-terminal-scroll,.sq-terminal-scroll,.tabs-shell)::-webkit-scrollbar-thumb { border-radius:10px; background:#efefef; }
    :is(.nq-terminal-scroll,.sq-terminal-scroll,.tabs-shell)::-webkit-scrollbar-thumb:hover { background:#95e6ff; }
    @media (max-width:768px) { header,main,footer,.copy-actions { width:auto; margin-inline:10px; } main { padding-inline:10px; } nav a { min-width:80px; } }
    @media (max-width:640px) { header { min-height:100px; padding:20px 0 14px; } .brand-wordmark { font-size:34px; } .combined-wordmark .brand-wordmark { font-size:22px; } .wordmark-plus { margin-inline:7px; font-size:19px; } .copy-actions button { padding:0 1em; } .copy-actions .general-md { margin-left:0; } .copy-status { right:0; bottom:0; left:0; border-radius:0; text-align:center; } .ansi-output { font-size:12px; } .linked-report .nq-output { font-size:12px; } .linked-report .nq-terminal-scroll { min-height:460px; max-height:calc(100vh - 185px); } }
  </style>
</head>
<body class="${hasNodeQuality ? "linked-report" : "standalone-report"}">
  ${headerHtml}
  ${copyActionsTop}
  <main class="${isNodeQualityPage ? "nodequality-page" : hasNodeQuality ? "speedquality-addon-page" : "speedquality-standalone-page"}${showTabs ? "" : " single-page-report"}">
    ${tabsShell}${noticeHtml}${content}${promotionHtml}${summaryHtml}
  </main>
  ${copyActionsBottom}
  ${footerHtml}
  ${copySources}
  ${copyScript}
</body>
</html>`;
}

async function readStreamLimited(stream, maxBytes) {
  const reader = stream.getReader();
  const chunks = [];
  let size = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maxBytes) {
      await reader.cancel();
      throw new Error("Snapshot exceeds its uncompressed limit");
    }
    chunks.push(value);
  }
  const combined = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    combined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return combined;
}

async function loadNodeQualitySnapshot(env, id) {
  if (!env.SNAPSHOTS) return null;
  try {
    const object = await env.SNAPSHOTS.get(snapshotObjectKey(id));
    if (!object) return null;
    const decompressed = object.body.pipeThrough(new DecompressionStream("gzip"));
    const bytes = await readStreamLimited(decompressed, MAX_SNAPSHOT_BYTES);
    const validated = validateNodeQualitySnapshot(decoder.decode(bytes));
    return validated.error ? null : validated.value;
  } catch {
    return null;
  }
}

async function showReport(request, env, id) {
  if (!env.DB) {
    return textResponse("Report storage is not configured\n", 503, {
      "cache-control": "no-store",
    });
  }
  if (!REPORT_ID_PATTERN.test(id)) {
    return textResponse("Not Found\n", 404, { "cache-control": "no-store" });
  }
  try {
    const now = Math.floor(Date.now() / 1000);
    const report = await env.DB.prepare(
      "SELECT * FROM reports WHERE id = ? AND expires_at > ?",
    ).bind(id, now).first();
    if (!report) {
      return textResponse("Not Found\n", 404, { "cache-control": "no-store" });
    }
    const url = new URL(request.url);
    const requestedTab = url.searchParams.get("tab") || "";
    const tab = /^[a-z0-9-]{1,35}$/.test(requestedTab) ? requestedTab : "";
    const [snapshot, usage] = await Promise.all([
      report.nq_url && request.method !== "HEAD"
        ? loadNodeQualitySnapshot(env, id)
        : null,
      readUsageCounters(env, now),
    ]);
    const scriptNonce = `${randomId()}${randomId()}`;
    const page = renderReport(report, {
      tab,
      snapshot,
      reportPath: url.pathname,
      reportUrl: `${url.origin}${url.pathname}`,
      scriptNonce,
      usage,
      promotion: reportPromotion(env),
    });
    return htmlResponse(request.method === "HEAD" ? null : page, 200, {
      "cache-control": "public, max-age=60, s-maxage=300",
      "content-security-policy": reportContentSecurityPolicy(scriptNonce),
    });
  } catch {
    return textResponse("Unable to read report\n", 500, { "cache-control": "no-store" });
  }
}

async function serveRepositoryScript(request, env, filename, description) {
  const upstreamUrl = resolveUpstream(env, filename);
  const probeVersion = configuredProbeVersion(env);
  if (!upstreamUrl || !probeVersion) {
    return textResponse("Worker GitHub source is not configured\n", 503, {
      "cache-control": "no-store",
    });
  }
  let upstream;
  try {
    upstream = await fetch(upstreamUrl, {
      headers: {
        accept: "text/plain",
        "user-agent": "SpeedQuality-Script-Proxy/1.0",
      },
      cf: { cacheEverything: true, cacheTtl: 300 },
    });
  } catch {
    return textResponse(`Unable to fetch ${description}\n`, 502, {
      "cache-control": "no-store",
    });
  }
  if (!upstream.ok) {
    return textResponse(`Unable to fetch ${description}\n`, 502, {
      "cache-control": "no-store",
    });
  }
  let body = null;
  if (request.method !== "HEAD") {
    body = (await upstream.text())
      .replaceAll(SCRIPT_MARKER, new URL(request.url).origin)
      .replaceAll(PROBE_VERSION_MARKER, probeVersion)
      .replaceAll(NQ_BINDING_MARKER, nodeQualityBindingEnabled(env) ? "1" : "0");
  }
  return textResponse(body, 200, {
    "cache-control": "public, max-age=60, s-maxage=300",
  });
}

function serveScript(request, env) {
  return serveRepositoryScript(request, env, "run.sh", "SpeedQuality script");
}

function serveNodeInstaller(request, env) {
  return serveRepositoryScript(request, env, "install-node.sh", "sq-node installer");
}

async function serveProbeAsset(request, env, version, asset) {
  const upstreamUrl = resolveProbeAsset(env, version, asset);
  if (!upstreamUrl) {
    return textResponse("Probe release is not configured\n", 503, { "cache-control": "no-store" });
  }
  let upstream;
  try {
    upstream = await fetch(upstreamUrl, {
      headers: { accept: "application/octet-stream", "user-agent": "SpeedQuality-Release-Proxy/1.0" },
      cf: { cacheEverything: true, cacheTtl: 86400 },
    });
  } catch {
    return textResponse("Unable to fetch probe release\n", 502, { "cache-control": "no-store" });
  }
  if (!upstream.ok) {
    return textResponse("Unable to fetch probe release\n", 502, { "cache-control": "no-store" });
  }
  return new Response(request.method === "HEAD" ? null : upstream.body, {
    status: 200,
    headers: commonHeaders({
      "content-type": asset.endsWith(".txt") || asset.endsWith(".json") || asset.endsWith(".sig")
        ? "text/plain; charset=utf-8"
        : "application/octet-stream",
      "cache-control": "public, max-age=3600, s-maxage=86400, immutable",
      "content-disposition": `attachment; filename="${asset}"`,
    }),
  });
}

async function routeRequest(request, env) {
  const url = new URL(request.url);

  if (url.pathname === "/health") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return textResponse(request.method === "HEAD" ? null : "ok\n", 200, {
      "cache-control": "no-store",
      "x-report-store": env.DB ? "configured" : "unconfigured",
      "x-snapshot-store": env.SNAPSHOTS ? "configured" : "unconfigured",
      "x-node-service": env.NODE_CORE || hasStaticNodeConfiguration(env)
        ? "configured"
        : "unconfigured",
    });
  }

  if (url.pathname === "/api/features") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return new Response(request.method === "HEAD" ? null : JSON.stringify({
      version: 1,
      nodequality_binding: nodeQualityBindingEnabled(env),
    }), {
      status: 200,
      headers: commonHeaders({
        "content-type": "application/json; charset=utf-8",
        "cache-control": "no-store",
      }),
    });
  }

  if (url.pathname === "/api/time") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return new Response(request.method === "HEAD" ? null : JSON.stringify({
      version: 1,
      epoch: Math.floor(Date.now() / 1000),
    }), {
      status: 200,
      headers: commonHeaders({
        "content-type": "application/json; charset=utf-8",
        "cache-control": "no-store",
      }),
    });
  }

  if (url.pathname === "/run" || url.pathname === "/run.sh") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return serveScript(request, env);
  }

  if (url.pathname === "/install-node") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return serveNodeInstaller(request, env);
  }

  if (url.pathname === "/api/nodes/detect") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return detectNodeEnvironment(request);
  }

  if (url.pathname === "/api/nodes/update") {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return nodeUpdateInfo(request, env);
  }

  const probeAssetMatch = url.pathname.match(
    /^\/bin\/(v\d+\.\d+\.\d+)\/((?:sqprobe|sq-node)-linux-(?:amd64|arm64)|checksums\.txt|release-manifest\.json(?:\.sig)?)$/,
  );
  if (probeAssetMatch) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return serveProbeAsset(request, env, probeAssetMatch[1], probeAssetMatch[2]);
  }

  if (url.pathname === "/api/results") {
    if (request.method !== "POST") {
      return textResponse("Method Not Allowed\n", 405, { allow: "POST" });
    }
    return createReport(request, env);
  }

  const nodeAPI = {
    "/api/nodes/register": "register",
    "/api/nodes/heartbeat": "heartbeat",
    "/api/nodes/unregister": "unregister",
    "/api/nodes/route-key": "routeKey",
    "/api/nodes/control": "control",
    "/api/nodes/control/ack": "controlAck",
    "/api/nodes/resolve": "resolve",
  }[url.pathname];
  if (nodeAPI) {
    if (request.method !== "POST") {
      return textResponse("Method Not Allowed\n", 405, { allow: "POST" });
    }
    return proxyCommunityNodeRequest(request, env, nodeAPI);
  }

  if (url.pathname === "/api/session") {
    if (request.method !== "POST") {
      return textResponse("Method Not Allowed\n", 405, { allow: "POST" });
    }
    return createSession(request, env);
  }

  if (url.pathname === "/api/node-lease") {
    if (request.method !== "POST") {
      return textResponse("Method Not Allowed\n", 405, { allow: "POST" });
    }
    return requestNodeLease(request, env);
  }

  const reportMatch = url.pathname.match(/^\/r\/([^/]+)$/);
  if (reportMatch) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return textResponse("Method Not Allowed\n", 405, { allow: "GET, HEAD" });
    }
    return showReport(request, env, reportMatch[1]);
  }

  return textResponse("Not Found\n", 404, { "cache-control": "no-store" });
}

async function handleFetch(request, env) {
  const started = Date.now();
  const currentRequestID = requestID(request);
  const pathname = new URL(request.url).pathname;
  let response;
  try {
    response = await routeRequest(request, env);
  } catch (error) {
    logEvent(env, "error", "http.request_failed", {
      request_id: currentRequestID,
      method: request.method,
      path: routeLabel(pathname),
      error_type: error?.name || "Error",
      duration_ms: Date.now() - started,
    });
    return textResponse("Internal Server Error\n", 500, {
      "cache-control": "no-store",
      "x-request-id": currentRequestID,
    });
  }
  const output = new Response(response.body, response);
  output.headers.set("x-request-id", currentRequestID);
  logEvent(
    env,
    response.status >= 500 ? "error" : response.status >= 400 ? "warn" : "info",
    "http.request_completed",
    {
      request_id: currentRequestID,
      method: request.method,
      path: routeLabel(pathname),
      status: response.status,
      duration_ms: Date.now() - started,
    },
  );
  return output;
}

export async function cleanupExpiredReports(env, now = Math.floor(Date.now() / 1000)) {
  if (!env.DB) return;
  const cutoffDay = new Date((now - 8 * 86400) * 1000).toISOString().slice(0, 10);
  if (env.SNAPSHOTS) {
    const expired = await env.DB.prepare(
      "SELECT id FROM reports WHERE expires_at <= ? ORDER BY id",
    ).bind(now).all();
    const keys = (expired?.results || [])
      .map((row) => row?.id)
      .filter((id) => REPORT_ID_PATTERN.test(String(id)))
      .map((id) => snapshotObjectKey(id));
    for (let offset = 0; offset < keys.length; offset += 1000) {
      await env.SNAPSHOTS.delete(keys.slice(offset, offset + 1000));
    }
  }
  await env.DB.batch([
    env.DB.prepare("DELETE FROM reports WHERE expires_at <= ?").bind(now),
    env.DB.prepare("DELETE FROM rate_limits WHERE day < ?").bind(cutoffDay),
    env.DB.prepare(
      "DELETE FROM session_leases WHERE token_hash IN (SELECT token_hash FROM sessions WHERE expires_at <= ?)",
    ).bind(now),
    env.DB.prepare("DELETE FROM sessions WHERE expires_at <= ?").bind(now),
    env.DB.prepare("DELETE FROM session_rate_limits WHERE day < ?").bind(cutoffDay),
  ]);
}

export default {
  fetch: handleFetch,
  async scheduled(_event, env, context) {
    context.waitUntil((async () => {
      const started = Date.now();
      try {
        await cleanupExpiredReports(env);
        logEvent(env, "info", "scheduled.completed", { duration_ms: Date.now() - started });
      } catch (error) {
        logEvent(env, "error", "scheduled.failed", {
          error_type: error?.name || "Error",
          duration_ms: Date.now() - started,
        });
        throw error;
      }
    })());
  },
};
