import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { DatabaseSync, backup } from "node:sqlite";

// The application keeps the D1 interface; each batch is one local transaction.
export class SQLiteDatabase {
  constructor(path) {
    this.path = path;
    if (path !== ":memory:") mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    this.database = new DatabaseSync(path);
    this.database.exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA synchronous=NORMAL;");
    if (path !== ":memory:") {
      chmodSync(path, 0o600);
      this.database.exec("PRAGMA journal_mode=WAL;");
    }
  }

  prepare(sql) { return new SQLiteStatement(this, sql); }

  async batch(statements) {
    if (!statements.every((item) => item instanceof SQLiteStatement && item.owner === this)) {
      throw new Error("A batch must contain statements from the same database");
    }
    this.database.exec("BEGIN IMMEDIATE");
    try {
      const result = statements.map((statement) => statement.execute());
      this.database.exec("COMMIT");
      return result;
    } catch (error) {
      this.database.exec("ROLLBACK");
      throw error;
    }
  }

  migrate(directory) {
    this.database.exec(`CREATE TABLE IF NOT EXISTS sq_migrations (
      name TEXT PRIMARY KEY, sha256 TEXT NOT NULL, applied_at INTEGER NOT NULL
    )`);
    for (const name of migrationFiles(directory)) {
      const sql = readFileSync(join(directory, name), "utf8");
      const digest = createHash("sha256").update(sql).digest("hex");
      this.database.exec("BEGIN IMMEDIATE");
      try {
        const applied = this.database.prepare("SELECT sha256 FROM sq_migrations WHERE name = ?").get(name);
        if (applied) {
          if (applied.sha256 !== digest) throw new Error(`Previously applied migration changed: ${name}`);
        } else {
          this.database.exec(sql);
          this.database.prepare("INSERT INTO sq_migrations VALUES (?, ?, unixepoch())").run(name, digest);
        }
        this.database.exec("COMMIT");
      } catch (error) {
        this.database.exec("ROLLBACK");
        throw error;
      }
    }
  }

  async backup(destination) {
    await backup(this.database, destination);
    chmodSync(destination, 0o600);
  }

  close() { this.database.close(); }
}

class SQLiteStatement {
  constructor(owner, sql, values = []) {
    this.owner = owner;
    this.sql = sql;
    this.values = values;
  }

  bind(...values) { return new SQLiteStatement(this.owner, this.sql, values); }

  execute() {
    const db = this.owner.database;
    const before = db.prepare("SELECT total_changes() AS count").get().count;
    const results = db.prepare(this.sql).all(...this.values).map((row) => ({ ...row }));
    const changes = db.prepare("SELECT total_changes() AS count").get().count - before;
    return { success: true, results, meta: { changes: Number(changes) } };
  }

  async first(column) {
    const row = this.execute().results[0] || null;
    return column === undefined ? row : row?.[column] ?? null;
  }

  async all() { return this.execute(); }
  async run() { return this.execute(); }
}

export function migrationFiles(directory) {
  return readdirSync(directory).filter((name) => /^\d+_[a-z0-9_]+\.sql$/.test(name)).sort();
}

export function applicationSchema(database) {
  const tables = database.prepare(`SELECT name, sql FROM sqlite_schema WHERE type='table'
    AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_cf_%'
    AND name NOT IN ('d1_migrations', 'sq_migrations') ORDER BY name`).all();
  const quote = (name) => `"${name.replaceAll('"', '""')}"`;
  // SQLite/D1 exports can quote identifiers and format whitespace differently.
  // Keep string literals, CHECK constraints and index predicates in the comparison.
  const canonicalSQL = (sql) => (String(sql || "").match(/'(?:''|[^'])*'|"(?:""|[^"])*"|`[^`]*`|\[[^\]]*\]|[^\s'"`\[\]]+/g) || [])
    .map((part) => part.startsWith("'") ? part : part.replace(/["`\[\]]/g, "").toLowerCase())
    .join("").replace(/ifnotexists/g, "");
  return tables.map(({ name, sql }) => ({
    name,
    definition: canonicalSQL(sql),
    columns: database.prepare(`PRAGMA table_xinfo(${quote(name)})`).all()
      .map(({ name, type, notnull, pk, dflt_value }) => ({ name, type, notnull, pk, dflt_value })),
    foreignKeys: database.prepare(`PRAGMA foreign_key_list(${quote(name)})`).all(),
    indexes: database.prepare(`PRAGMA index_list(${quote(name)})`).all()
      .map(({ name, unique, origin, partial }) => ({ name, unique, origin, partial,
        columns: database.prepare(`PRAGMA index_xinfo(${quote(name)})`).all(),
        definition: canonicalSQL(database.prepare("SELECT sql FROM sqlite_schema WHERE name = ?").get(name)?.sql),
      })).sort((left, right) => left.name.localeCompare(right.name)),
  }));
}
