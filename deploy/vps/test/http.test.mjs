import test from "node:test";
import assert from "node:assert/strict";
import { request as httpRequest } from "node:http";
import { createHTTPServer, listen, closeServer } from "../lib/http.mjs";

async function fixture(t, options = {}) {
  const server = createHTTPServer({ origin: "https://sq.example", handler: async (r) => Response.json({ url: r.url, ip: r.headers.get("cf-connecting-ip"), forwarded: r.headers.get("x-forwarded-for") }), ...options });
  const address = await listen(server, "127.0.0.1", 0); t.after(() => closeServer(server));
  return `http://127.0.0.1:${address.port}`;
}
test("direct HTTP uses socket identity and configured origin despite spoofed headers", async (t) => {
  const url = await fixture(t);
  const response = await fetch(url + "/api/session", { headers: { host: "evil.example", "cf-connecting-ip": "8.8.8.8", "x-forwarded-for": "8.8.4.4" } });
  assert.deepEqual(await response.json(), { url: "https://sq.example/api/session", ip: "127.0.0.1", forwarded: null });
});
test("proxy source requires secret and valid IP; health supports HEAD", async (t) => {
  const url = await fixture(t, { proxySecret: "test-proxy-secret" });
  assert.equal((await fetch(url)).status, 403);
  assert.equal((await fetch(url, { headers: { "x-sq-proxy-secret": "test-proxy-secret" } })).status, 400);
  const response = await fetch(url, { headers: { "x-sq-proxy-secret": "test-proxy-secret", "x-sq-client-ip": "8.8.8.8" } });
  assert.equal((await response.json()).ip, "8.8.8.8");
  const health = await fetch(url + "/health", { method: "HEAD" });
  assert.equal(health.status, 200); assert.equal(await health.text(), "");
});
test("declared and streamed bodies have the same enforced limit", async (t) => {
  const url = await fixture(t, { maxBodyBytes: 16 });
  assert.equal((await fetch(url, { method: "POST", body: "x".repeat(17) })).status, 413);
  const status = await new Promise((resolve, reject) => {
    const req = httpRequest(url, { method: "POST", headers: { "transfer-encoding": "chunked" } }, (res) => { res.resume(); res.on("end", () => resolve(res.statusCode)); });
    req.on("error", reject); req.write("x".repeat(12)); req.end("y".repeat(12));
  });
  assert.equal(status, 413);
});
