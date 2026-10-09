import { createHash, randomBytes } from "node:crypto";
import { mkdir, open, readFile, readdir, rename, stat, unlink } from "node:fs/promises";
import { join } from "node:path";

export class FileAssetSource {
  constructor({ directory, repositoryRoot, owner, repository, ref, version, fetcher = fetch }) {
    this.directory = directory;
    this.repositoryRoot = repositoryRoot;
    this.fetcher = fetcher;
    this.pending = new Map();
    this.allowed = new Set();
    this.scripts = new Map();
    for (const name of ["run.sh", "install-node.sh"]) {
      const url = `https://raw.githubusercontent.com/${owner}/${repository}/${ref}/${name}`;
      this.scripts.set(url, join(repositoryRoot, name));
    }
    for (const asset of ["sqprobe-linux-amd64", "sqprobe-linux-arm64", "sq-node-linux-amd64",
      "sq-node-linux-arm64", "checksums.txt", "release-manifest.json", "release-manifest.json.sig"]) {
      this.allowed.add(`https://github.com/${owner}/${repository}/releases/download/${version}/${asset}`);
    }
  }

  async fetch(url) {
    const target = String(url);
    if (this.scripts.has(target)) return new Response(await readFile(this.scripts.get(target)));
    if (!this.allowed.has(target)) throw new Error("Unrecognized release resource");
    const key = createHash("sha256").update(target).digest("hex");
    const path = join(this.directory, `${key}.bin`);
    try { return new Response(await readFile(path)); } catch (error) { if (error.code !== "ENOENT") throw error; }
    if (!this.pending.has(key)) {
      if (this.pending.size >= 8) return new Response("Release download busy", { status: 503 });
      this.pending.set(key, this.download(target, path));
    }
    try {
      await this.pending.get(key);
      return new Response(await readFile(path));
    } finally { this.pending.delete(key); }
  }

  async download(url, path) {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const temporary = `${path}.${randomBytes(12).toString("hex")}.tmp`;
    const response = await this.fetcher(url, { signal: AbortSignal.timeout(45_000),
      headers: { "user-agent": "SpeedQuality-Release-Proxy/1.1" } });
    if (!response.ok) { await response.body?.cancel(); throw new Error("Release download failed"); }
    const reader = response.body.getReader();
    let handle;
    try {
      handle = await open(temporary, "wx", 0o600);
      let size = 0;
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        size += value.length;
        if (size > 32 * 1024 * 1024) throw new Error("Release resource exceeds size limit");
        await handle.writeFile(value);
      }
      if (size === 0) throw new Error("Empty release resource");
      await handle.sync();
      await handle.close();
      handle = null;
      await rename(temporary, path);
      await this.prune();
    } finally {
      await reader.cancel().catch(() => {});
      await handle?.close();
      await unlink(temporary).catch((error) => { if (error.code !== "ENOENT") throw error; });
    }
  }

  async prune() {
    const files = [];
    for (const name of await readdir(this.directory)) {
      if (!/^[a-f0-9]{64}\.bin$/.test(name)) continue;
      const path = join(this.directory, name);
      const info = await stat(path).catch(() => null);
      if (info?.isFile()) files.push({ path, size: info.size, time: info.mtimeMs });
    }
    files.sort((left, right) => left.time - right.time);
    let bytes = files.reduce((sum, file) => sum + file.size, 0);
    while (files.length > 64 || bytes > 512 * 1024 * 1024) {
      const file = files.shift();
      await unlink(file.path).catch((error) => { if (error.code !== "ENOENT") throw error; });
      bytes -= file.size;
    }
  }
}
