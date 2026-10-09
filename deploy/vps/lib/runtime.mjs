import { readFileSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";
import { pathToFileURL } from "node:url";
export { SQLiteDatabase, applicationSchema, migrationFiles } from "./sqlite.mjs";
export { MemoryCache, FileSnapshots } from "./storage.mjs";
export { createHTTPServer, listen, closeServer, scheduleTask } from "./http.mjs";

export function readConfig(defaultFile, argv = process.argv.slice(2)) {
  if (argv.includes("--help")) {
    console.log("用法: node server.mjs [--config /绝对路径/config.json]\n配置只在启动时读取；修改后重启对应服务。");
    return null;
  }
  let file = process.env.SQ_CONFIG || defaultFile;
  if (argv.length) {
    if (argv.length !== 2 || argv[0] !== "--config") throw new Error("Only --config and --help are supported");
    file = argv[1];
  }
  const text = readFileSync(file, "utf8");
  let config;
  try { config = JSON.parse(text); } catch { throw new Error("Configuration is not valid JSON; check the private configuration file"); }
  if (!config || typeof config !== "object" || Array.isArray(config)) throw new Error("Configuration must be an object");
  for (const key of ["DATA_DIR", "HOST", "PORT", "CORE_URL"]) {
    if (process.env[`SQ_${key}`]) config[key] = process.env[`SQ_${key}`];
  }
  return config;
}

export function runtimeSettings(config, defaultPort) {
  const origin = new URL(config.ORIGIN || "https://sq.yolo2.cc");
  if (origin.username || origin.password || origin.search || origin.hash || origin.pathname !== "/" ||
      (origin.protocol !== "https:" && !(origin.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(origin.hostname)))) {
    throw new Error("ORIGIN must be an HTTPS origin (HTTP is allowed only on localhost)");
  }
  const dataDir = String(config.DATA_DIR || "");
  if (!isAbsolute(dataDir) || dataDir === "/") throw new Error("DATA_DIR must be a dedicated absolute directory");
  const port = Number(config.PORT ?? defaultPort);
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error("Invalid listen port");
  return { origin: origin.origin, dataDir, host: String(config.HOST || "127.0.0.1"), port };
}

export function requireSecret(config, name) {
  if (typeof config[name] !== "string" || config[name].length < 32 || /CHANGE_ME|YOUR_/i.test(config[name])) {
    throw new Error(`A private ${name} with at least 32 characters is required`);
  }
}

export function isMain(url) {
  return Boolean(process.argv[1]) && pathToFileURL(resolve(process.argv[1])).href === url;
}

export function installShutdown(stop) {
  let closing = false;
  for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => {
    if (closing) return;
    closing = true;
    stop().then(() => { process.exitCode = 0; }).catch((error) => {
      console.error(JSON.stringify({ event: "vps.shutdown_failed", error_type: error.name }));
      process.exitCode = 1;
    });
  });
}
