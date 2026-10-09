import { fileURLToPath } from "node:url";
import { join } from "node:path";
import worker, { cleanupExpiredReports } from "../cloudflare-worker/src/index.js";
import { SQLiteDatabase, FileSnapshots, createHTTPServer, listen, closeServer, scheduleTask,
  readConfig, runtimeSettings, requireSecret, isMain, installShutdown } from "./lib/runtime.mjs";
import { createGeoResolver } from "./lib/geo.mjs";
import { FileAssetSource } from "./lib/assets.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const migrations = fileURLToPath(new URL("../cloudflare-worker/migrations/", import.meta.url));

export function createPublicApplication(config, dependencies = {}) {
  const settings = runtimeSettings(config, 52800);
  requireSecret(config, "RATE_LIMIT_SALT");
  if (config.PROXY_SECRET) requireSecret(config, "PROXY_SECRET");
  if (!config.PROXY_SECRET && !["127.0.0.1", "::1"].includes(settings.host)) {
    throw new Error("A non-loopback listener requires PROXY_SECRET");
  }
  const database = new SQLiteDatabase(join(settings.dataDir, "reports.sqlite"));
  try {
    database.migrate(migrations);
    const env = { LOG_LEVEL: "info", RESULT_TTL_DAYS: "90", NQ_BINDING_ENABLED: "true", ...config,
      DB: database, SNAPSHOTS: new FileSnapshots(join(settings.dataDir, "snapshots")) };
    if (config.STATIC_NODES && typeof config.STATIC_NODES === "object") env.STATIC_NODES = JSON.stringify(config.STATIC_NODES);
    if (config.CORE_URL) {
      requireSecret(config, "NODE_CORE_SECRET");
      const target = new URL(config.CORE_URL);
      if (!["http:", "https:"].includes(target.protocol) || target.username || target.password ||
          target.pathname !== "/" || target.search || target.hash) throw new Error("Invalid CORE_URL");
      env.NODE_CORE = { async fetch(request) {
        const original = new URL(request.url);
        if (original.origin !== "https://node-core.internal") throw new Error("Invalid Core request origin");
        return (dependencies.coreFetch || fetch)(new URL(original.pathname + original.search, target), {
          method: request.method, headers: request.headers,
          body: ["GET", "HEAD"].includes(request.method) ? undefined : await request.arrayBuffer(),
          redirect: "error", signal: AbortSignal.timeout(45_000),
        });
      } };
    }
    env.ASSET_SOURCE = new FileAssetSource({ directory: join(settings.dataDir, "assets"), repositoryRoot,
      owner: env.GITHUB_OWNER, repository: env.GITHUB_REPO, ref: env.GITHUB_REF,
      version: env.PROBE_VERSION, fetcher: dependencies.assetFetch || fetch });
    const server = createHTTPServer({ handler: (request) => worker.fetch(request, env),
      origin: settings.origin, proxySecret: config.PROXY_SECRET,
      resolveGeo: dependencies.resolveGeo || createGeoResolver({ url: config.GEOIP_URL }) });
    return { server, settings, env, database,
      cleanup: () => env.MAINTENANCE_MODE === "readonly" ? undefined : cleanupExpiredReports(env), close: () => database.close() };
  } catch (error) { database.close(); throw error; }
}

export async function startPublicServer(config, dependencies) {
  const app = createPublicApplication(config, dependencies);
  try {
    const address = await listen(app.server, app.settings.host, app.settings.port);
    const stopTasks = scheduleTask("report_cleanup", app.cleanup, 3600_000);
    return { ...app, address, async stop() { await closeServer(app.server); await stopTasks(); app.close(); } };
  } catch (error) { app.close(); throw error; }
}

if (isMain(import.meta.url)) {
  try {
    const config = readConfig("/etc/speedquality/public.json");
    if (config) {
      const app = await startPublicServer(config);
      console.log(JSON.stringify({ event: "vps.started", service: "public", port: app.address.port }));
      installShutdown(app.stop);
    }
  } catch (error) {
    console.error(`SpeedQuality VPS 启动失败: ${error.message}`);
    process.exitCode = 1;
  }
}
