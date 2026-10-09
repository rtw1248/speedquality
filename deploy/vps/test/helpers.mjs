import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export const migrations = fileURLToPath(new URL("../../cloudflare-worker/migrations/", import.meta.url));
export function temporary(t) {
  const path = mkdtempSync(join(tmpdir(), "sq-vps-test-"));
  t.after(() => rmSync(path, { recursive: true, force: true }));
  return path;
}
export function config(directory, extra = {}) {
  return { ORIGIN: "https://sq.example", DATA_DIR: directory, PORT: 0, HOST: "127.0.0.1",
    RATE_LIMIT_SALT: "fixture-rate-salt-12345678901234567890", LOG_LEVEL: "error",
    GITHUB_OWNER: "example", GITHUB_REPO: "speedquality", GITHUB_REF: "v1.1.0", PROBE_VERSION: "v1.1.0",
    STATIC_NODES: { hb: [{ carrier: "ct", address: "8.8.8.8", port: 51234, scheme: "http", max_mbps: 200 }] }, ...extra };
}
export const sessionFields = { regions: "hb", mode: "s", ip_mode: "v4", duration_seconds: "5", target_mbps: "200" };
export function resultFields() {
  const now = Math.floor(Date.now() / 1000);
  return { tested_at: String(now), regions: "湖北", mode: "s", ip_mode: "v4", speed_url: "", speed_text: "fixture",
    speed_data: JSON.stringify({ version: 1, lease_id: "lease_fixture_123", started_at: now - 15, completed_at: now,
      region: { code: "hb", name: "湖北" }, family: "v4", duration_seconds: 5, target_mbps: 200, modes: ["s"],
      results: [{ carrier: "ct", label: "湖北电信", node_id: "0123456789abcdef0123456789abcdef", latency_ms: 8.25,
        status: "ok", single: { download_mbps: 199.2, upload_mbps: 150.5, download_bytes: 124500000, upload_bytes: 94062500 } }] }),
    duration_seconds: "5", target_mbps: "200", traffic_rx_bytes: "100000000", traffic_tx_bytes: "50000000",
    bind_status: "standalone", version: "1.1.0" };
}

// A dump in SQLite/D1 SQL format, independent of the application's importer.
export function dump(database) {
  const tables = database.prepare("SELECT name, sql FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'sq_migrations' ORDER BY name").all();
  const quoteName = (name) => `"${name.replaceAll('"', '""')}"`;
  const sql = ["PRAGMA foreign_keys=OFF;", ...tables.map(({ sql }) => `${sql};`)];
  for (const { name } of tables) {
    for (const row of database.prepare(`SELECT * FROM ${quoteName(name)}`).all()) {
      const values = Object.values(row).map((value) => database.prepare("SELECT quote(?) AS value").get(value).value);
      sql.push(`INSERT INTO ${quoteName(name)} VALUES (${values.join(",")});`);
    }
  }
  sql.push(...database.prepare("SELECT sql FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL ORDER BY name").all().map(({ sql }) => `${sql};`));
  return sql.join("\n");
}
