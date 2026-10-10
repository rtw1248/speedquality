import test from "node:test";
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { createServer } from "node:net";
import { setTimeout as delay } from "node:timers/promises";
import { RedisCache } from "../lib/redis.mjs";
import { DirectoryCache } from "../lib/directory-cache.mjs";
import { temporary } from "./helpers.mjs";

test("real Redis authenticates, expires, restores AOF after a crash and recovers after an outage", {
  skip: process.env.SQ_TEST_REDIS_DOCKER !== "1" ? "Run npm run test:redis with Docker" : false,
  timeout: 45000,
}, async (t) => {
  const directory = temporary(t);
  const name = `sq-cache-test-${randomBytes(6).toString("hex")}`;
  const password = randomBytes(32).toString("hex");
  writeFileSync(join(directory, "redis.conf"), `bind 0.0.0.0\nrequirepass ${password}\ndir /data\nappendonly yes\nappendfsync everysec\nsave ""\n`);
  const docker = (...args) => execFileSync("docker", args, { encoding: "utf8", timeout: 20000, stdio: ["ignore", "pipe", "pipe"] }).trim();
  const listener = createServer();
  await new Promise((resolve) => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port;
  await new Promise((resolve) => listener.close(resolve));
  docker("run", "-d", "--name", name, "--user", `${process.getuid()}:${process.getgid()}`,
    "-p", `127.0.0.1:${port}:6379`, "-v", `${directory}:/data`, "redis:8-alpine", "redis-server", "/data/redis.conf");
  t.after(() => docker("rm", "-f", name));
  const url = `redis://:${password}@127.0.0.1:${port}`;
  const cache = new RedisCache({ url, retryMs: 50, timeoutMs: 3500 });
  t.after(() => cache.close());
  for (let attempt = 0; ; attempt++) {
    try { assert.equal(await cache.ping(), "PONG"); break; }
    catch (error) { if (attempt === 30) throw error; await delay(100); }
  }
  assert.equal(await cache.put("persist", "[1]", { expirationTtl: 604800, checkedAt: 100 }), 1);
  assert.equal(await cache.put("persist", "[1]", { expirationTtl: 604800, checkedAt: 200 }), 0);
  assert.deepEqual(await cache.read("persist"), { data: "[1]", checkedAt: 200 });
  await cache.command((client) => client.expire("sq:cache:persist", 10));
  assert.equal((await cache.read("persist", 604800)).checkedAt, 200, "touch must not refresh verification time");
  assert.ok(await cache.command((client) => client.ttl("sq:cache:persist")) > 604700);
  await cache.put("empty", "[]", { expirationTtl: 60 });
  await cache.read("empty", 604800);
  assert.ok(await cache.command((client) => client.ttl("sq:cache:empty")) <= 60, "negative cache must keep its short expiry");
  await cache.put("expire", "value", { expirationTtl: 1 });
  await delay(1100);
  assert.equal(await cache.get("expire"), null);
  const bad = new RedisCache({ url: `redis://:wrong@127.0.0.1:${port}`, retryMs: 50, timeoutMs: 500 });
  await assert.rejects(bad.ping(), (error) => error.message === "Redis cache unavailable"); bad.close();
  const lookup = new DirectoryCache(cache);
  await lookup.getOrLoad("directory", async () => [{ id: "durable" }]);
  await cache.command((client) => client.sendCommand(["WAITAOF", "1", "0", "2000"]));
  docker("kill", name);
  await assert.rejects(cache.ping(), /unavailable/);
  const degraded = await lookup.getOrLoad("new", async () => [{ id: "fallback" }]);
  assert.equal(degraded[0].id, "fallback");
  docker("start", name);
  await delay(150);
  for (let attempt = 0; ; attempt++) {
    try { assert.equal(await cache.ping(), "PONG"); break; }
    catch (error) { if (attempt === 30) throw error; await delay(100); }
  }
  const restored = new DirectoryCache(cache);
  assert.deepEqual(await restored.getOrLoad("directory", async () => { throw new Error("must use persisted data"); }), [{ id: "durable" }]);
  assert.equal(await cache.get("persist"), "[1]");
});
