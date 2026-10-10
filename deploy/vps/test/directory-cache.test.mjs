import test from "node:test";
import assert from "node:assert/strict";
import { DirectoryCache } from "../lib/directory-cache.mjs";

function fixture(options = {}) {
  let now = 1000000, writes = 0;
  const records = new Map();
  const store = {
    async read(key) { return records.get(key) || null; },
    async put(key, data, { checkedAt }) { writes++; records.set(key, { data, checkedAt }); },
  };
  return { records, store, advance: (ms) => { now += ms; }, writes: () => writes,
    cache: new DirectoryCache(store, { now: () => now, ...options }) };
}

test("directory retention survives reuse while freshness and the seven-day hard age do not slide", async () => {
  const f = fixture(); let calls = 0;
  const load = async () => { calls++; return [{ id: calls }]; };
  assert.deepEqual(await f.cache.getOrLoad("node", load), [{ id: 1 }]);
  for (let i = 0; i < 9; i++) {
    f.advance(60000);
    assert.deepEqual(await f.cache.getOrLoad("node", load), [{ id: 1 }]);
  }
  f.advance(60000);
  assert.deepEqual(await f.cache.getOrLoad("node", load), [{ id: 2 }]);
  assert.equal(calls, 2);
  const fails = async () => { throw new Error("upstream unavailable"); };
  f.advance(600001);
  assert.deepEqual(await f.cache.getOrLoad("node", fails), [{ id: 2 }]);
  f.advance(604800000);
  await assert.rejects(f.cache.getOrLoad("node", fails), /unavailable/);
});

test("an empty authoritative directory removes old candidates; upstream failures have a cooldown", async () => {
  const f = fixture();
  await f.cache.getOrLoad("node", async () => [{ id: 1 }]);
  f.advance(600001);
  assert.deepEqual(await f.cache.getOrLoad("node", async () => []), []);
  let calls = 0;
  const fails = async () => { calls++; throw new Error("failed"); };
  assert.deepEqual(await f.cache.getOrLoad("node", fails), []);
  f.advance(60001);
  await assert.rejects(f.cache.getOrLoad("node", fails));
  await assert.rejects(f.cache.getOrLoad("node", fails), /deferred/);
  assert.equal(calls, 1);
});

test("Redis outage coalesces identical requests and bounds unrelated upstream work", async () => {
  const f = fixture({ maxConcurrent: 2, maxPending: 5 });
  f.store.read = f.store.put = async () => { throw new Error("offline"); };
  let active = 0, peak = 0, calls = 0;
  const load = async () => {
    calls++; active++; peak = Math.max(peak, active);
    await new Promise((resolve) => setTimeout(resolve, 10));
    active--; return [{ id: 1 }];
  };
  await Promise.all(Array.from({ length: 20 }, () => f.cache.getOrLoad("same", load)));
  assert.equal(calls, 1);
  await f.cache.getOrLoad("same", load);
  assert.equal(calls, 1, "brief outage fallback prevents sequential cache misses from flooding upstream");
  const work = Array.from({ length: 5 }, (_, i) => f.cache.getOrLoad(String(i), load));
  await assert.rejects(f.cache.getOrLoad("overflow", load), /busy/);
  await Promise.all(work);
  assert.equal(peak, 2);
});

test("timed-out refresh waiters never run and later work can acquire the released slot", async () => {
  const f = fixture({ maxConcurrent: 1, queueTimeoutMs: 20 });
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const first = f.cache.getOrLoad("slow", async () => { await gate; return [1]; });
  let calls = 0;
  await assert.rejects(f.cache.getOrLoad("queued", async () => { calls++; return [2]; }), /unavailable/);
  release(); await first;
  assert.deepEqual(await f.cache.getOrLoad("later", async () => [3]), [3]);
  assert.equal(calls, 0);
});

test("a refresh failure cannot return a fallback that expired while the request was running", async () => {
  const f = fixture();
  await f.cache.getOrLoad("node", async () => [1]);
  f.advance(604799000);
  await assert.rejects(f.cache.getOrLoad("node", async () => {
    f.advance(2000); throw new Error("upstream timeout");
  }), /unavailable/);
});
