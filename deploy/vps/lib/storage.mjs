import { randomBytes } from "node:crypto";
import { constants } from "node:fs";
import { mkdir, open, rename, unlink } from "node:fs/promises";
import { join } from "node:path";

export class MemoryCache {
  constructor({ maxEntries = 5000, maxBytes = 32 * 1024 * 1024, now = Date.now } = {}) {
    this.entries = new Map();
    this.bytes = 0;
    this.maxEntries = maxEntries;
    this.maxBytes = maxBytes;
    this.now = now;
  }

  async get(key, type) {
    const item = this.entries.get(key);
    if (!item) return null;
    if (item.expires <= this.now()) { this.remove(key); return null; }
    this.entries.delete(key);
    this.entries.set(key, item);
    return type === "json" ? JSON.parse(item.value) : item.value;
  }

  async put(key, value, options = {}) {
    if (typeof key !== "string" || typeof value !== "string") throw new TypeError("Cache values must be strings");
    const size = Buffer.byteLength(key) + Buffer.byteLength(value);
    const ttl = Number(options.expirationTtl ?? 120);
    if (!Number.isFinite(ttl) || ttl <= 0) throw new Error("Invalid cache expiration");
    this.remove(key);
    if (size > this.maxBytes) return;
    for (const [existing, item] of this.entries) {
      if (item.expires <= this.now()) this.remove(existing);
    }
    while (this.entries.size && (this.entries.size >= this.maxEntries || this.bytes + size > this.maxBytes)) {
      this.remove(this.entries.keys().next().value);
    }
    this.entries.set(key, { value, size, expires: this.now() + ttl * 1000 });
    this.bytes += size;
  }

  async delete(key) { this.remove(key); }

  remove(key) {
    const item = this.entries.get(key);
    if (item) { this.bytes -= item.size; this.entries.delete(key); }
  }
}

export class FileSnapshots {
  constructor(directory) { this.directory = directory; }

  path(key) {
    const match = String(key).match(/^nodequality\/([A-Za-z0-9_-]{12})\.json\.gz$/);
    if (!match) throw new Error("Invalid snapshot key");
    return join(this.directory, `${match[1]}.json.gz`);
  }

  async put(key, value) {
    const path = this.path(key);
    const bytes = Buffer.from(value);
    if (bytes.length > 300 * 1024) throw new Error("Snapshot exceeds storage limit");
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const temporary = `${path}.${randomBytes(12).toString("hex")}.tmp`;
    let handle;
    try {
      handle = await open(temporary, "wx", 0o600);
      await handle.writeFile(bytes);
      await handle.sync();
      await handle.close();
      handle = null;
      await rename(temporary, path);
    } finally {
      await handle?.close();
      await unlink(temporary).catch((error) => { if (error.code !== "ENOENT") throw error; });
    }
  }

  async get(key) {
    let handle;
    try {
      handle = await open(this.path(key), constants.O_RDONLY | constants.O_NOFOLLOW);
      const stat = await handle.stat();
      if (!stat.isFile() || stat.size > 300 * 1024) throw new Error("Invalid snapshot file");
      const bytes = await handle.readFile();
      return { body: new Blob([bytes]).stream() };
    } catch (error) {
      if (error.code === "ENOENT") return null;
      throw error;
    } finally {
      await handle?.close();
    }
  }

  async delete(keys) {
    for (const key of Array.isArray(keys) ? keys : [keys]) {
      await unlink(this.path(key)).catch((error) => { if (error.code !== "ENOENT") throw error; });
    }
  }
}
