import { execFileSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { importD1 } from "./admin.mjs";
import { SQLiteDatabase } from "./lib/sqlite.mjs";

const root = fileURLToPath(new URL("../../", import.meta.url));
const options = {};
for (let i = 2; i < process.argv.length; i += 2) options[process.argv[i]] = process.argv[i + 1];
if (!options["--output"]) {
  console.log("用法: node deploy/vps/export-cloudflare.mjs --output NEW_DIR [--core-dir PRIVATE_REPO]\n只读取 Cloudflare。迁移必须先停止新会话并等待在途测试结束，再执行最终导出。\n使用两个仓库已安装的 Wrangler 和本机登录状态；不导出密钥。输出含 SQL、SQLite 和 NQ 快照。");
} else {
  try {
    const destination = resolve(options["--output"]);
    if (existsSync(destination)) throw new Error("导出目录已存在，请使用新目录");
    const publicWorker = join(root, "deploy/cloudflare-worker");
    const coreRepo = resolve(options["--core-dir"] || join(root, "../speedquality-node-core"));
    const wrangler = join(publicWorker, "node_modules/wrangler/bin/wrangler.js");
    const invoke = (cwd, args) => {
      try { execFileSync(process.execPath, [wrangler, ...args], { cwd, stdio: ["ignore", "pipe", "pipe"], timeout: 180000, maxBuffer: 4 * 1024 * 1024 }); }
      catch { throw new Error(`Wrangler ${args.slice(0, 2).join(" ")} 失败；请在对应目录运行 npx wrangler whoami 检查权限，导出目录可能不完整`); }
    };
    mkdirSync(destination, { recursive: true, mode: 0o700 });
    for (const [name, database, cwd, migrations] of [
      ["public", "speedquality-results", publicWorker, join(publicWorker, "migrations")],
      ["core", "speedquality-node-health", coreRepo, join(coreRepo, "migrations")],
    ]) {
      const sql = join(destination, `${name}.sql`);
      invoke(cwd, ["d1", "export", database, "--remote", "--output", sql]); chmodSync(sql, 0o600);
      const output = join(destination, name, name === "public" ? "reports.sqlite" : "core.sqlite");
      importD1({ input: sql, output, migrations });
      console.log(`${name}: SQL 已导出并通过本地数据库结构及完整性校验`);
    }
    const db = new SQLiteDatabase(join(destination, "public/reports.sqlite"));
    let rows;
    try { rows = (await db.prepare("SELECT id FROM reports WHERE bind_status != 'standalone' AND expires_at > unixepoch()").all()).results; }
    finally { db.close(); }
    const snapshotDir = join(destination, "public/snapshots"); mkdirSync(snapshotDir, { mode: 0o700 });
    for (const { id } of rows) {
      if (!/^[A-Za-z0-9_-]{12}$/.test(id)) throw new Error("无效报告 ID");
      const output = join(snapshotDir, `${id}.json.gz`);
      invoke(publicWorker, ["r2", "object", "get", `speedquality-nodequality/nodequality/${id}.json.gz`, "--remote", "--file", output]);
      chmodSync(output, 0o600);
    }
    writeFileSync(join(destination, "export-complete.json"), JSON.stringify({ completed_at: new Date().toISOString(), snapshots: rows.length, secrets_included: false }, null, 2) + "\n", { mode: 0o600, flag: "wx" });
    console.log(`导出完成: ${destination}；NQ 快照 ${rows.length} 个。仅带 export-complete.json 的目录可用于迁移。`);
  } catch (error) { console.error(`导出失败: ${error.message}`); process.exitCode = 1; }
}
