import test from "node:test";
import assert from "node:assert/strict";
import { fork } from "node:child_process";
import { once } from "node:events";
import { join } from "node:path";
import { readFileSync, writeFileSync, existsSync, readdirSync, unlinkSync, statSync } from "node:fs";
import { startPublicServer } from "../server.mjs";
import { initialize, enableRedis, importD1, backupData, restoreData } from "../admin.mjs";
import { SQLiteDatabase } from "../lib/sqlite.mjs";
import { temporary, config, sessionFields, resultFields, migrations, dump } from "./helpers.mjs";

async function start(t, settings) {
  const app = await startPublicServer(settings, { resolveGeo: async () => ({ asn: 4134, asOrganization: "fixture" }) });
  let stopped = false;
  const stop = async () => { if (!stopped) { stopped = true; await app.stop(); } };
  t.after(stop);
  return { ...app, stop, url: `http://127.0.0.1:${app.address.port}` };
}
async function submit(t, app, { nq = false, headers = {} } = {}) {
  const session = await fetch(app.url + "/api/session", { method: "POST", body: new URLSearchParams(sessionFields), headers });
  assert.equal(session.status, 201, await session.clone().text());
  const token = (await session.text()).trim();
  const lease = await fetch(app.url + "/api/node-lease", { method: "POST", headers: { ...headers, authorization: `Bearer ${token}` }, body: new URLSearchParams({ region: "hb", family: "v4" }) });
  assert.equal(lease.status, 200, await lease.clone().text());
  const fields = resultFields();
  if (nq) Object.assign(fields, { bind_status: "verified", nq_url: "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB",
    nq_tested_at: String(Number(fields.tested_at) - 60), nq_time_source: "header_info.log", time_gap_seconds: "60", nq_identity_reason: "masked_ip_and_asn" });
  const body = new FormData();
  for (const [key, value] of Object.entries(fields)) body.set(key, value);
  if (nq) body.set("nq_snapshot", new Blob([JSON.stringify({ version: 2, truncated: false, omitted_files: 0,
    pages: [{ id: "all", title: "全部", format: "ansi", content: "NodeQuality persistent fixture", image_url: "", source: "header_info.log", truncated: false }] })], { type: "application/json" }), "nodequality.json");
  const result = await fetch(app.url + "/api/results", { method: "POST", headers: { ...headers, authorization: `Bearer ${token}` }, body });
  assert.equal(result.status, 201, await result.clone().text());
  if (nq) assert.equal(result.headers.get("x-snapshot-store"), "stored");
  return (await result.text()).trim();
}

test("HTTP session, lease and NQ report survive restart, D1 migration and backup/restore", async (t) => {
  const dir = temporary(t), publicDir = join(dir, "public");
  const app = await start(t, config(publicDir));
  const report = await submit(t, app, { nq: true });
  const path = new URL(report).pathname;
  assert.match(await (await fetch(app.url + path + "?tab=nq-all")).text(), /NodeQuality persistent fixture/);
  const count = await app.database.prepare("SELECT count(*) AS count FROM reports").first("count");
  assert.equal(count, 1);
  const exportPath = join(dir, "d1.sql");
  writeFileSync(exportPath, dump(app.database.database));
  const imported = importD1({ input: exportPath, output: join(dir, "imported/reports.sqlite") });
  const importedDB = new SQLiteDatabase(imported);
  assert.equal(await importedDB.prepare("SELECT count(*) AS count FROM reports").first("count"), 1);
  importedDB.migrate(migrations); importedDB.close();
  assert.throws(() => importD1({ input: exportPath, output: imported }), /已存在/);
  writeFileSync(join(dir, "no-index.sql"), readFileSync(exportPath, "utf8").replace(/CREATE INDEX[^;]+;/g, ""));
  assert.throws(() => importD1({ input: join(dir, "no-index.sql"), output: join(dir, "bad.sqlite") }), /表结构/);
  assert.equal(existsSync(join(dir, "bad.sqlite")), false);
  const backupDir = join(dir, "backup");
  const manifest = await backupData({ publicDir, output: backupDir });
  assert.deepEqual(manifest.missing_snapshots, []);
  const restored = join(dir, "restored");
  restoreData({ input: backupDir, publicDir: restored });
  assert.throws(() => restoreData({ input: backupDir, publicDir: restored }), /空目录/);
  await app.stop();
  const restarted = await start(t, config(restored));
  const page = await fetch(restarted.url + path + "?tab=nq-all");
  assert.equal(page.status, 200); assert.match(await page.text(), /NodeQuality persistent fixture/);
  const extra = join(backupDir, "snapshots/Unexpected12.json.gz"); writeFileSync(extra, "unexpected");
  assert.throws(() => restoreData({ input: backupDir, publicDir: join(dir, "extra") }), /未经清单/);
  assert.equal(existsSync(join(dir, "extra")), false); unlinkSync(extra);
  const file = readdirSync(join(backupDir, "snapshots"))[0];
  writeFileSync(join(backupDir, "snapshots", file), "tampered");
  assert.throws(() => restoreData({ input: backupDir, publicDir: join(dir, "tampered") }), /校验失败/);
});

test("session source binding still works through the trusted VPS proxy", async (t) => {
  const secret = "proxy-fixture-secret-1234567890123456";
  const app = await start(t, config(temporary(t), { PROXY_SECRET: secret }));
  const headers = { "x-sq-proxy-secret": secret, "x-sq-client-ip": "8.8.8.8" };
  const response = await fetch(app.url + "/api/session", { method: "POST", headers, body: new URLSearchParams(sessionFields) });
  assert.equal(response.status, 201);
  const token = (await response.text()).trim();
  const lease = await fetch(app.url + "/api/node-lease", { method: "POST", body: new URLSearchParams({ region: "hb", family: "v4" }),
    headers: { ...headers, "x-sq-client-ip": "8.8.4.4", "cf-connecting-ip": "8.8.8.8", authorization: `Bearer ${token}` } });
  assert.equal(lease.status, 401);
});

test("two independent processes enforce a shared SQLite daily session limit", async (t) => {
  const settings = config(temporary(t), { DAILY_SESSION_LIMIT: "5" });
  const children = [];
  t.after(async () => {
    await Promise.all(children.map(async (child) => { const stopped = once(child, "exit"); child.send("stop"); await stopped; }));
  });
  const ports = [];
  for (let index = 0; index < 2; index++) {
    const child = fork(new URL("server-child.mjs", import.meta.url), [], { stdio: ["ignore", "ignore", "ignore", "ipc"] });
    children.push(child); const message = once(child, "message"); child.send(settings);
    const [started] = await message; assert.equal(started.error, undefined); ports.push(started.port);
  }
  const statuses = await Promise.all(Array.from({ length: 24 }, async (_, i) => {
    const response = await fetch(`http://127.0.0.1:${ports[i % 2]}/api/session`, { method: "POST", body: new URLSearchParams(sessionFields) });
    await response.text(); return response.status;
  }));
  assert.equal(statuses.filter((s) => s === 201).length, 5, String(statuses));
  assert.equal(statuses.filter((s) => s === 429).length, 19, String(statuses));
});

test("init creates private independent secrets and refuses regeneration", (t) => {
  const dir = join(temporary(t), "config"); initialize({ dir, origin: "https://sq.example" });
  const pub = JSON.parse(readFileSync(join(dir, "public.json"))), core = JSON.parse(readFileSync(join(dir, "core.json")));
  assert.equal(pub.NODE_CORE_SECRET, core.INTERNAL_SECRET);
  assert.notEqual(pub.RATE_LIMIT_SALT, core.NODE_ID_SALT);
  assert.equal(pub.TASK_SCHEDULER_ENABLED, "true");
  assert.equal(JSON.parse(core.NODE_JWT_PRIVATE_JWK).crv, "P-256");
  assert.equal(statSync(join(dir, "core.json")).mode & 0o777, 0o600);
  assert.match(core.REDIS_URL, /^redis:\/\/:.+@redis:6379\/0$/);
  assert.equal(statSync(join(dir, "redis.conf")).mode & 0o777, 0o600);
  enableRedis({ dir });
  assert.deepEqual(JSON.parse(readFileSync(join(dir, "core.json"))), core);
  assert.throws(() => initialize({ dir }), /拒绝覆盖/);
});

test("migration drain blocks new sessions while readonly blocks every write", async (t) => {
  const drain = await start(t, config(temporary(t), { MAINTENANCE_MODE: "drain" }));
  assert.equal((await fetch(drain.url + "/api/session", { method: "POST", body: new URLSearchParams(sessionFields) })).status, 503);
  assert.equal((await fetch(drain.url + "/api/time")).status, 200);
  assert.notEqual((await fetch(drain.url + "/api/results", { method: "POST", body: "" })).status, 503);
  const readonly = await start(t, config(temporary(t), { MAINTENANCE_MODE: "readonly" }));
  assert.equal((await fetch(readonly.url + "/api/results", { method: "POST", body: "" })).status, 503);
  assert.equal((await fetch(readonly.url + "/api/time")).status, 200);
});
