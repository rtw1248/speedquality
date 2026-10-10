import { createHash, generateKeyPairSync, randomBytes } from "node:crypto";
import { chmodSync, chownSync, copyFileSync, existsSync, linkSync, lstatSync, mkdirSync, readFileSync,
  readdirSync, renameSync, statSync, unlinkSync, writeFileSync } from "node:fs";
import { setTimeout as delay } from "node:timers/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";
import { SQLiteDatabase, applicationSchema, migrationFiles, isMain } from "./lib/runtime.mjs";

const root = fileURLToPath(new URL("../../", import.meta.url));
const defaultMigrations = fileURLToPath(new URL("../cloudflare-worker/migrations/", import.meta.url));
const keyPattern = /^[A-Za-z0-9_-]{12}\.json\.gz$/;

function inheritOwner(path) {
  if (process.getuid?.() === 0) {
    const parent = statSync(dirname(path));
    chownSync(path, parent.uid, parent.gid);
  }
}

function atomicPrivateFile(path, text) {
  const temporary = `${path}.${randomBytes(8).toString("hex")}.tmp`;
  try {
    writeFileSync(temporary, text, { flag: "wx", mode: 0o600 });
    inheritOwner(temporary);
    renameSync(temporary, path);
  } finally { if (existsSync(temporary)) unlinkSync(temporary); }
}

export function enableRedis(options) {
  const directory = resolve(options.dir || join(root, "deploy/vps/local"));
  const path = join(directory, "core.json");
  const config = JSON.parse(readFileSync(path, "utf8"));
  const redisConfigPath = join(directory, "redis.conf");
  let password;
  if (config.REDIS_URL) {
    const url = new URL(config.REDIS_URL);
    if (url.protocol !== "redis:" || url.hostname !== "redis" || url.port !== "6379" || !/^[a-f0-9]{64}$/.test(url.password)) {
      throw new Error("已有自定义 REDIS_URL，拒绝覆盖；请自行配置 Redis 服务");
    }
    password = url.password;
  } else if (existsSync(redisConfigPath)) {
    password = readFileSync(redisConfigPath, "utf8").match(/^requirepass ([a-f0-9]{64})$/m)?.[1];
    if (!password) throw new Error("已有 Redis 配置无效，拒绝覆盖");
  } else password = randomBytes(32).toString("hex");
  if (!existsSync(redisConfigPath)) {
    writeFileSync(redisConfigPath, `bind 0.0.0.0
protected-mode yes
port 6379
requirepass ${password}
dir /data
appendonly yes
appendfsync everysec
save "900 1"
maxmemory 128mb
maxmemory-policy allkeys-lru
`, { flag: "wx", mode: 0o600 });
    inheritOwner(redisConfigPath);
  } else if (!readFileSync(redisConfigPath, "utf8").includes(`requirepass ${password}\n`)) {
    throw new Error("Redis 凭据不一致；拒绝自动替换");
  }
  const data = join(directory, "data/redis");
  mkdirSync(data, { recursive: true, mode: 0o700 });
  if (process.getuid?.() === 0) {
    const owner = statSync(directory); chownSync(data, owner.uid, owner.gid);
  }
  if (!config.REDIS_URL) {
    const backup = join(directory, "core.before-redis.json");
    if (!existsSync(backup)) { copyFileSync(path, backup, 1); chmodSync(backup, 0o600); inheritOwner(backup); }
    config.REDIS_URL = `redis://:${password}@redis:6379/0`;
  }
  config.DIRECTORY_REFRESH_SECONDS ??= 600;
  config.DIRECTORY_RETENTION_SECONDS ??= 604800;
  atomicPrivateFile(path, JSON.stringify(config, null, 2) + "\n");
  return directory;
}

export function initialize(options) {
  const directory = resolve(options.dir || join(root, "deploy/vps/local"));
  for (const name of ["public.json", "core.json", "compose.env"]) {
    if (existsSync(join(directory, name))) throw new Error("配置已存在；拒绝覆盖密钥。请直接修改现有配置。");
  }
  const origin = new URL(options.origin || "https://sq.yolo2.cc");
  if (origin.protocol !== "https:" || origin.pathname !== "/" || origin.search || origin.hash || origin.username) {
    throw new Error("--origin 必须是 HTTPS 域名入口");
  }
  const owner = options["github-owner"] || "rtw1248";
  const repository = options["github-repo"] || "speedquality";
  const version = options.release || "v1.2.0";
  if (!/^[A-Za-z0-9_.-]+$/.test(owner) || !/^[A-Za-z0-9_.-]+$/.test(repository) || !/^v\d+\.\d+\.\d+$/.test(version)) {
    throw new Error("无效的 GitHub 仓库或版本");
  }
  const uid = Number(options.uid ?? (process.getuid?.() || 10001));
  const gid = Number(options.gid ?? (process.getgid?.() || 10001));
  if (![uid, gid].every((value) => Number.isInteger(value) && value > 0)) throw new Error("服务 UID/GID 必须是非 root 用户");
  const publicData = join(directory, "data/public");
  const coreData = join(directory, "data/core");
  const coreDirectory = resolve(options["core-dir"] || join(root, "../speedquality-node-core"));
  for (const path of [directory, publicData, coreData]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
    if (process.getuid?.() === 0) chownSync(path, uid, gid);
  }
  const secret = () => randomBytes(32).toString("hex");
  const internalSecret = secret();
  const signingKey = generateKeyPairSync("ec", { namedCurve: "prime256v1" }).privateKey.export({ format: "jwk" });
  signingKey.kid = `sq-${randomBytes(8).toString("hex")}`;
  const common = { ORIGIN: origin.origin, DATA_DIR: "/data", HOST: "0.0.0.0", LOG_LEVEL: "info" };
  const publicConfig = { ...common, PORT: 52800, CORE_URL: "http://core:52801",
    PROXY_SECRET: secret(), NODE_CORE_SECRET: internalSecret, RATE_LIMIT_SALT: secret(),
    GITHUB_OWNER: owner, GITHUB_REPO: repository, GITHUB_REF: version, PROBE_VERSION: version,
    RESULT_TTL_DAYS: "90", DAILY_RESULT_LIMIT: "100", DAILY_SESSION_LIMIT: "20", NQ_BINDING_ENABLED: "true",
    TASK_SCHEDULER_ENABLED: "true",
    PROMOTION_TEXT: "", PROMOTION_URL: "" };
  const coreConfig = { ...common, PORT: 52801, INTERNAL_SECRET: internalSecret,
    NODE_ID_SALT: secret(), NODE_CREDENTIAL_SALT: secret(), NODE_JWT_PRIVATE_JWK: JSON.stringify(signingKey),
    NODE_JWT_ISSUER: origin.origin, NODE_JWT_AUDIENCE: "sq-node",
    GEO_VERIFY_URL: "https://ipwho.is/{ip}?fields=success,country_code,region,city" };
  for (const [name, value] of [["public.json", publicConfig], ["core.json", coreConfig]]) {
    writeFileSync(join(directory, name), JSON.stringify(value, null, 2) + "\n", { flag: "wx", mode: 0o600 });
    inheritOwner(join(directory, name));
  }
  const values = { SQ_CONFIG_DIR: directory, SQ_PUBLIC_DATA: publicData, SQ_CORE_DATA: coreData,
    SQ_CORE_DIR: coreDirectory, SQ_UID: uid, SQ_GID: gid, SQ_PUBLIC_PORT: 52800 };
  const text = Object.entries(values).map(([key, value]) => {
    if (/[\r\n$]/.test(String(value))) throw new Error("配置目录不能包含换行或美元符号");
    return `${key}=${JSON.stringify(String(value))}`;
  }).join("\n") + "\n";
  writeFileSync(join(directory, "compose.env"), text, { flag: "wx", mode: 0o600 });
  inheritOwner(join(directory, "compose.env"));
  enableRedis({ dir: directory });
  return { directory, publicData, coreData };
}

export function importD1({ input, output, migrations = defaultMigrations }) {
  const target = resolve(output);
  if (existsSync(target)) throw new Error("目标数据库已存在；请导入到新的空目录后检查，禁止覆盖运行中的数据库");
  mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
  const temporary = `${target}.${randomBytes(12).toString("hex")}.import`;
  let database;
  let reference;
  try {
    database = new SQLiteDatabase(temporary);
    database.database.exec(readFileSync(input, "utf8"));
    if (database.database.prepare("PRAGMA quick_check").get().quick_check !== "ok" ||
        database.database.prepare("PRAGMA foreign_key_check").all().length) throw new Error("导入数据库完整性校验失败");
    reference = new SQLiteDatabase(":memory:");
    reference.migrate(migrations);
    if (JSON.stringify(applicationSchema(database.database)) !== JSON.stringify(applicationSchema(reference.database))) {
      throw new Error("导出数据的表结构与当前迁移不一致；请先使用对应版本导出，不能猜测迁移状态");
    }
    database.database.exec("BEGIN IMMEDIATE");
    try {
      database.database.exec("CREATE TABLE IF NOT EXISTS sq_migrations (name TEXT PRIMARY KEY, sha256 TEXT NOT NULL, applied_at INTEGER NOT NULL)");
      for (const name of migrationFiles(migrations)) {
        const digest = createHash("sha256").update(readFileSync(join(migrations, name))).digest("hex");
        database.database.prepare("INSERT INTO sq_migrations VALUES (?, ?, unixepoch()) ON CONFLICT(name) DO UPDATE SET sha256=excluded.sha256").run(name, digest);
      }
      database.database.exec("COMMIT");
    } catch (error) { database.database.exec("ROLLBACK"); throw error; }
    database.database.exec("PRAGMA wal_checkpoint(TRUNCATE)");
    database.close(); database = null;
    linkSync(temporary, target); // Atomic no-overwrite installation.
    chmodSync(target, 0o600);
    inheritOwner(target);
    return target;
  } finally {
    database?.close();
    reference?.close();
    for (const suffix of ["", "-wal", "-shm"]) {
      try { unlinkSync(temporary + suffix); } catch (error) { if (error.code !== "ENOENT") throw error; }
    }
  }
}

export function importSnapshots({ input, output }) {
  mkdirSync(output, { recursive: true, mode: 0o700 });
  let copied = 0;
  for (const name of readdirSync(input)) {
    if (!keyPattern.test(name)) throw new Error("快照目录只能包含报告ID.json.gz 文件");
    const source = join(input, name);
    const destination = join(output, name);
    if (!lstatSync(source).isFile() || lstatSync(source).size > 300 * 1024) throw new Error("无效快照文件");
    copyFileSync(source, destination, 1);
    chmodSync(destination, 0o600);
    inheritOwner(destination);
    copied++;
  }
  return copied;
}

export async function backupData({ publicDir, coreDir, output }) {
  const destination = resolve(output);
  if (existsSync(destination)) throw new Error("备份目录已存在，请指定新的目录");
  mkdirSync(destination, { recursive: true, mode: 0o700 });
  const manifest = { version: 1, created_at: new Date().toISOString(), files: {}, missing_snapshots: [] };
  for (const [name, directory] of [["reports.sqlite", publicDir], ["core.sqlite", coreDir]]) {
    if (!directory) continue;
    const source = join(directory, name);
    if (!existsSync(source)) throw new Error(`缺少 ${name}`);
    const database = new SQLiteDatabase(source);
    try { await database.backup(join(destination, name)); } finally { database.close(); }
  }
  if (publicDir) {
    const db = new DatabaseSync(join(destination, "reports.sqlite"), { readOnly: true });
    let rows;
    try {
      rows = db.prepare("SELECT id FROM reports WHERE expires_at > unixepoch() AND bind_status != 'standalone'").all();
    } finally { db.close(); }
    mkdirSync(join(destination, "snapshots"), { mode: 0o700 });
    const retryUntil = Date.now() + 2100;
    for (const { id } of rows) {
      if (!/^[A-Za-z0-9_-]{12}$/.test(id)) throw new Error("无效报告 ID");
      const file = `${id}.json.gz`;
      const source = join(publicDir, "snapshots", file);
      // A report row can precede its asynchronous snapshot write. Retry briefly,
      // then mark the backup incomplete instead of certifying a missing file.
      for (const wait of [100, 500, 1500]) {
        if (existsSync(source) || Date.now() >= retryUntil) break;
        await delay(Math.min(wait, retryUntil - Date.now()));
      }
      try {
        copyFileSync(source, join(destination, "snapshots", file), 1);
        chmodSync(join(destination, "snapshots", file), 0o600);
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
        manifest.missing_snapshots.push(id);
      }
    }
  }
  const paths = readdirSync(destination).filter((name) => name.endsWith(".sqlite"));
  if (existsSync(join(destination, "snapshots"))) {
    paths.push(...readdirSync(join(destination, "snapshots")).map((name) => `snapshots/${name}`));
  }
  for (const path of paths) manifest.files[path] = createHash("sha256").update(readFileSync(join(destination, path))).digest("hex");
  writeFileSync(join(destination, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n", { flag: "wx", mode: 0o600 });
  return manifest;
}

export function restoreData({ input, publicDir, coreDir }) {
  const manifest = JSON.parse(readFileSync(join(input, "manifest.json"), "utf8"));
  if (manifest.version !== 1 || !manifest.files || typeof manifest.files !== "object" || Array.isArray(manifest.files)) throw new Error("无效备份清单");
  if (!Array.isArray(manifest.missing_snapshots) || manifest.missing_snapshots.length) throw new Error("备份存在缺失快照；请修复来源并重新备份");
  if (!publicDir && !coreDir) throw new Error("至少指定一个恢复目录");
  if (publicDir && coreDir && resolve(publicDir) === resolve(coreDir)) throw new Error("Public 和 Core 必须使用不同目录");
  for (const [path, digest] of Object.entries(manifest.files)) {
    if (!/^(reports\.sqlite|core\.sqlite|snapshots\/[A-Za-z0-9_-]{12}\.json\.gz)$/.test(path)) throw new Error("无效备份路径");
    if (!lstatSync(join(input, path)).isFile()) throw new Error("备份只能包含普通文件");
    const actual = createHash("sha256").update(readFileSync(join(input, path))).digest("hex");
    if (actual !== digest) throw new Error(`备份校验失败: ${path}`);
  }
  if (existsSync(join(input, "snapshots"))) {
    for (const name of readdirSync(join(input, "snapshots"))) {
      if (!Object.hasOwn(manifest.files, `snapshots/${name}`)) throw new Error("备份包含未经清单校验的快照");
    }
  }
  // Validate every requested database before creating any destination files.
  for (const [name, directory] of [["reports.sqlite", publicDir], ["core.sqlite", coreDir]]) {
    if (!directory) continue;
    if (!manifest.files[name]) throw new Error(`备份缺少 ${name}`);
    const db = new DatabaseSync(join(input, name), { readOnly: true });
    try {
      if (db.prepare("PRAGMA quick_check").get().quick_check !== "ok" || db.prepare("PRAGMA foreign_key_check").all().length) {
        throw new Error(`备份数据库完整性校验失败: ${name}`);
      }
    } finally { db.close(); }
  }
  for (const directory of [publicDir, coreDir].filter(Boolean)) {
    if (existsSync(directory) && readdirSync(directory).length) throw new Error("恢复目标必须是空目录；禁止覆盖现有数据");
  }
  for (const [name, directory] of [["reports.sqlite", publicDir], ["core.sqlite", coreDir]]) {
    if (!directory) continue;
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    copyFileSync(join(input, name), join(directory, name), 1);
    chmodSync(join(directory, name), 0o600);
    inheritOwner(join(directory, name));
  }
  if (publicDir && existsSync(join(input, "snapshots"))) importSnapshots({ input: join(input, "snapshots"), output: join(publicDir, "snapshots") });
}

function argumentsFor(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!/^--[a-z][a-z-]*$/.test(argv[index]) || !argv[index + 1] || argv[index + 1].startsWith("--")) throw new Error("参数必须使用 --名称 值");
    values[argv[index].slice(2)] = argv[index + 1];
  }
  return values;
}

if (isMain(import.meta.url)) {
  try {
    const [command, ...argv] = process.argv.slice(2);
    if (!command || command === "--help") {
      console.log(`SpeedQuality VPS 管理工具（Node.js 24）
  init --dir DIR --origin https://sq.example --core-dir PRIVATE_REPO [--release v1.2.0]
  enable-redis --dir EXISTING_STATE_DIR
  import-d1 --input export.sql --output NEW.sqlite [--migrations DIR]
  import-snapshots --input DIR --output DIR
  backup --public-dir DIR --core-dir DIR --output NEW_BACKUP_DIR
  restore --input BACKUP_DIR --public-dir EMPTY_DIR --core-dir EMPTY_DIR

init 自动配置 Redis；enable-redis 为旧部署补充缓存配置，保留原平台密钥。
初始化不覆盖已有密钥。迁移前必须保留原平台密钥。
backup 使用 SQLite 在线备份；密钥配置需另外加密备份。restore 只写入空目录。`);
    } else {
      const options = argumentsFor(argv);
      if (command === "init") console.log("配置已生成:", initialize(options).directory);
      else if (command === "enable-redis") console.log("Redis 配置已准备:", enableRedis(options));
      else if (command === "import-d1") console.log("已校验并导入:", importD1(options));
      else if (command === "import-snapshots") console.log("已导入快照数:", importSnapshots(options));
      else if (command === "backup") {
        const manifest = await backupData({ publicDir: options["public-dir"], coreDir: options["core-dir"], output: options.output });
        console.log("备份文件数:", Object.keys(manifest.files).length, "缺失快照数:", manifest.missing_snapshots.length);
        if (manifest.missing_snapshots.length) process.exitCode = 2;
      } else if (command === "restore") {
        restoreData({ input: options.input, publicDir: options["public-dir"], coreDir: options["core-dir"] });
        console.log("备份已校验并恢复到空目录");
      } else throw new Error("未知命令，使用 --help 查看用法");
    }
  } catch (error) { console.error(`操作失败: ${error.message}`); process.exitCode = 1; }
}
