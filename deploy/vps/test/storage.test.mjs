import test from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync, statSync, symlinkSync } from "node:fs";
import { join } from "node:path";
import { SQLiteDatabase, MemoryCache, FileSnapshots, readConfig } from "../lib/runtime.mjs";
import { FileAssetSource } from "../lib/assets.mjs";
import { createGeoResolver } from "../lib/geo.mjs";
import { temporary, migrations } from "./helpers.mjs";

test("real SQLite batches roll back and consume RETURNING; migrations are durable", async (t) => {
  const path = join(temporary(t), "reports.sqlite");
  const db = new SQLiteDatabase(path);
  t.after(() => db.close());
  db.migrate(migrations); db.migrate(migrations);
  const insert = db.prepare("INSERT INTO usage_counters VALUES (?, ?, ?)");
  const rows = await db.batch([insert.bind("first", 1, 1), db.prepare("UPDATE usage_counters SET count=count+1 RETURNING count")]);
  assert.equal(rows[1].results[0].count, 2);
  assert.equal(rows[1].meta.changes, 1);
  await assert.rejects(db.batch([insert.bind("second", 1, 1), insert.bind("first", 1, 1)]));
  assert.equal(await db.prepare("SELECT * FROM usage_counters WHERE key='second'").first(), null);
  assert.equal(statSync(path).mode & 0o777, 0o600);
  const dir = join(temporary(t), "migrations"); mkdirSync(dir);
  writeFileSync(join(dir, "0099_example.sql"), "CREATE TABLE example (id INTEGER)"); db.migrate(dir);
  writeFileSync(join(dir, "0099_example.sql"), "CREATE TABLE example (id TEXT)");
  assert.throws(() => db.migrate(dir), /migration changed/);
});

test("cache stays bounded under concurrent updates and honors expiration/LRU", async () => {
  let now = 0;
  const cache = new MemoryCache({ maxEntries: 2, maxBytes: 30, now: () => now });
  await Promise.all(Array.from({ length: 100 }, (_, i) => cache.put("a", String(i), { expirationTtl: 1 })));
  assert.equal(cache.bytes, 3);
  await cache.put("b", "1"); await cache.get("a"); await cache.put("c", "2");
  assert.equal(await cache.get("b"), null);
  now = 1001;
  assert.equal(await cache.get("a"), null);
  await cache.put("huge", "x".repeat(40));
  assert.equal(cache.entries.size, 1);
});

test("snapshot writes are atomic, private and reject paths, oversize files and symlinks", async (t) => {
  const dir = temporary(t), store = new FileSnapshots(dir), key = "nodequality/AbCdEf123456.json.gz";
  await store.put(key, Buffer.from("first"));
  await store.put(key, Buffer.from("second"));
  assert.equal(await new Response((await store.get(key)).body).text(), "second");
  assert.equal(statSync(join(dir, "AbCdEf123456.json.gz")).mode & 0o777, 0o600);
  await assert.rejects(store.put(key, Buffer.alloc(300 * 1024 + 1)), /limit/);
  await assert.rejects(store.get("../secret"), /key/);
  symlinkSync(join(dir, "AbCdEf123456.json.gz"), join(dir, "Symlink12345.json.gz"));
  await assert.rejects(store.get("nodequality/Symlink12345.json.gz"));
  await store.delete(key); assert.equal(await store.get(key), null);
});

test("release assets use a single bounded download and persist across instances", async (t) => {
  const dir = temporary(t); writeFileSync(join(dir, "run.sh"), "fixture script");
  let calls = 0;
  const options = { directory: join(dir, "cache"), repositoryRoot: dir, owner: "a", repository: "b", ref: "v1.1.0", version: "v1.1.0",
    fetcher: async () => { calls++; return new Response("binary fixture"); } };
  const source = new FileAssetSource(options), url = "https://github.com/a/b/releases/download/v1.1.0/sqprobe-linux-amd64";
  assert.equal(await (await source.fetch("https://raw.githubusercontent.com/a/b/v1.1.0/run.sh")).text(), "fixture script");
  const texts = await Promise.all(Array.from({ length: 8 }, async () => (await source.fetch(url)).text()));
  assert.ok(texts.every((text) => text === "binary fixture")); assert.equal(calls, 1);
  await new FileAssetSource(options).fetch(url); assert.equal(calls, 1);
  await assert.rejects(source.fetch("https://evil.example/asset"), /Unrecognized/);
  const broken = new FileAssetSource({ ...options, directory: join(dir, "failed"), fetcher: async () => new Response("missing", { status: 404 }) });
  await assert.rejects(broken.fetch(url), /failed/);
});

test("geo lookups coalesce, cache failures and never invent unknown locations", async () => {
  let calls = 0;
  const lookup = createGeoResolver({ fetcher: async () => { calls++; return Response.json({ success: true, country_code: "CN", region: "Hubei", connection: { asn: 4134, org: "fixture" } }); } });
  const results = await Promise.all([lookup("8.8.8.8"), lookup("8.8.8.8")]);
  assert.equal(calls, 1); assert.equal(results[0].region, "Hubei"); assert.equal(results[1].asn, 4134);
  assert.deepEqual(await lookup("127.0.0.1"), {});
  const unavailable = createGeoResolver({ fetcher: async () => { throw new Error("unavailable"); } });
  assert.deepEqual(await unavailable("8.8.4.4"), {});
});

test("configuration syntax errors never echo private key fragments", (t) => {
  const path = join(temporary(t), "config.json");
  writeFileSync(path, '{"private":"DO_NOT_PRINT_THIS", invalid}');
  assert.throws(() => readConfig(path, []), (error) => !error.message.includes("DO_NOT_PRINT_THIS") && /JSON/.test(error.message));
});
