import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath } from "node:url";
import worker, { formatReportCopies, renderReport, validateSpeedData } from "../src/index.js";
import { sortSQReports, linkedNQContext } from "../src/report-link.js";
import { retransmissionText } from "../src/retransmission.js";
import { SQLiteDatabase } from "../../vps/lib/sqlite.mjs";

const base = "https://sq.example";
const address = "203.0.113.9";
const now = () => Math.floor(Date.now() / 1000);
const sourceData = (stats) => JSON.stringify({
  version: 1, lease_id: "lease_example_123", started_at: now() - 15, completed_at: now(),
  region: { code: "hb", name: "湖北" }, family: "v4", duration_seconds: 5, target_mbps: 200, modes: ["s"],
  results: [{ carrier: "ct", label: "湖北电信", status: "ok", latency_ms: 15,
    single: { download_mbps: 120, upload_mbps: 150, download_bytes: 10000000, upload_bytes: 10000000,
      ...(stats !== undefined ? { upload_retransmission: stats } : {}) } }],
});
function environment(t) {
  const DB = new SQLiteDatabase(":memory:");
  DB.migrate(fileURLToPath(new URL("../migrations/", import.meta.url)));
  t.after(() => DB.close());
  return { DB, RATE_LIMIT_SALT: "test-only-report-identity-key", NODEQUALITY_BINDING_ENABLED: "true" };
}
function post(path, fields, ip = address, headers = {}) {
  return new Request(`${base}${path}`, { method: "POST", headers: { "cf-connecting-ip": ip, ...headers },
    body: new URLSearchParams(fields) });
}
async function submit(env, overrides = {}, ip = address) {
  const fields = { regions: "hb", mode: "s", ip_mode: "v4", duration_seconds: "5", target_mbps: "200" };
  const session = await worker.fetch(post("/api/session", fields, ip), env);
  assert.equal(session.status, 201, await session.clone().text());
  return worker.fetch(post("/api/results", {
    ...fields, regions: "湖北", tested_at: String(now()), version: "1.6.0", bind_status: "standalone",
    speed_data: sourceData(), ...overrides,
  }, ip, { authorization: `Bearer ${(await session.text()).trim()}` }), env);
}
async function create(env, overrides = {}) {
  const response = await submit(env, overrides);
  assert.equal(response.status, 201, await response.clone().text());
  const url = (await response.text()).trim();
  return { url, id: url.split("/").at(-1) };
}
const preflight = (env, url, ip = address) => worker.fetch(post("/api/reports/preflight", { sq_url: url }, ip), env);
const read = (env, id) => env.DB.prepare("SELECT * FROM reports WHERE id = ? AND expires_at > ?").bind(id, now()).first();

test("SQ identity is private, preflight binds one original report, and a pair cannot grow", async (t) => {
  const env = environment(t);
  const original = await create(env);
  const stored = await read(env, original.id);
  assert.equal(JSON.parse(stored.source_identity_hashes).length, 1);
  assert.doesNotMatch(stored.source_identity_hashes, /203\.0\.113\.9/);
  const check = await preflight(env, `${original.url}?tab=sq`);
  assert.equal(check.status, 200);
  const plan = await check.json();
  assert.equal(plan.sq_limit, 2);
  assert.equal(plan.sq_count, 2);
  const combined = await create(env, { sq_preflight_token: plan.token });
  const linked = await read(env, combined.id);
  assert.equal(linked.sq_source_id, original.id);
  assert.equal((await read(env, original.id)).sq_source_id, "");
  const rejected = await preflight(env, combined.url);
  assert.equal(rejected.status, 409);
  assert.equal((await rejected.json()).error, "sq_limit_exceeded");
  const page = await (await worker.fetch(new Request(`${combined.url}?tab=sq`), env)).text();
  assert.match(page, /class="sq-time-tabs"/);
  assert.doesNotMatch(page, new RegExp(JSON.parse(stored.source_identity_hashes)[0]));
});

test("preflight rejects legacy, expired, foreign, mismatched, and multiple sources", async (t) => {
  const env = environment(t);
  const original = await create(env);
  assert.equal((await preflight(env, original.url, "198.51.100.9")).status, 403);
  assert.equal((await preflight(env, original.url.replace(base, "https://foreign.example"))).status, 400);
  const fields = new URLSearchParams([['sq_url', original.url], ['sq_url', original.url]]);
  assert.equal((await worker.fetch(post("/api/reports/preflight", fields), env)).status, 400);
  await env.DB.prepare("UPDATE reports SET source_identity_hashes = '[]' WHERE id = ?").bind(original.id).run();
  assert.equal((await preflight(env, original.url)).status, 422);
  await env.DB.prepare("UPDATE reports SET expires_at = ? WHERE id = ?").bind(now() - 1, original.id).run();
  assert.equal((await preflight(env, original.url)).status, 404);
});

test("link proof cannot be tampered with or submitted from another source", async (t) => {
  const env = environment(t);
  const original = await create(env);
  const { token } = await (await preflight(env, original.url)).json();
  const wrongSignature = `${token.slice(0, -1)}${token.endsWith("0") ? "1" : "0"}`;
  assert.equal((await submit(env, { sq_preflight_token: wrongSignature })).status, 403);
  assert.equal((await submit(env, { sq_preflight_token: token }, "198.51.100.9")).status, 403);
  const future = Date.now() + 7 * 3600 * 1000;
  t.mock.method(Date, "now", () => future);
  assert.equal((await submit(env, { sq_preflight_token: token })).status, 403);
});

test("dual-stack reports retain both authenticated identities and accept either for preflight", async (t) => {
  const env = environment(t);
  const fields = { regions: "hb", mode: "s", ip_mode: "v6", duration_seconds: "5", target_mbps: "200" };
  const v6 = "2001:db8::9";
  const first = await worker.fetch(post("/api/session", fields), env);
  const second = await worker.fetch(post("/api/session", fields, v6), env);
  const submitted = await worker.fetch(post("/api/results", {
    ...fields, tested_at: String(now()), version: "1.6.0", bind_status: "standalone", speed_data: sourceData(),
  }, address, { authorization: `Bearer ${(await first.text()).trim()}`,
    "x-sq-peer-session": (await second.text()).trim() }), env);
  assert.equal(submitted.status, 201, await submitted.clone().text());
  const url = (await submitted.text()).trim();
  const report = await read(env, url.split("/").at(-1));
  assert.equal(JSON.parse(report.source_identity_hashes).length, 2);
  assert.equal((await preflight(env, url)).status, 200);
  assert.equal((await preflight(env, url, v6)).status, 200);
});

test("expiry after preflight preserves the new measurement and shows an unavailable source", async (t) => {
  const env = environment(t);
  const original = await create(env);
  const { token } = await (await preflight(env, original.url)).json();
  await env.DB.prepare("DELETE FROM reports WHERE id = ?").bind(original.id).run();
  const combined = await create(env, { sq_preflight_token: token });
  const page = await (await worker.fetch(new Request(`${combined.url}?tab=sq&sq=${original.id}`), env)).text();
  assert.match(page, /原始 SQ 报告已过期或不可用/);
  assert.match(page, /本次/);
});

test("time selectors and all exports sort by Beijing clock time, retaining dates and one NQ", () => {
  const evening = { id: "Evening12345", tested_at: Date.parse("2026-10-13T21:40:00+08:00") / 1000,
    regions: "湖北", version: "1.6.0", speed_text: "EVENING_RESULT", target_mbps: 200, duration_seconds: 5,
    nq_url: "https://nodequality.com/r/ExampleNQReport1234", nq_tested_at: Date.parse("2026-10-13T21:35:00+08:00") / 1000,
    bind_status: "verified", time_gap_seconds: 300, nq_identity_reason: "full_ip" };
  const morning = { ...evening, id: "Morning12345", tested_at: Date.parse("2026-10-16T08:10:00+08:00") / 1000,
    speed_text: "MORNING_RESULT" };
  const options = { tab: "sq", sqReports: [evening, morning], reportUrl: `${base}/r/${evening.id}`,
    snapshot: { pages: [{ id: "basic", title: "基本信息", format: "ansi", content: "NQ_ONCE" }] } };
  assert.deepEqual(sortSQReports(options.sqReports).map(r => r.id), [morning.id, evening.id]);
  assert.deepEqual(sortSQReports([morning, { ...evening, tested_at: morning.tested_at }]).map(r => r.id), [morning.id, evening.id]);
  const html = renderReport(evening, { ...options, sqID: morning.id });
  const nav = html.match(/<nav class="sq-time-tabs"[\s\S]*?<\/nav>/)[0];
  assert.ok(nav.indexOf("08:10") < nav.indexOf("21:40"));
  assert.match(nav, /2026\/10\/16/);
  assert.match(nav, /2026\/10\/13/);
  assert.equal(linkedNQContext(evening, morning).bind_status, "verified_stale");
  assert.equal(linkedNQContext(evening, morning).nq_identity_reason, "linked_sq");
  assert.equal(linkedNQContext(evening, evening).nq_identity_reason, "full_ip");
  for (const copy of Object.values(formatReportCopies(evening, options))) {
    assert.ok(copy.indexOf("MORNING_RESULT") < copy.indexOf("EVENING_RESULT"));
    assert.equal(copy.split("NQ_ONCE").length - 1, 1);
  }
});

test("TCP retransmission percentage uses bytes, preserves tiny values, and rejects invalid counters", () => {
  assert.equal(retransmissionText({ sent_bytes: 105000000, retransmitted_bytes: 5000000 }), "4.76%");
  assert.equal(retransmissionText({ sent_bytes: 1000000, retransmitted_bytes: 1 }), "<0.01%");
  assert.equal(retransmissionText({ sent_bytes: 100, retransmitted_bytes: 0 }), "0.00%");
  assert.equal(retransmissionText(null), "—");
  assert.ok(validateSpeedData(sourceData({ sent_bytes: 100, retransmitted_bytes: 101 })).error);
  assert.ok(validateSpeedData(sourceData({ sent_bytes: 0, retransmitted_bytes: 0 })).error);
  const data = validateSpeedData(sourceData({ sent_bytes: 1000000, retransmitted_bytes: 1 }));
  assert.ok(!data.error);
  const report = { speed_data: data.value, target_mbps: 200 };
  for (const copy of Object.values(formatReportCopies(report))) assert.match(copy, /<0\.01%/);
  const html = renderReport(report);
  assert.match(html, /&lt;0\.01%/);
});
