import { createClient } from "redis";

const readScript = `
local data = redis.call('HGET', KEYS[1], 'data')
if not data then return nil end
local checked = redis.call('HGET', KEYS[1], 'checked_at')
local retention = tonumber(ARGV[1])
local original_retention = tonumber(redis.call('HGET', KEYS[1], 'retention') or '0')
if retention > 0 and original_retention == retention and redis.call('TTL', KEYS[1]) < retention - 3600 then
  redis.call('EXPIRE', KEYS[1], retention)
end
return {data, checked or '0'}
`;
const writeScript = `
local changed = 0
if redis.call('HGET', KEYS[1], 'data') ~= ARGV[1] then
  redis.call('HSET', KEYS[1], 'data', ARGV[1])
  changed = 1
end
redis.call('HSET', KEYS[1], 'checked_at', ARGV[2], 'retention', ARGV[3])
redis.call('EXPIRE', KEYS[1], ARGV[3])
return changed
`;

// This store holds reconstructible cache data only. Reservations and quotas
// remain transactional database records and must never use cache fallbacks.
export class RedisCache {
  constructor({ url, prefix = "sq:cache:", timeoutMs = 1500, retryMs = 5000,
    onState = () => {}, now = Date.now } = {}) {
    let parsed;
    try { parsed = new URL(url); } catch { throw new Error("REDIS_URL is invalid"); }
    if (!["redis:", "rediss:"].includes(parsed.protocol) || !parsed.hostname || !parsed.password || parsed.hash) {
      throw new Error("REDIS_URL must specify a Redis address and password");
    }
    this.prefix = prefix; this.timeoutMs = timeoutMs; this.retryMs = retryMs;
    this.onState = onState; this.now = now; this.nextAttempt = 0; this.closed = false;
    this.client = createClient({ url, disableOfflineQueue: true, commandsQueueMaxLength: 128,
      socket: { connectTimeout: timeoutMs, reconnectStrategy: false } });
    this.client.on("error", () => this.state("unavailable"));
  }

  state(value) {
    if (value !== this.lastState) { this.lastState = value; this.onState(value); }
  }

  async command(run) {
    if (this.closed || this.now() < this.nextAttempt) throw new Error("Redis cache unavailable");
    let timer;
    try {
      const operation = (async () => {
        if (!this.client.isReady) {
          if (!this.connecting) this.connecting = this.client.connect().finally(() => { this.connecting = null; });
          await this.connecting;
        }
        if (this.closed) throw new Error("Cache closed");
        const result = await run(this.client.withCommandOptions({ timeout: this.timeoutMs }));
        this.state("ready");
        return result;
      })();
      return await Promise.race([operation, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("Cache timeout")), this.timeoutMs);
      })]);
    } catch {
      this.nextAttempt = this.now() + this.retryMs;
      this.state("unavailable");
      if (this.client.isOpen) this.client.destroy();
      throw new Error("Redis cache unavailable");
    } finally { clearTimeout(timer); }
  }

  async read(key, retentionSeconds = 0) {
    const result = await this.command((client) => client.eval(readScript, {
      keys: [this.prefix + key], arguments: [String(retentionSeconds)],
    }));
    return result ? { data: result[0], checkedAt: Number(result[1]) } : null;
  }

  async get(key, type) {
    const entry = await this.read(key);
    return entry ? (type === "json" ? JSON.parse(entry.data) : entry.data) : null;
  }

  async put(key, value, { expirationTtl = 600, checkedAt = this.now() } = {}) {
    if (typeof key !== "string" || typeof value !== "string" || Buffer.byteLength(value) > 1024 * 1024 ||
        !Number.isInteger(expirationTtl) || expirationTtl < 1 || expirationTtl > 604800 ||
        !Number.isSafeInteger(checkedAt) || checkedAt < 0) throw new Error("Invalid cache entry");
    return this.command((client) => client.eval(writeScript, {
      keys: [this.prefix + key], arguments: [value, String(checkedAt), String(expirationTtl)],
    }));
  }

  async delete(key) { await this.command((client) => client.del(this.prefix + key)); }
  async ping() { return this.command((client) => client.ping()); }
  close() { this.closed = true; if (this.client.isOpen) this.client.destroy(); }
}
