import assert from "node:assert/strict";
import test from "node:test";

import worker from "../src/index.js";

test("fixture provider supports configured Beijing, Shanghai, and Guangdong nodes", async () => {
  const response = await worker.fetch(new Request("https://fixture/lease", {
    method: "POST",
    headers: { "x-node-core-secret": "secret", "content-type": "application/json" },
    body: JSON.stringify({
      region: "bj",
      family: "v4",
      duration_seconds: 5,
      target_mbps: 200,
      modes: ["s"],
    }),
  }), {
    INTERNAL_SECRET: "secret",
    FIXTURE_NODES: JSON.stringify({
      bj: [{ carrier: "ct", address: "127.0.0.1", port: 18080 }],
      sh: [],
      gd: [],
    }),
  });
  assert.equal(response.status, 200);
  const lease = await response.json();
  assert.equal(lease.region.code, "bj");
  assert.equal(lease.target_mbps, 200);
  assert.equal(lease.targets[0].candidates[0].address, "127.0.0.1");
  assert.equal(lease.targets[0].candidates[0].activate.ready_delay_ms, 0);
  assert.match(lease.targets[0].candidates[0].id, /^[a-f0-9]{32}$/);
});
