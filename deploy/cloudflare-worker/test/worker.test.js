import assert from "node:assert/strict";
import test from "node:test";
import { runInNewContext } from "node:vm";

import worker, { formatReportCopies, maskedReportAddress, renderReport } from "../src/index.js";
import { logEvent } from "../src/observability.js";

class FakeD1 {
  constructor() {
    this.reports = new Map();
    this.limits = new Map();
    this.sessions = new Map();
    this.sessionLeases = new Map();
    this.sessionLimits = new Map();
    this.usageCounters = new Map();
  }

  prepare(sql) {
    const database = this;
    const normalized = sql.replace(/\s+/g, " ").trim().toLowerCase();
    return {
      arguments: [],
      bind(...values) {
        this.arguments = values;
        return this;
      },
      async first() {
        if (normalized.startsWith("insert into session_rate_limits")) {
          const [day, hash, updatedAt] = this.arguments;
          const key = `${day}:${hash}`;
          const count = (database.sessionLimits.get(key)?.count || 0) + 1;
          database.sessionLimits.set(key, { count, updatedAt });
          return { count };
        }
        if (normalized.startsWith("insert into rate_limits")) {
          const [day, hash, updatedAt] = this.arguments;
          const key = `${day}:${hash}`;
          const count = (database.limits.get(key)?.count || 0) + 1;
          database.limits.set(key, { count, updatedAt });
          return { count };
        }
        if (normalized.startsWith("select * from reports")) {
          const [id, now] = this.arguments;
          const report = database.reports.get(id);
          return report && report.expires_at > now ? report : null;
        }
        if (normalized.startsWith("select * from sessions")) {
          const [tokenHash, now] = this.arguments;
          const session = database.sessions.get(tokenHash);
          return session && session.expires_at > now ? session : null;
        }
        if (normalized.startsWith("insert into session_leases")) {
          const [tokenHash, region, family, updatedAt] = this.arguments;
          const key = `${tokenHash}:${region}:${family}`;
          const attempts = (database.sessionLeases.get(key)?.attempts || 0) + 1;
          database.sessionLeases.set(key, {
            tokenHash, region, family, attempts, updatedAt,
            preparation_id: database.sessionLeases.get(key)?.preparation_id || "",
          });
          return { attempts };
        }
        if (normalized.startsWith("select attempts, preparation_id from session_leases")) {
          const [tokenHash, region, family] = this.arguments;
          return database.sessionLeases.get(`${tokenHash}:${region}:${family}`) || null;
        }
        throw new Error(`Unsupported first query: ${normalized}`);
      },
      async all() {
        if (normalized.startsWith("select key, count from usage_counters")) {
          return {
            results: this.arguments
              .filter((key) => database.usageCounters.has(key))
              .map((key) => ({ key, count: database.usageCounters.get(key).count })),
          };
        }
        if (normalized.startsWith("select id from reports where expires_at")) {
          const [now] = this.arguments;
          return {
            results: [...database.reports.values()]
              .filter((report) => report.expires_at <= now)
              .sort((left, right) => left.id.localeCompare(right.id))
              .map((report) => ({ id: report.id })),
          };
        }
        throw new Error(`Unsupported all query: ${normalized}`);
      },
      async run() {
        if (normalized.startsWith("insert into usage_counters")) {
          const [key, updatedAt] = this.arguments;
          const count = (database.usageCounters.get(key)?.count || 0) + 1;
          database.usageCounters.set(key, { count, updatedAt });
          return { success: true };
        }
        if (normalized.startsWith("insert into sessions")) {
          const [
            tokenHash, createdAt, expiresAt, clientHash, regions, mode, ipMode,
            durationSeconds, targetMbps, leaseLimit, communityNodeID,
          ] = this.arguments;
          database.sessions.set(tokenHash, {
            token_hash: tokenHash,
            created_at: createdAt,
            expires_at: expiresAt,
            client_hash: clientHash,
            regions,
            mode,
            ip_mode: ipMode,
            duration_seconds: durationSeconds,
            target_mbps: targetMbps,
            lease_limit: leaseLimit,
            community_node_id: communityNodeID || "",
            completed_at: null,
          });
          return { success: true };
        }
        if (normalized.startsWith("update sessions set completed_at")) {
          const [completedAt, tokenHash] = this.arguments;
          const session = database.sessions.get(tokenHash);
          if (session) session.completed_at = completedAt;
          return { success: true };
        }
        if (normalized.startsWith("update session_leases set preparation_id")) {
          const [preparationID, updatedAt, tokenHash, region, family] = this.arguments;
          const row = database.sessionLeases.get(`${tokenHash}:${region}:${family}`);
          if (row) {
            row.preparation_id = preparationID;
            row.updatedAt = updatedAt;
          }
          return { success: true };
        }
        if (normalized.startsWith("delete from session_leases")) {
          return { success: true };
        }
        if (normalized.startsWith("delete from sessions where expires_at")) {
          const [now] = this.arguments;
          for (const [tokenHash, session] of database.sessions) {
            if (session.expires_at <= now) database.sessions.delete(tokenHash);
          }
          return { success: true };
        }
        if (normalized.startsWith("delete from session_rate_limits where day")) {
          const [cutoffDay] = this.arguments;
          for (const key of database.sessionLimits.keys()) {
            if (key.slice(0, 10) < cutoffDay) database.sessionLimits.delete(key);
          }
          return { success: true };
        }
        if (normalized.startsWith("delete from reports where expires_at")) {
          const [now] = this.arguments;
          for (const [id, report] of database.reports) {
            if (report.expires_at <= now) database.reports.delete(id);
          }
          return { success: true };
        }
        if (normalized.startsWith("delete from rate_limits where day")) {
          const [cutoffDay] = this.arguments;
          for (const key of database.limits.keys()) {
            if (key.slice(0, 10) < cutoffDay) database.limits.delete(key);
          }
          return { success: true };
        }
        if (!normalized.startsWith("insert into reports")) {
          throw new Error(`Unsupported run query: ${normalized}`);
        }
        const [
          id, createdAt, expiresAt, testedAt, regions, mode, ipMode,
          sourceIPMasked, sourceAsn, sourceAsOrganization,
          speedUrl, speedText, speedData, durationSeconds, targetMbps,
          trafficRxBytes, trafficTxBytes,
          nqUrl, nqTestedAt, nqTimeSource, timeGapSeconds, nqIdentityReason,
          bindStatus, version,
        ] = this.arguments;
        if (database.reports.has(id)) throw new Error("UNIQUE constraint failed");
        database.reports.set(id, {
          id,
          created_at: createdAt,
          expires_at: expiresAt,
          tested_at: testedAt,
          regions,
          mode,
          ip_mode: ipMode,
          source_ip_masked: sourceIPMasked,
          source_asn: sourceAsn,
          source_as_organization: sourceAsOrganization,
          speed_url: speedUrl,
          speed_text: speedText,
          speed_data: speedData,
          duration_seconds: durationSeconds,
          target_mbps: targetMbps,
          traffic_rx_bytes: trafficRxBytes,
          traffic_tx_bytes: trafficTxBytes,
          nq_url: nqUrl,
          nq_tested_at: nqTestedAt,
          nq_time_source: nqTimeSource,
          time_gap_seconds: timeGapSeconds,
          nq_identity_reason: nqIdentityReason,
          bind_status: bindStatus,
          version,
        });
        return { success: true };
      },
    };
  }

  async batch(statements) {
    return Promise.all(statements.map((statement) => statement.run()));
  }
}

class FakeR2 {
  constructor() {
    this.objects = new Map();
  }

  async put(key, value, options = {}) {
    const bytes = value instanceof ArrayBuffer
      ? new Uint8Array(value)
      : new Uint8Array(await new Response(value).arrayBuffer());
    this.objects.set(key, { bytes: new Uint8Array(bytes), options });
  }

  async get(key) {
    const object = this.objects.get(key);
    if (!object) return null;
    return {
      body: new Blob([object.bytes]).stream(),
      httpMetadata: object.options.httpMetadata,
      customMetadata: object.options.customMetadata,
    };
  }

  async delete(keys) {
    for (const key of Array.isArray(keys) ? keys : [keys]) {
      this.objects.delete(key);
    }
  }
}

function resultRequest(fields, headers = {}) {
  return new Request("https://rtw.example/api/results", {
    method: "POST",
    headers: {
      "cf-connecting-ip": "203.0.113.9",
      ...headers,
    },
    body: new URLSearchParams(fields),
  });
}

function multipartResultRequest(fields, snapshot, headers = {}) {
  const body = new FormData();
  for (const [key, value] of Object.entries(fields)) body.set(key, value);
  body.set(
    "nq_snapshot",
    new Blob([JSON.stringify(snapshot)], { type: "application/json" }),
    "nodequality.json",
  );
  return new Request("https://rtw.example/api/results", {
    method: "POST",
    headers: { "cf-connecting-ip": "203.0.113.9", ...headers },
    body,
  });
}

function basicFields(overrides = {}) {
  return {
    tested_at: String(Math.floor(Date.now() / 1000)),
    regions: "湖北",
    mode: "s",
    ip_mode: "v4",
    speed_url: "",
    speed_text: "湖北电信 <script>alert(1)</script>",
    speed_data: structuredSpeedData(),
    duration_seconds: "5",
    target_mbps: "200",
    traffic_rx_bytes: "100000000",
    traffic_tx_bytes: "50000000",
    bind_status: "standalone",
    version: "0.7.0",
    ...overrides,
  };
}

function sessionRequest(fields = {}, ip = "203.0.113.9") {
  return new Request("https://rtw.example/api/session", {
    method: "POST",
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      "cf-connecting-ip": ip,
    },
    body: new URLSearchParams({
      regions: "hb",
      mode: "s",
      ip_mode: "v4",
      duration_seconds: "5",
      target_mbps: "200",
      ...fields,
    }),
  });
}

function resultRegionCodes(fields) {
  try {
    const codes = String(fields.speed_data || "")
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line)?.region?.code)
      .filter(Boolean);
    return [...new Set(codes)].join(",") || "hb";
  } catch {
    return "hb";
  }
}

async function resultSessionToken(fields, env) {
  const target = Number(fields.target_mbps);
  const response = await worker.fetch(sessionRequest({
    regions: resultRegionCodes(fields),
    mode: fields.mode === "s" ? fields.mode : "s",
    ip_mode: ["v4", "v6"].includes(fields.ip_mode) ? fields.ip_mode : "v4",
    duration_seconds: "5",
    target_mbps: [100, 200, 400].includes(target) ? String(target) : "200",
  }), env);
  assert.equal(response.status, 201);
  return (await response.text()).trim();
}

async function submitResult(fields, env) {
  const token = await resultSessionToken(fields, env);
  return worker.fetch(resultRequest(fields, {
    authorization: `Bearer ${token}`,
  }), env);
}

async function submitMultipartResult(fields, snapshot, env) {
  const token = await resultSessionToken(fields, env);
  return worker.fetch(multipartResultRequest(fields, snapshot, {
    authorization: `Bearer ${token}`,
  }), env);
}

function leaseRequest(token, fields = {}, ip = "203.0.113.9") {
  return new Request("https://rtw.example/api/node-lease", {
    method: "POST",
    headers: {
      "content-type": "application/x-www-form-urlencoded",
      "cf-connecting-ip": ip,
      authorization: `Bearer ${token}`,
    },
    body: new URLSearchParams({ region: "hb", family: "v4", ...fields }),
  });
}

function coreLease(input) {
  const now = Math.floor(Date.now() / 1000);
  const address = input.family === "v6" ? "2001:db8::10" : "192.0.2.10";
  const authority = input.family === "v6" ? `[${address}]:8080` : `${address}:8080`;
  return {
    version: 1,
    lease_id: "lease_fixture_123",
    issued_at: now,
    expires_at: now + 180,
    region: { code: input.region, name: "湖北" },
    family: input.family,
    duration_seconds: input.duration_seconds,
    target_mbps: input.target_mbps,
    modes: input.modes,
    targets: [{
      carrier: "ct",
      label: "湖北电信",
      candidates: [{
        id: "0123456789abcdef0123456789abcdef",
        address,
        port: 8080,
        activate: { method: "GET", url: `http://${authority}/activate` },
        download: { method: "GET", url: `http://${authority}/download?key={key}` },
        upload: { method: "POST", url: `http://${authority}/upload`, content_length: 1000000 },
        release: { method: "POST", url: `http://${authority}/release?key={key}` },
      }],
    }],
  };
}

class FakeNodeCore {
  constructor() {
    this.leases = [];
    this.feedback = [];
    this.community = [];
    this.routeKey = `sqn_${"r".repeat(32)}`;
    this.nodeID = "fedcba9876543210fedcba9876543210";
    this.families = ["v4", "v6"];
    this.region = "hb";
  }

  async fetch(request) {
    const url = new URL(request.url);
    const body = await request.json();
    if (url.pathname === "/lease") {
      this.leases.push(body);
      return Response.json(coreLease(body));
    }
    if (url.pathname === "/feedback") {
      this.feedback.push(body);
      return Response.json({ accepted: body.measurements.length });
    }
    if (url.pathname.startsWith("/community/")) {
      this.community.push({
        path: url.pathname,
        body,
        clientIP: request.headers.get("x-community-client-ip") || "",
        apiToken: request.headers.get("x-node-api-token") || "",
        secret: request.headers.get("x-node-core-secret") || "",
      });
      if (url.pathname === "/community/resolve") {
        if (body.route_key !== this.routeKey) {
          return Response.json({ error: "node_route_unavailable" }, { status: 404 });
        }
        return Response.json({
          node_id: this.nodeID,
          region: this.region,
          carrier: "ct",
          families: this.families,
          max_mbps: 200,
          access_mode: "private",
        });
      }
      if (url.pathname === "/community/register") {
        return Response.json({
          node_id: this.nodeID,
          route_key: this.routeKey,
          api_token: `sqa_${"a".repeat(43)}`,
          jwt_public_jwk: { kty: "EC", crv: "P-256", x: "x", y: "y" },
          jwt_issuer: "issuer",
          jwt_audience: "sq-node",
          heartbeat_seconds: 60,
        }, { status: 201 });
      }
      if (url.pathname === "/community/heartbeat") {
        return Response.json({ status: "ok", heartbeat_seconds: 60 });
      }
      if (url.pathname === "/community/unregister") {
        return Response.json({ status: "revoked" });
      }
      if (url.pathname === "/community/route-key") {
        return Response.json({ status: "rotated", route_key: `sqn_${"n".repeat(32)}` });
      }
      if (url.pathname === "/community/control") {
        return Response.json({ status: "ok", server_time: 1, commands: [] });
      }
      if (url.pathname === "/community/control/ack") {
        return Response.json({ status: "ok", accepted: 1 });
      }
    }
    return new Response("Not Found", { status: 404 });
  }
}

function structuredSpeedData() {
  const now = Math.floor(Date.now() / 1000);
  return JSON.stringify({
    version: 1,
    lease_id: "lease_fixture_123",
    started_at: now - 15,
    completed_at: now,
    region: { code: "hb", name: "湖北" },
    family: "v4",
    duration_seconds: 5,
    target_mbps: 200,
    modes: ["s"],
    results: [{
      carrier: "ct",
      label: "湖北电信",
      node_id: "0123456789abcdef0123456789abcdef",
      latency_ms: 8.25,
      status: "ok",
      single: {
        download_mbps: 199.2,
        upload_mbps: 150.5,
        download_bytes: 124500000,
        upload_bytes: 94062500,
      },
    }],
  });
}

test("speed colors use 30 and 80 percent boundaries", () => {
  const now = Math.floor(Date.now() / 1000);
  const result = (carrier, speed, download = speed) => ({
    carrier,
    label: carrier,
    latency_ms: 10,
    status: "ok",
    single: {
      download_mbps: download,
      upload_mbps: speed,
      download_bytes: 1,
      upload_bytes: 1,
    },
  });
  const page = renderReport({
    id: "ColorDemo123",
    tested_at: now,
    expires_at: now + 3600,
    source_ip_masked: "203.0.*.*",
    target_mbps: 200,
    duration_seconds: 5,
    version: "1.0.0",
    speed_data: JSON.stringify([{
      region: { code: "hb", name: "湖北" },
      family: "v4",
      target_mbps: 200,
      results: [
        result("ct", 160, 198),
        result("cu", 60),
        result("cm", 59.9),
      ],
    }]),
  }, { reportUrl: "https://sq.example.com/r/ColorDemo123" });

  assert.match(page, /color:#9eff6e;font-weight:700">\s*160\.00Mbps<\/span>/);
  assert.match(page, /color:rgb\(255,165,0\)">\s*60\.00Mbps<\/span>/);
  assert.match(page, /color:#fc5f5a;font-weight:700">\s*59\.90Mbps<\/span>/);
  assert.match(page, /color:#9eff6e;font-weight:700">\s*200Mbps ✓<\/span>/);
});

test("health reports whether D1 is configured", async () => {
  const response = await worker.fetch(new Request("https://rtw.example/health"), {});
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("x-report-store"), "unconfigured");
  assert.equal(response.headers.get("x-snapshot-store"), "unconfigured");
  assert.equal(await response.text(), "ok\n");
});

test("feature endpoint exposes the NodeQuality binding switch", async () => {
  const enabled = await worker.fetch(new Request("https://rtw.example/api/features"), {});
  assert.equal(enabled.status, 200);
  assert.deepEqual(await enabled.json(), { version: 1, nodequality_binding: true });

  const disabled = await worker.fetch(
    new Request("https://rtw.example/api/features"),
    { NQ_BINDING_ENABLED: "false" },
  );
  assert.equal(disabled.status, 200);
  assert.deepEqual(await disabled.json(), { version: 1, nodequality_binding: false });
});

test("time endpoint exposes an uncached platform timestamp", async () => {
  const before = Math.floor(Date.now() / 1000);
  const response = await worker.fetch(new Request("https://rtw.example/api/time"), {});
  const after = Math.floor(Date.now() / 1000);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  const body = await response.json();
  assert.equal(body.version, 1);
  assert.ok(body.epoch >= before && body.epoch <= after);

  const head = await worker.fetch(new Request("https://rtw.example/api/time", {
    method: "HEAD",
  }), {});
  assert.equal(head.status, 200);
  assert.equal(await head.text(), "");
  const rejected = await worker.fetch(new Request("https://rtw.example/api/time", {
    method: "POST",
  }), {});
  assert.equal(rejected.status, 405);
});

test("report addresses are masked before storage", () => {
  assert.equal(maskedReportAddress("203.0.113.9"), "203.0.*.*");
  assert.equal(maskedReportAddress("2001:0db8:abcd:1234::9"), "2001:db8:abcd::/48");
  assert.equal(maskedReportAddress("::ffff:192.0.2.10"), "192.0.*.*");
  assert.equal(maskedReportAddress("not-an-ip"), "");
});

test("session is source-bound and obtains a lease through the private binding", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  const env = {
    DB,
    NODE_CORE,
    RATE_LIMIT_SALT: "test-salt",
    NODE_CORE_SECRET: "core-secret",
    STATIC_NODES: JSON.stringify({
      hb: [{ carrier: "ct", address: "192.0.2.99", port: 8080, max_mbps: 200 }],
    }),
  };
  const sessionResponse = await worker.fetch(sessionRequest(), env);
  assert.equal(sessionResponse.status, 201);
  const token = (await sessionResponse.text()).trim();
  assert.match(token, /^[A-Za-z0-9_-]{32,128}$/);

  const leaseResponse = await worker.fetch(leaseRequest(token), env);
  assert.equal(leaseResponse.status, 200);
  const lease = await leaseResponse.json();
  assert.equal(lease.region.code, "hb");
  assert.equal(lease.targets[0].candidates[0].address, "192.0.2.10");
  assert.equal(NODE_CORE.leases.length, 1);
  assert.equal(NODE_CORE.leases[0].client_ip, "203.0.113.9");
  assert.equal(NODE_CORE.leases[0].target_mbps, 200);
  assert.deepEqual(NODE_CORE.leases[0].modes, ["s"]);

  const wrongSource = await worker.fetch(leaseRequest(token, {}, "203.0.113.10"), env);
  assert.equal(wrongSource.status, 401);
});

test("firewall preparation polling does not consume the normal lease retry", async () => {
  const DB = new FakeD1();
  const calls = [];
  const preparation = "lease_abcdefghijklmnop";
  const NODE_CORE = {
    async fetch(request) {
      const input = await request.json();
      calls.push(input);
      if (!input.preparation_id) {
        return Response.json({
          status: "preparing",
          preparation,
          expires_at: Math.floor(Date.now() / 1000) + 180,
          retry_after_ms: 500,
        }, { status: 202 });
      }
      return Response.json(coreLease(input));
    },
  };
  const env = { DB, NODE_CORE, NODE_CORE_SECRET: "core-secret", RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const pending = await worker.fetch(leaseRequest(token), env);
  assert.equal(pending.status, 202);
  assert.equal((await pending.json()).preparation, preparation);
  const ready = await worker.fetch(leaseRequest(token, { preparation }), env);
  assert.equal(ready.status, 200);
  assert.equal(calls.length, 2);
  assert.equal(calls[1].preparation_id, preparation);
  const [row] = DB.sessionLeases.values();
  assert.equal(row.attempts, 1);
  assert.equal(row.preparation_id, preparation);
  assert.equal((await worker.fetch(leaseRequest(
    token, { preparation: "lease_wrongpreparation" },
  ), env)).status, 403);
});

test("node management API forwards source identity and keeps the Core secret private", async () => {
  const NODE_CORE = new FakeNodeCore();
  const env = { NODE_CORE, NODE_CORE_SECRET: "core-secret" };
  const registration = await worker.fetch(new Request("https://rtw.example/api/nodes/register", {
    method: "POST",
    headers: { "content-type": "application/json", "cf-connecting-ip": "8.8.8.8" },
    body: JSON.stringify({ version: 1 }),
  }), env);
  assert.equal(registration.status, 201);
  assert.equal((await registration.json()).route_key, NODE_CORE.routeKey);
  assert.deepEqual(NODE_CORE.community[0], {
    path: "/community/register",
    body: { version: 1 },
    clientIP: "8.8.8.8",
    apiToken: "",
    secret: "core-secret",
  });

  const apiToken = `sqa_${"a".repeat(43)}`;
  const heartbeat = await worker.fetch(new Request("https://rtw.example/api/nodes/heartbeat", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${apiToken}`,
      "cf-connecting-ip": "8.8.8.8",
    },
    body: JSON.stringify({ access_mode: "public" }),
  }), env);
  assert.equal(heartbeat.status, 200);
  assert.equal(NODE_CORE.community[1].apiToken, apiToken);
  assert.equal(heartbeat.headers.has("x-node-core-secret"), false);

  const control = await worker.fetch(new Request("https://rtw.example/api/nodes/control", {
    method: "POST",
    headers: { "content-type": "application/json", authorization: `Bearer ${apiToken}` },
    body: "{}",
  }), env);
  assert.equal(control.status, 200);
  assert.equal(NODE_CORE.community.at(-1).path, "/community/control");
  assert.equal(NODE_CORE.community.at(-1).apiToken, apiToken);

  const missingToken = await worker.fetch(new Request("https://rtw.example/api/nodes/unregister", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}",
  }), env);
  assert.equal(missingToken.status, 401);
});

test("node setup detection derives address, province, and carrier without user input", async () => {
  const request = new Request("https://rtw.example/api/nodes/detect", {
    headers: { "cf-connecting-ip": "1.2.3.4" },
  });
  Object.defineProperty(request, "cf", { value: {
    country: "CN",
    region: "Hubei Province",
    regionCode: "HB",
    asn: 4134,
    asOrganization: "CHINANET Hubei province network",
  } });
  const response = await worker.fetch(request, {});
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    address: "1.2.3.4",
    family: "v4",
    country_code: "CN",
    region: "hb",
    region_name: "湖北",
    carrier: "ct",
    carrier_name: "电信",
    asn: 4134,
    as_organization: "CHINANET Hubei province network",
  });
});

test("node update endpoint exposes only the configured stable signed manifest", async () => {
  const env = {
    GITHUB_OWNER: "owner",
    GITHUB_REPO: "speedquality",
    GITHUB_REF: "main",
    PROBE_VERSION: "v1.2.3",
  };
  const response = await worker.fetch(
    new Request("https://rtw.example/api/nodes/update?channel=stable"),
    env,
  );
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    channel: "stable",
    version: "v1.2.3",
    manifest_url: "https://rtw.example/bin/v1.2.3/release-manifest.json",
    signature_url: "https://rtw.example/bin/v1.2.3/release-manifest.json.sig",
  });
  assert.equal((await worker.fetch(
    new Request("https://rtw.example/api/nodes/update?channel=preview"), env,
  )).status, 400);
});

test("a Route Key creates an exact-node session and never stores the raw key", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  const env = { DB, NODE_CORE, NODE_CORE_SECRET: "core-secret", RATE_LIMIT_SALT: "test-salt" };
  const response = await worker.fetch(sessionRequest({ node_route: NODE_CORE.routeKey }), env);
  assert.equal(response.status, 201);
  const token = (await response.text()).trim();
  const [session] = DB.sessions.values();
  assert.equal(session.community_node_id, NODE_CORE.nodeID);
  assert.doesNotMatch(JSON.stringify(session), /sqn_/);

  const lease = await worker.fetch(leaseRequest(token), env);
  assert.equal(lease.status, 200);
  assert.equal(NODE_CORE.leases.at(-1).community_node_id, NODE_CORE.nodeID);

  const wrongProvince = await worker.fetch(sessionRequest({
    regions: "bj",
    node_route: NODE_CORE.routeKey,
  }, "203.0.113.10"), env);
  assert.equal(wrongProvince.status, 400);
  const tooFast = await worker.fetch(sessionRequest({
    target_mbps: "400",
    node_route: NODE_CORE.routeKey,
  }, "203.0.113.11"), env);
  assert.equal(tooFast.status, 400);

  for (const region of ["hk", "mo", "tw"]) {
    const publicRequest = await worker.fetch(sessionRequest({ regions: region }), env);
    assert.equal(publicRequest.status, 400);
    assert.match(await publicRequest.text(), /not open for public speed tests/);

    NODE_CORE.region = region;
    const exactNode = await worker.fetch(sessionRequest({
      regions: region,
      node_route: NODE_CORE.routeKey,
    }), env);
    assert.equal(exactNode.status, 201);
  }
});

test("an IPv6-only Route Key accepts only an IPv6 session", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  NODE_CORE.families = ["v6"];
  const env = { DB, NODE_CORE, NODE_CORE_SECRET: "core-secret", RATE_LIMIT_SALT: "test-salt" };

  const response = await worker.fetch(sessionRequest({
    ip_mode: "v6",
    node_route: NODE_CORE.routeKey,
  }), env);
  assert.equal(response.status, 201);
  const token = (await response.text()).trim();
  const lease = await worker.fetch(leaseRequest(token, { family: "v6" }), env);
  assert.equal(lease.status, 200);
  assert.equal(NODE_CORE.leases.at(-1).family, "v6");

  const incompatible = await worker.fetch(sessionRequest({
    ip_mode: "v4",
    node_route: NODE_CORE.routeKey,
  }, "203.0.113.10"), env);
  assert.equal(incompatible.status, 400);
});

test("self-hosted static nodes issue a lease without a Provider", async () => {
  const DB = new FakeD1();
  const STATIC_NODES = JSON.stringify({
    hb: [
      { carrier: "ct", address: "192.0.2.40", port: 18080, max_mbps: 200 },
      { carrier: "cu", address: "2001:db8::40", port: 18080, max_mbps: 400 },
      { carrier: "cm", address: "192.0.2.41", port: 18080, max_mbps: 100 },
      { carrier: "edu", address: "192.0.2.42", port: 18080, max_mbps: 400 },
    ],
  });
  const env = { DB, STATIC_NODES, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 200);
  const lease = await response.json();
  assert.equal(lease.region.code, "hb");
  assert.equal(lease.region.name, "湖北");
  assert.equal(lease.target_mbps, 200);
  assert.equal(lease.targets.length, 1);
  assert.equal(lease.targets[0].carrier, "ct");
  assert.match(lease.targets[0].candidates[0].id, /^[a-f0-9]{32}$/);
  assert.equal(lease.targets[0].candidates[0].activate.method, "POST");
  assert.equal(
    lease.targets[0].candidates[0].download.url,
    "http://192.0.2.40:18080/download?key={key}&nonce={nonce}",
  );
  const health = await worker.fetch(new Request("https://rtw.example/health"), env);
  assert.equal(health.headers.get("x-node-service"), "configured");
});

test("static nodes enforce address family and configured speed tier", async () => {
  const DB = new FakeD1();
  const env = {
    DB,
    RATE_LIMIT_SALT: "test-salt",
    STATIC_NODES: JSON.stringify({
      hb: [{ carrier: "ct", address: "192.0.2.50", port: 8080, max_mbps: 100 }],
    }),
  };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 503);
  assert.equal(await response.text(), "No matching static nodes are available\n");
});

test("a session can retry each region and family only once", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  assert.equal((await worker.fetch(leaseRequest(token), env)).status, 200);
  assert.equal((await worker.fetch(leaseRequest(token), env)).status, 200);
  assert.equal((await worker.fetch(leaseRequest(token), env)).status, 429);
  assert.equal((await worker.fetch(
    leaseRequest(token, { region: "bj" }), env,
  )).status, 403);
  assert.equal((await worker.fetch(
    leaseRequest(token, { family: "v6" }), env,
  )).status, 403);
});

test("invalid data from the private node service is rejected", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  NODE_CORE.fetch = async (request) => {
    const input = await request.json();
    const lease = coreLease(input);
    lease.expires_at = lease.issued_at;
    return Response.json(lease);
  };
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 502);
  assert.equal(await response.text(), "Node service returned invalid data\n");
});

test("safe node directory errors are exposed to the CLI", async () => {
  const DB = new FakeD1();
  const NODE_CORE = {
    fetch: async () => Response.json(
      { error: "node_directory_unavailable" },
      { status: 502 },
    ),
  };
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 502);
  assert.deepEqual(await response.json(), { error: "node_directory_unavailable" });
});

test("unknown private node errors remain hidden", async () => {
  const DB = new FakeD1();
  const NODE_CORE = {
    fetch: async () => Response.json(
      { error: "private_upstream_detail", endpoint: "http://192.0.2.50:8080" },
      { status: 503 },
    ),
  };
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 502);
  assert.deepEqual(await response.json(), { error: "node_service_unavailable" });
});

test("unsupported carriers from the private node service are rejected", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  NODE_CORE.fetch = async (request) => {
    const input = await request.json();
    const lease = coreLease(input);
    lease.targets[0].carrier = "edu";
    lease.targets[0].label = "湖北教育网";
    return Response.json(lease);
  };
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const response = await worker.fetch(leaseRequest(token), env);
  assert.equal(response.status, 502);
  assert.equal(await response.text(), "Node service returned invalid data\n");
});

test("sessions allow at most five provinces and size their lifetime accordingly", async () => {
  const DB = new FakeD1();
  const regions = "hb,bj,sh,gd,js";
  const response = await worker.fetch(sessionRequest({
    regions,
    ip_mode: "v6",
    target_mbps: "400",
  }), { DB, RATE_LIMIT_SALT: "test-salt" });
  assert.equal(response.status, 201);
  const [session] = DB.sessions.values();
  assert.ok(session.expires_at - session.created_at >= 1300);
  assert.ok(session.expires_at - session.created_at <= 1340);

  const tooMany = await worker.fetch(sessionRequest({
    regions: `${regions},zj`,
  }, "203.0.113.10"), { DB, RATE_LIMIT_SALT: "test-salt" });
  assert.equal(tooMany.status, 400);
});

test("structured results render as terminal text and feed private node health", async () => {
  const DB = new FakeD1();
  const NODE_CORE = new FakeNodeCore();
  const env = { DB, NODE_CORE, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const speedReport = JSON.parse(structuredSpeedData());
  speedReport.results.push({
    carrier: "cu",
    label: "湖北联通",
    node_id: "abcdef0123456789abcdef0123456789",
    status: "failed",
    error: "没有可连接的候选节点",
  });
  speedReport.results.push({
    carrier: "cm",
    label: "湖北移动",
    node_id: "fedcba9876543210fedcba9876543210",
    latency_ms: 769,
    status: "failed",
    error: "上传未产生有效数据",
    single: { download_mbps: 0.04, upload_mbps: 0, download_bytes: 25000, upload_bytes: 0 },
  });
  const fields = basicFields({
    ip_mode: "v4",
    speed_url: "",
    speed_text: "",
    speed_data: JSON.stringify(speedReport),
  });
  const request = resultRequest(fields, { authorization: `Bearer ${token}` });
  const response = await worker.fetch(request, env);
  assert.equal(response.status, 201);
  const reportUrl = (await response.text()).trim();
  const reportID = reportUrl.split("/").pop();
  assert.equal(DB.reports.get(reportID)?.source_ip_masked, "203.0.*.*");
  const page = await (await worker.fetch(new Request(reportUrl), env)).text();
  assert.match(page, /SpeedQuality 测速报告/);
  assert.match(page, /203\.0\.\*\.\*/);
  assert.match(page, /IPv4/);
  assert.match(page, /<span style="color:#c8faf4;font-weight:700">\s*延迟<\/span>/);
  assert.match(page, /<span style="color:#c8faf4;font-weight:700">\s*单线程上传<\/span>/);
  assert.match(page, /<span style="color:#c8faf4;font-weight:700">\s*单线程下载<\/span>/);
  assert.match(page, /<span style="color:#c8faf4;font-weight:700">湖北<\/span>/);
  assert.match(page, /<span style="color:#70a598">\s*电信<\/span>/);
  assert.match(page, /<span style="color:#9eff6e">\s*8ms<\/span>/);
  assert.match(page, /<span style="color:rgb\(255,165,0\)">\s*150\.50Mbps<\/span>/);
  assert.match(page, /<span style="color:#9eff6e;font-weight:700">\s*200Mbps ✓<\/span>/);
  assert.match(page, /<span style="color:#70a598">下载流量 <\/span><span style="color:#9eff6e">100\.00 MB<\/span> \/ /);
  assert.match(page, /报告时间：/);
  assert.match(page, /200Mbps ✓/);
  assert.match(page, /测速配置：单线程 \/ 200 Mbps 档位/);
  assert.doesNotMatch(page, /每方向 5 秒/);
  assert.match(page, /实际流量/);
  assert.doesNotMatch(page, /达标线：|统计口径：/);
  assert.match(page, /150\.50Mbps/);
  assert.doesNotMatch(page, /\[失败\]|没有可连接的候选节点|上传未产生有效数据/);
  assert.match(page, /769ms/);
  assert.match(page, /0\.04Mbps/);
  assert.match(page, />\s*失败<\/span>/);
  assert.doesNotMatch(page, /<table/);
  assert.equal(NODE_CORE.feedback.length, 1);
  assert.equal(NODE_CORE.feedback[0].measurements[0].lease_id, "lease_fixture_123");
  assert.equal(NODE_CORE.feedback[0].measurements[0].node_id, "0123456789abcdef0123456789abcdef");
  assert.equal(NODE_CORE.feedback[0].measurements[0].download_bytes, 124500000);
  assert.equal(NODE_CORE.feedback[0].measurements[0].upload_bytes, 94062500);
  assert.equal(NODE_CORE.feedback[0].measurements[1].node_id, "abcdef0123456789abcdef0123456789");
  assert.equal(NODE_CORE.feedback[0].measurements[1].status, "failed");
  const savedReports = JSON.parse(DB.reports.get(reportID).speed_data);
  assert.equal(savedReports[0].results[2].error, "上传未产生有效数据");
});

test("structured result target must match the session", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const token = (await (await worker.fetch(sessionRequest(), env)).text()).trim();
  const speedReport = JSON.parse(structuredSpeedData());
  speedReport.target_mbps = 400;
  const response = await worker.fetch(resultRequest(basicFields({
    ip_mode: "v4",
    speed_url: "",
    speed_data: JSON.stringify(speedReport),
  }), { authorization: `Bearer ${token}` }), env);
  assert.equal(response.status, 403);
});

test("run endpoint injects its origin, version, and feature availability", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(
    'REPORT_BASE="__SPEEDQUALITY_REPORT_BASE__"\n' +
      'PROBE_VERSION="__SPEEDQUALITY_PROBE_VERSION__"\n' +
      'NQ_ENABLED="__SPEEDQUALITY_NQ_BINDING_ENABLED__"\n',
    { status: 200 },
  );
  try {
    for (const enabled of [true, false]) {
      const response = await worker.fetch(new Request("https://rtw.example/run"), {
        GITHUB_OWNER: "owner",
        GITHUB_REPO: "rtw",
        GITHUB_REF: "main",
        PROBE_VERSION: "v1.2.3",
        NQ_BINDING_ENABLED: String(enabled),
      });
      assert.equal(response.status, 200);
      assert.equal(
        await response.text(),
        'REPORT_BASE="https://rtw.example"\nPROBE_VERSION="v1.2.3"\n' +
          `NQ_ENABLED="${enabled ? "1" : "0"}"\n`,
      );
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("node installer endpoint injects its origin and release version", async () => {
  const originalFetch = globalThis.fetch;
  let requested = "";
  globalThis.fetch = async (request) => {
    requested = String(request);
    return new Response(
      'SERVICE_BASE="__SPEEDQUALITY_REPORT_BASE__"\n' +
        'NODE_VERSION="__SPEEDQUALITY_PROBE_VERSION__"\n',
      { status: 200 },
    );
  };
  try {
    const response = await worker.fetch(new Request("https://rtw.example/install-node"), {
      GITHUB_OWNER: "owner",
      GITHUB_REPO: "rtw",
      GITHUB_REF: "main",
      PROBE_VERSION: "v1.2.3",
    });
    assert.equal(response.status, 200);
    assert.equal(
      requested,
      "https://raw.githubusercontent.com/owner/rtw/main/install-node.sh",
    );
    assert.equal(
      await response.text(),
      'SERVICE_BASE="https://rtw.example"\nNODE_VERSION="v1.2.3"\n',
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("binary endpoint proxies only configured release assets", async () => {
  const originalFetch = globalThis.fetch;
  let requested = "";
  globalThis.fetch = async (request) => {
    requested = String(request);
    return new Response("binary", { status: 200 });
  };
  try {
    const env = {
      GITHUB_OWNER: "owner",
      GITHUB_REPO: "speedquality",
      PROBE_VERSION: "v1.0.0",
    };
    const response = await worker.fetch(
      new Request("https://rtw.example/bin/v1.0.0/sqprobe-linux-amd64"),
      env,
    );
    assert.equal(response.status, 200);
    assert.equal(await response.text(), "binary");
    assert.equal(
      requested,
      "https://github.com/owner/speedquality/releases/download/v1.0.0/sqprobe-linux-amd64",
    );
    const nodeBinary = await worker.fetch(
      new Request("https://rtw.example/bin/v1.0.0/sq-node-linux-arm64"),
      env,
    );
    assert.equal(nodeBinary.status, 200);
    assert.equal(
      requested,
      "https://github.com/owner/speedquality/releases/download/v1.0.0/sq-node-linux-arm64",
    );
    const missing = await worker.fetch(
      new Request("https://rtw.example/bin/v1.0.0/other-file"),
      env,
    );
    assert.equal(missing.status, 404);
    const wrongVersion = await worker.fetch(
      new Request("https://rtw.example/bin/v0.9.0/sqprobe-linux-amd64"),
      env,
    );
    assert.equal(wrongVersion.status, 503);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("report copy formats support plain text, NodeSeek, and general Markdown", () => {
  const report = {
    id: "CopyDemo1234",
    source_ip_masked: "203.0.*.*",
    source_asn: 64500,
    source_as_organization: "Example Network",
    tested_at: 1780000000,
    regions: "湖北",
    target_mbps: 200,
    duration_seconds: 5,
    version: "v1.0.0",
    speed_text: "\u001b[32m湖北电信 200Mbps ✓\u001b[0m\n</textarea>",
    speed_data: "",
    traffic_rx_bytes: 1_000_000_000,
    traffic_tx_bytes: 500_000_000,
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
  };
  const snapshot = {
    pages: [
      {
        id: "all",
        title: "全部",
        format: "ansi",
        content: "\u001b[36mNodeQuality 全部结果\u001b[0m\n```",
        image_url: "",
      },
      {
        id: "basic",
        title: "基本信息",
        format: "ansi",
        content: "\u001b[32mNodeQuality 基本信息\u001b[0m\n```",
        image_url: "",
      },
      {
        id: "network-quality",
        title: "网络质量",
        format: "image",
        content: "",
        image_url: "https://images.example/network.webp",
      },
    ],
  };
  const copies = formatReportCopies(report, {
    snapshot,
    reportUrl: "https://sq.example.com/r/CopyDemo1234",
    projectUrl: "https://github.com/owner/speedquality",
  });

  for (const copy of Object.values(copies)) {
    assert.match(copy, /SpeedQuality 测速报告：/);
    assert.match(copy, /203\.0\.\*\.\*/);
    assert.match(copy, /\[NodeQuality链接\]\(https:\/\/nodequality\.com\/r\/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB\)/);
    assert.match(copy, /\[SpeedQuality链接\]\(https:\/\/sq\.example\.com\/r\/CopyDemo1234\)$/);
    assert.doesNotMatch(copy, /GitHub 项目链接|github\.com/);
    assert.doesNotMatch(copy, /分省三网|taierspeedtest|AS64500|Example Network/);
  }

  assert.match(copies.text, /^NodeQuality 全部结果/);
  assert.match(copies.text, /NodeQuality 全部结果/);
  assert.match(copies.text, /湖北电信 200Mbps ✓/);
  assert.match(copies.text, /实际流量：下载流量 1\.00 GB \/ 上传流量 500\.00 MB \/ 合计流量 1\.50 GB/);
  assert.match(copies.text, /\[NodeQuality链接\]\(https:\/\/nodequality\.com\/r\/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB\)/);
  assert.match(copies.text, /\[SpeedQuality链接\]\(https:\/\/sq\.example\.com\/r\/CopyDemo1234\)/);
  assert.doesNotMatch(copies.text, /\u001b/);

  assert.match(copies.nodeseek, /^:::: tabs/);
  assert.match(copies.nodeseek, /::: tab-item 基本信息\n````ansi\n\u001b\[32mNodeQuality 基本信息/);
  assert.match(copies.nodeseek, /::: tab-item 速度质量\n```ansi/);
  assert.match(copies.nodeseek, /!\[NodeQuality 网络质量\]\(https:\/\/images\.example\/network\.webp\)/);
  assert.match(copies.nodeseek, /\n::::\n\n\[NodeQuality链接\]/);
  assert.doesNotMatch(copies.nodeseek, /::: tab-item 全部|\[details=/);

  assert.match(copies.markdown, /^# 基本信息\n````text\nNodeQuality 基本信息/);
  assert.match(copies.markdown, /# 网络质量\n!\[NodeQuality 网络质量\]/);
  assert.match(copies.markdown, /# 速度质量\n```text/);
  assert.doesNotMatch(copies.markdown, /:::: tabs|::: tab-item|\[details=|\u001b/);
  assert.match(copies.markdown, /<\/textarea>/);
});

test("report network identity comes from the submitter's Cloudflare metadata", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const fields = basicFields({
    source_asn: "12345",
    source_as_organization: "Forged Network",
  });
  const token = await resultSessionToken(fields, env);
  const request = resultRequest(fields, { authorization: `Bearer ${token}` });
  Object.defineProperty(request, "cf", {
    value: { asn: 64500, asOrganization: "\u001b[31mExample\u001b[0m\nNetwork <script>" },
  });
  const response = await worker.fetch(request, env);
  assert.equal(response.status, 201);
  const reportUrl = (await response.text()).trim();
  const saved = DB.reports.get(reportUrl.split("/").pop());
  assert.equal(saved.source_asn, 64500);
  assert.equal(saved.source_as_organization, "Example Network <script>");

  const viewerRequest = new Request(reportUrl);
  Object.defineProperty(viewerRequest, "cf", {
    value: { asn: 64501, asOrganization: "Viewer Network" },
  });
  const page = await (await worker.fetch(viewerRequest, env)).text();
  assert.match(page, /203\.0\.\*\.\*/);
  assert.doesNotMatch(page, /AS64500|Example Network|Forged Network|Viewer Network|203\.0\.113\.9|<script>/);
});

test("standalone report is saved, rendered, and HTML escaped", async () => {
  const DB = new FakeD1();
  const env = {
    DB,
    RATE_LIMIT_SALT: "test-salt",
    GITHUB_OWNER: "owner",
    GITHUB_REPO: "speedquality",
  };
  const response = await submitResult(basicFields({
    tested_at: String(Date.parse("2026-10-09T16:05:09Z") / 1000),
  }), env);
  assert.equal(response.status, 201);
  const reportUrl = (await response.text()).trim();
  assert.match(reportUrl, /^https:\/\/rtw\.example\/r\/[A-Za-z0-9_-]{12}$/);

  const id = reportUrl.split("/").pop();
  const saved = DB.reports.get(id);
  assert.equal(saved.bind_status, "standalone");
  assert.equal(saved.nq_url, "");
  assert.equal(saved.source_ip_masked, "203.0.*.*");
  assert.equal(saved.source_asn, null);
  assert.equal(saved.source_as_organization, "");

  const pageResponse = await worker.fetch(new Request(`${reportUrl}?tab=speed`), env);
  const page = await pageResponse.text();
  assert.equal(pageResponse.status, 200);
  assert.match(page, /SpeedQuality 测速报告/);
  assert.match(page, /<body class="standalone-report">/);
  assert.match(page, /class="brand-wordmark sq-wordmark"/);
  assert.match(page, /class="speedquality-standalone-page single-page-report"/);
  assert.doesNotMatch(page, /<nav aria-label="报告分页">/);
  assert.doesNotMatch(page, />速度质量<\/a>/);
  assert.doesNotMatch(page, /NodeQuality/);
  assert.match(page, /湖北/);
  assert.match(page, /下载流量/);
  assert.match(page, /100\.00 MB/);
  assert.match(page, /上传流量/);
  assert.match(page, /50\.00 MB/);
  assert.match(page, /合计/);
  assert.match(page, /150\.00 MB/);
  assert.match(page, /测速配置：单线程 \/ 200 Mbps 档位/);
  assert.doesNotMatch(page, /每方向 5 秒/);
  assert.match(page, /实际流量/);
  assert.doesNotMatch(page, /达标线：|统计口径：/);
  assert.doesNotMatch(page, /aria-label="推广"|SpeedQuality 社区节点计划/);
  assert.equal((page.match(/>复制文本<\/button>/g) || []).length, 2);
  assert.equal((page.match(/>复制为NodeSeek格式<\/button>/g) || []).length, 2);
  assert.equal((page.match(/>复制为通用Markdown<\/button>/g) || []).length, 2);
  const downloadBaseName = `SpeedQuality_湖北_${id}`;
  const fileNames = [...page.matchAll(/data-download="([^"]+)"/g)].map((match) => match[1]);
  assert.deepEqual(fileNames, [
    `${downloadBaseName}_NodeSeek.md`, `${downloadBaseName}_Markdown.md`,
    `${downloadBaseName}_NodeSeek.md`, `${downloadBaseName}_Markdown.md`,
  ]);
  assert.equal((page.match(/class="general-md"/g) || []).length, 2);
  assert.equal((page.match(/class="copy-status"/g) || []).length, 1);
  const topCopyActions = page.indexOf('class="copy-actions copy-actions-top"');
  const reportContainer = page.indexOf('class="speedquality-standalone-page single-page-report"');
  const speedOutput = page.indexOf('class="ansi-output sq-output"');
  const bottomCopyActions = page.indexOf('class="copy-actions copy-actions-bottom"');
  assert.ok(topCopyActions < reportContainer);
  assert.ok(reportContainer < speedOutput);
  assert.ok(speedOutput < page.indexOf('aria-label="SpeedQuality 使用统计"'));
  assert.ok(page.indexOf('aria-label="SpeedQuality 使用统计"') < bottomCopyActions);
  assert.match(page, /navigator\.clipboard/);
  assert.match(page, /document\.execCommand\("copy"\)/);
  assert.match(page, /URL\.createObjectURL\(new Blob/);
  assert.doesNotMatch(page, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  const nonce = page.match(/<script nonce="([A-Za-z0-9_-]+)">/)?.[1];
  assert.ok(nonce);
  assert.match(
    pageResponse.headers.get("content-security-policy"),
    new RegExp(`script-src 'nonce-${nonce}'`),
  );
  assert.match(page, /https:\/\/github\.com\/owner\/speedquality/);
  assert.match(
    page,
    /<a href="https:\/\/github\.com\/owner\/speedquality" rel="noreferrer">GitHub 项目链接<\/a>/,
  );
  const terminal = page.match(/<pre class="ansi-output sq-output">([\s\S]*?)<\/pre>/)?.[1] || "";
  assert.match(terminal, /SpeedQuality 测速报告：/);
  assert.match(terminal, /203\.0\.\*\.\*/);
  assert.doesNotMatch(terminal, /github\.com/);
  assert.doesNotMatch(page, /sq-report-intro|分省三网|taierspeedtest/);
  assert.match(page, /<p class="report-links"><span>报告链接：[\s\S]*?<\/span><a [^>]+>GitHub 项目链接<\/a><\/p>/);
  assert.match(page, /\.brand-wordmark \{[^}]*padding-left:0; border-left:0;/);
  assert.match(page, /今日速度检测量：<strong>1<\/strong>；总检测量：<strong>1<\/strong>。感谢使用 SpeedQuality！/);
  assert.match(page, /报告链接：<a href="https:\/\/rtw\.example\/r\//);
  assert.doesNotMatch(page, /SQ Node Verification|验证规范/);
  assert.doesNotMatch(page, /<script>alert\(1\)<\/script>/);
  assert.equal(pageResponse.headers.get("referrer-policy"), "no-referrer");

  const script = page.match(/<script nonce="[A-Za-z0-9_-]+">([\s\S]*?)<\/script>/)?.[1];
  assert.ok(script);
  const copied = [];
  const downloads = [];
  const sources = new Map([
    ["copy-report-text", { value: "plain report" }],
    ["copy-report-nodeseek", { value: ":::: tabs\n::::" }],
    ["copy-report-markdown", { value: "# 速度质量" }],
  ]);
  const buttons = [
    { dataset: { copySource: "copy-report-text" }, textContent: "复制文本" },
    {
      dataset: { copySource: "copy-report-nodeseek", download: fileNames[0] },
      textContent: "复制为NodeSeek格式",
    },
    {
      dataset: { copySource: "copy-report-markdown", download: fileNames[1] },
      textContent: "复制为通用Markdown",
    },
  ];
  for (const button of buttons) {
    button.addEventListener = (_event, listener) => { button.listener = listener; };
  }
  let activeElement = null;
  const statuses = [{ textContent: "" }];
  const document = {
    body: { append: () => {} },
    querySelectorAll: (selector) => selector === ".copy-status" ? statuses : buttons,
    getElementById: (id) => sources.get(id),
    createElement: (tag) => {
      if (tag === "a") {
        return {
          href: "",
          download: "",
          click() { downloads.push({ href: this.href, filename: this.download }); },
          remove() {},
        };
      }
      return {
        value: "",
        style: {},
        setAttribute() {},
        select() { activeElement = this; },
        setSelectionRange() {},
        remove() {},
      };
    },
    execCommand: (command) => {
      if (command !== "copy" || !activeElement) return false;
      copied.push(activeElement.value);
      return true;
    },
  };
  const blobs = new Map();
  let blobIndex = 0;
  const objectUrl = {
    createObjectURL(blob) {
      const url = `blob:test-${blobIndex++}`;
      blobs.set(url, blob);
      return url;
    },
    revokeObjectURL() {},
  };
  let exportNow = Date.parse("2026-10-10T16:05:09.123Z");
  class ExportDate extends Date {
    static now() { return exportNow; }
  }
  runInNewContext(script, {
    Blob,
    Date: ExportDate,
    URL: objectUrl,
    clearTimeout() {},
    document,
    navigator: {},
    setTimeout() { return 1; },
    window: { isSecureContext: false },
  });
  await buttons[0].listener();
  assert.deepEqual(copied, ["plain report"]);
  assert.equal(downloads.length, 0);
  assert.deepEqual(statuses.map((status) => status.textContent), ["已复制：复制文本"]);
  await buttons[1].listener();
  await buttons[2].listener();
  assert.deepEqual(copied, ["plain report", ":::: tabs\n::::", "# 速度质量"]);
  assert.deepEqual(downloads.map((download) => download.filename), [
    `${downloadBaseName}_NodeSeek_导出20261011-000509-123.md`,
    `${downloadBaseName}_Markdown_导出20261011-000509-124.md`,
  ]);
  assert.equal(await blobs.get(downloads[0].href).text(), ":::: tabs\n::::");
  assert.equal(await blobs.get(downloads[1].href).text(), "# 速度质量");
  exportNow += 2000;
  await buttons[1].listener();
  assert.equal(downloads[2].filename, `${downloadBaseName}_NodeSeek_导出20261011-000511-123.md`);
  assert.equal(await blobs.get(downloads[2].href).text(), ":::: tabs\n::::");
});

test("successful reports increment daily and total usage counters", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const first = await submitResult(basicFields(), env);
  const second = await submitResult(basicFields(), env);
  assert.equal(first.status, 201);
  assert.equal(second.status, 201);
  assert.equal(DB.usageCounters.get("total")?.count, 2);
  const dailyCounters = [...DB.usageCounters.entries()].filter(([key]) => key.startsWith("day:"));
  assert.equal(dailyCounters.length, 1);
  assert.equal(dailyCounters[0][1].count, 2);
  const reportUrl = (await second.text()).trim();
  const page = await (await worker.fetch(new Request(reportUrl), env)).text();
  assert.match(page, /今日速度检测量：<strong>2<\/strong>；总检测量：<strong>2<\/strong>。感谢使用 SpeedQuality！/);
});

test("result creation requires a source-bound session", async () => {
  const response = await worker.fetch(resultRequest(basicFields()), {
    DB: new FakeD1(),
    RATE_LIMIT_SALT: "test-salt",
  });
  assert.equal(response.status, 401);
});

test("traffic metadata rejects incomplete, malformed, and out-of-range values", async () => {
  const onlyRx = basicFields();
  delete onlyRx.traffic_tx_bytes;
  const cases = [
    onlyRx,
    basicFields({ traffic_rx_bytes: "-1" }),
    basicFields({ traffic_tx_bytes: "not-a-number" }),
    basicFields({ duration_seconds: "4" }),
    basicFields({ duration_seconds: "14" }),
    basicFields({ target_mbps: "300" }),
  ];
  for (const fields of cases) {
    const response = await submitResult(fields, { DB: new FakeD1() });
    assert.equal(response.status, 400);
  }
});

test("reports created before traffic fields remain renderable", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const response = await submitResult(basicFields(), env);
  assert.equal(response.status, 201);
  const reportUrl = (await response.text()).trim();
  const report = DB.reports.get(reportUrl.split("/").pop());
  report.duration_seconds = null;
  report.target_mbps = null;
  report.traffic_rx_bytes = null;
  report.traffic_tx_bytes = null;
  const page = await (await worker.fetch(
    new Request(reportUrl),
    { DB },
  )).text();
  assert.doesNotMatch(page, /实际流量/);
});

test("verified stale NodeQuality report is linked with a warning", async () => {
  const DB = new FakeD1();
  const testedAt = Math.floor(Date.now() / 1000);
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const response = await submitResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified_stale",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 4200),
    nq_time_source: "network.log",
    time_gap_seconds: "4200",
    nq_identity_reason: "full_ip",
  }), env);
  assert.equal(response.status, 201);
  const reportUrl = (await response.text()).trim();
  const page = await (await worker.fetch(new Request(`${reportUrl}?tab=sq`), { DB })).text();
  assert.match(page, /超过 60 分钟/);
  assert.match(page, /完整 IP 一致/);
  assert.match(page, /服务器身份已校验，检测时间差较大/);
  assert.match(page, /class="notice warning"/);
  assert.match(page, /查看 NodeQuality 原始报告/);
});

test("disabled NodeQuality binding forces old clients to a standalone report", async () => {
  const DB = new FakeD1();
  const SNAPSHOTS = new FakeR2();
  const testedAt = Math.floor(Date.now() / 1000);
  const env = {
    DB,
    SNAPSHOTS,
    RATE_LIMIT_SALT: "test-salt",
    NQ_BINDING_ENABLED: "false",
  };
  const response = await submitMultipartResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 60),
    nq_time_source: "header_info.log",
    time_gap_seconds: "60",
    nq_identity_reason: "full_ip",
  }), { invalid: "snapshot must be ignored" }, env);

  assert.equal(response.status, 201);
  const id = (await response.text()).trim().split("/").pop();
  const report = DB.reports.get(id);
  assert.equal(report.bind_status, "standalone");
  assert.equal(report.nq_url, "");
  assert.equal(report.nq_tested_at, null);
  assert.equal(report.nq_identity_reason, "");
  assert.equal(SNAPSHOTS.objects.size, 0);
});

test("NodeQuality snapshot is sanitized, stored in R2, and rendered in its tab", async () => {
  const DB = new FakeD1();
  const SNAPSHOTS = new FakeR2();
  const largeText = "\u001b[32m100Mbps\u001b[0m\n".repeat(2500);
  const fields = basicFields({
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(Math.floor(Date.now() / 1000) - 60),
    nq_time_source: "header_info.log",
    time_gap_seconds: "60",
    nq_identity_reason: "masked_ip_and_asn",
  });
  const snapshot = {
    version: 2,
    truncated: false,
    omitted_files: 0,
    pages: [
      {
        id: "all",
        title: "全部",
        format: "ansi",
        content: `\u001b[36mNodeQuality header\u001b[0m\r\r\nAll sections\n${largeText}ALL-END`,
        image_url: "",
        source: "header_info.log,hardware_quality.log,ip_quality.log,net_quality.log,backroute_trace.log",
        truncated: false,
      },
      {
        id: "basic",
        title: "基本信息",
        format: "ansi",
        content: `  aligned text\n\u001b[32mBasic info\u001b[0m\n${largeText}BASIC-END`,
        image_url: "",
        source: "nodequality.md",
        truncated: false,
      },
      {
        id: "ip-quality",
        title: "IP质量",
        format: "ansi",
        content: "\u001b[31mIP quality\u001b[0m\u001b]0;unsafe\u0007\u0001\n<script>alert(1)</script>",
        image_url: "",
        source: "nodequality.md",
        truncated: false,
      },
      {
        id: "network-quality",
        title: "网络质量",
        format: "image",
        content: "",
        image_url: "https://images.example/network.webp",
        source: "nodequality.md",
        truncated: false,
      },
      {
        id: "return-route",
        title: "回程路由",
        format: "image",
        content: "",
        image_url: "https://images.example/route.png",
        source: "nodequality.md",
        truncated: false,
      },
    ],
  };
  const env = {
    DB,
    SNAPSHOTS,
    RATE_LIMIT_SALT: "test-salt",
  };
  const snapshotBytes = new TextEncoder().encode(JSON.stringify(snapshot)).byteLength;
  assert.ok(snapshotBytes > 96 * 1024 && snapshotBytes <= 256 * 1024);
  const response = await submitMultipartResult(fields, snapshot, env);
  assert.equal(response.status, 201);
  assert.equal(response.headers.get("x-snapshot-store"), "stored");
  const reportUrl = (await response.text()).trim();
  const id = reportUrl.split("/").pop();
  assert.equal(SNAPSHOTS.objects.has(`nodequality/${id}.json.gz`), true);

  const pageResponse = await worker.fetch(
    new Request(`${reportUrl}?tab=nq-ip-quality`),
    { DB, SNAPSHOTS },
  );
  const page = await pageResponse.text();
  assert.equal(pageResponse.status, 200);
  assert.match(page, /aria-current="page">IP质量/);
  assert.ok(page.indexOf(">全部<") < page.indexOf(">基本信息<"));
  assert.ok(page.indexOf(">基本信息<") < page.indexOf(">IP质量<"));
  assert.ok(page.indexOf(">IP质量<") < page.indexOf(">网络质量<"));
  assert.ok(page.indexOf(">网络质量<") < page.indexOf(">回程路由<"));
  assert.ok(page.indexOf(">回程路由<") < page.indexOf(">速度质量<"));
  assert.match(page, /<span style="color:#bd0013">IP quality<\/span>/);
  assert.match(page, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.doesNotMatch(page, /<script>alert\(1\)<\/script>/);
  const visibleNQOutput = page.match(/<pre class="ansi-output nq-output">([\s\S]*?)<\/pre>/)?.[1] || "";
  assert.doesNotMatch(visibleNQOutput, /\u001b|\u0001/);
  assert.doesNotMatch(page, /aria-label="推广"/);
  assert.equal((page.match(/>复制文本<\/button>/g) || []).length, 2);
  assert.equal((page.match(/>复制为NodeSeek格式<\/button>/g) || []).length, 2);
  assert.equal((page.match(/>复制为通用Markdown<\/button>/g) || []).length, 2);
  const nqTopCopyActions = page.indexOf('class="copy-actions copy-actions-top"');
  const nqReportContainer = page.indexOf('class="nodequality-page"');
  const nqBottomCopyActions = page.indexOf('class="copy-actions copy-actions-bottom"');
  assert.ok(nqTopCopyActions < nqReportContainer);
  assert.ok(nqReportContainer < nqBottomCopyActions);
  assert.match(page, /navigator\.clipboard/);
  assert.doesNotMatch(page, /今日速度检测量/);
  assert.doesNotMatch(page, /服务器身份与时间均已校验/);
  assert.doesNotMatch(page, /报告将在/);
  assert.doesNotMatch(page, /<div class="sq-report-intro">/);

  const allPage = await (await worker.fetch(
    new Request(`${reportUrl}?tab=nq-all`),
    { DB, SNAPSHOTS },
  )).text();
  assert.match(allPage, /aria-current="page">全部/);
  assert.match(allPage, /NodeQuality header/);
  assert.match(allPage, /ALL-END/);
  assert.match(allPage, /BASIC-END/);
  assert.doesNotMatch(allPage, /\r/);
  assert.doesNotMatch(allPage, /aria-label="推广"/);
  assert.equal((allPage.match(/>复制文本<\/button>/g) || []).length, 2);

  const imagePage = await (await worker.fetch(
    new Request(`${reportUrl}?tab=nq-network-quality`),
    { DB, SNAPSHOTS },
  )).text();
  assert.match(imagePage, /aria-current="page">网络质量/);
  assert.match(imagePage, /src="https:\/\/images\.example\/network\.webp"/);
  assert.equal((imagePage.match(/>复制文本<\/button>/g) || []).length, 2);

  const speedPage = await (await worker.fetch(
    new Request(`${reportUrl}?tab=sq`),
    { DB, SNAPSHOTS },
  )).text();
  const speedMain = speedPage.indexOf('<main class="speedquality-addon-page">');
  const speedTabs = speedPage.indexOf('<div class="tabs-shell">');
  const speedOutput = speedPage.indexOf('class="sq-terminal-scroll"');
  assert.ok(speedMain < speedTabs);
  assert.ok(speedTabs < speedOutput);
  assert.match(speedPage, /aria-current="page">速度质量/);
  assert.match(speedPage, /aria-label="NodeQuality \+ SpeedQuality 联合报告"/);
  assert.match(speedPage, />基本信息</);
  assert.match(speedPage, /服务器身份与时间均已校验/);
  assert.match(speedPage, /脱敏 IP 网段与本次测速匹配，且 ASN 一致/);
  assert.doesNotMatch(speedPage, /aria-label="推广"|SpeedQuality 社区节点计划/);
  assert.match(speedPage, />复制文本<\/button>/);
  assert.match(speedPage, /NodeQuality header/);
  assert.match(speedPage, /今日速度检测量/);
  assert.match(speedPage, /报告将在/);
  assert.doesNotMatch(speedPage, /<div class="sq-report-intro">/);
});

test("NodeQuality snapshot rejects unsafe archive paths", async () => {
  const DB = new FakeD1();
  const fields = basicFields({
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(Math.floor(Date.now() / 1000) - 60),
    nq_time_source: "header_info.log",
    time_gap_seconds: "60",
  });
  const snapshot = {
    version: 1,
    truncated: false,
    omitted_files: 0,
    sections: [{
      name: "../secret.log",
      kind: "log",
      content: "unsafe",
      truncated: false,
    }],
  };
  const response = await submitMultipartResult(fields, snapshot, {
    DB,
    SNAPSHOTS: new FakeR2(),
  });
  assert.equal(response.status, 400);
  assert.equal(DB.reports.size, 0);
});

test("NodeQuality tab falls back to the original report when R2 is unavailable", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  const response = await submitResult(basicFields({
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(Math.floor(Date.now() / 1000) - 60),
    nq_time_source: "header_info.log",
    time_gap_seconds: "60",
  }), env);
  const reportUrl = (await response.text()).trim();
  const page = await (await worker.fetch(
    new Request(`${reportUrl}?tab=nodequality`),
    { DB },
  )).text();
  assert.match(page, /此联合报告暂时无法展示 NQ 内容/);
  assert.match(page, /不代表原始报告无法访问/);
  assert.match(page, /查看 NodeQuality 原始报告/);
  assert.doesNotMatch(page, /aria-label="推广"/);
  assert.doesNotMatch(page, /今日速度检测量/);
});

test("rejected binding statuses keep only a red reason and the standalone SQ report", async () => {
  const DB = new FakeD1();
  const env = { DB, RATE_LIMIT_SALT: "test-salt" };
  for (const [status, heading] of [
    ["mismatch", "服务器身份校验未通过"],
    ["unverified", "NodeQuality 报告无法校验"],
  ]) {
    const response = await submitResult(basicFields({
      bind_status: status,
      nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    }), env);
    assert.equal(response.status, 201);
    const reportUrl = (await response.text()).trim();
    const saved = DB.reports.get(reportUrl.split("/").pop());
    assert.equal(saved.bind_status, status);
    assert.equal(saved.nq_url, "");
    assert.equal(saved.nq_identity_reason, "");
    const page = await (await worker.fetch(new Request(reportUrl), env)).text();
    assert.match(page, new RegExp(heading));
    assert.match(page, /class="notice danger"/);
    assert.match(page, /本页仅展示 SpeedQuality 结果/);
    assert.doesNotMatch(page, /Node<span>Quality<\/span>/);
    assert.doesNotMatch(page, /报告分页/);
  }
});

test("invalid NodeQuality origin is rejected", async () => {
  const response = await worker.fetch(resultRequest(basicFields({
    bind_status: "verified",
    nq_url: "https://nodequality.com.evil.example/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: "1780000000",
    time_gap_seconds: "20",
  })), { DB: new FakeD1() });
  assert.equal(response.status, 400);
});

test("invalid NodeQuality identity reason is rejected", async () => {
  const testedAt = Math.floor(Date.now() / 1000);
  const response = await submitResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 60),
    nq_time_source: "header_info.log",
    time_gap_seconds: "60",
    nq_identity_reason: "trust_me",
  }), { DB: new FakeD1(), RATE_LIMIT_SALT: "test-salt" });
  assert.equal(response.status, 400);
});

test("60 minute verification boundary is enforced", async () => {
  const testedAt = Math.floor(Date.now() / 1000);
  const accepted = await submitResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 3600),
    time_gap_seconds: "3600",
  }), { DB: new FakeD1(), RATE_LIMIT_SALT: "test-salt" });
  assert.equal(accepted.status, 201);

  const rejected = await submitResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 3601),
    time_gap_seconds: "3601",
  }), { DB: new FakeD1(), RATE_LIMIT_SALT: "test-salt" });
  assert.equal(rejected.status, 400);

  const inconsistent = await submitResult(basicFields({
    tested_at: String(testedAt),
    bind_status: "verified",
    nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(testedAt - 300),
    time_gap_seconds: "30",
  }), { DB: new FakeD1(), RATE_LIMIT_SALT: "test-salt" });
  assert.equal(inconsistent.status, 400);
});

test("daily source limit is enforced", async () => {
  const DB = new FakeD1();
  const env = { DB, DAILY_RESULT_LIMIT: "1", RATE_LIMIT_SALT: "test-salt" };
  const first = await submitResult(basicFields(), env);
  const second = await submitResult(basicFields(), env);
  assert.equal(first.status, 201);
  assert.equal(second.status, 429);
});

test("oversized requests and unknown reports are rejected", async () => {
  const DB = new FakeD1();
  const tooLarge = await worker.fetch(resultRequest(basicFields({
    speed_text: "x".repeat(600 * 1024),
  })), { DB });
  assert.equal(tooLarge.status, 413);

  const missing = await worker.fetch(
    new Request("https://rtw.example/r/AbCdEfGhIjKl"),
    { DB },
  );
  assert.equal(missing.status, 404);
});

test("structured logs redact credentials, endpoints, and client addresses", () => {
  const records = [];
  const originalLog = console.log;
  console.log = (value) => records.push(value);
  try {
    logEvent({ LOG_LEVEL: "info" }, "info", "test.event", {
      request_id: "edge-test-request",
      node_id: "01234567",
      api_token: `sqa_${"a".repeat(32)}`,
      route_key: `sqn_${"r".repeat(32)}`,
      client_ip: "203.0.113.9",
      detail: "GET https://192.0.2.10/activate?key=secret from 2001:db8::1 eyJabc.def.ghi",
    });
  } finally {
    console.log = originalLog;
  }
  assert.equal(records.length, 1);
  const record = JSON.parse(records[0]);
  assert.equal(record.request_id, "edge-test-request");
  assert.equal(record.node_id, "01234567");
  assert.equal(record.api_token, undefined);
  assert.equal(record.route_key, undefined);
  assert.equal(record.client_ip, undefined);
  assert.doesNotMatch(records[0], /192\.0\.2\.10|2001:db8|eyJabc|sqa_|sqn_/);
});

test("scheduled cleanup removes expired reports and their R2 snapshots", async () => {
  const DB = new FakeD1();
  const SNAPSHOTS = new FakeR2();
  const now = Math.floor(Date.now() / 1000);
  DB.reports.set("Expired12345", { id: "Expired12345", expires_at: now - 1 });
  DB.reports.set("Current123456", { id: "Current123456", expires_at: now + 3600 });
  await SNAPSHOTS.put("nodequality/Expired12345.json.gz", new Uint8Array([1]));
  await SNAPSHOTS.put("nodequality/Current123456.json.gz", new Uint8Array([2]));

  let cleanupTask;
  await worker.scheduled({}, { DB, SNAPSHOTS }, {
    waitUntil(task) {
      cleanupTask = task;
    },
  });
  await cleanupTask;

  assert.equal(DB.reports.has("Expired12345"), false);
  assert.equal(DB.reports.has("Current123456"), true);
  assert.equal(SNAPSHOTS.objects.has("nodequality/Expired12345.json.gz"), false);
  assert.equal(SNAPSHOTS.objects.has("nodequality/Current123456.json.gz"), true);
});
