import { MemoryCache } from "./storage.mjs";

export class DirectoryCache {
  constructor(store, { refreshSeconds = 600, retentionSeconds = 604800, retrySeconds = 60,
    maxConcurrent = 4, maxPending = 32, queueTimeoutMs = 5000, now = Date.now, onEvent = () => {} } = {}) {
    if (!Number.isInteger(refreshSeconds) || refreshSeconds < 60 || refreshSeconds > 86400 ||
        !Number.isInteger(retentionSeconds) || retentionSeconds < refreshSeconds || retentionSeconds > 604800 ||
        ![retrySeconds, maxConcurrent, maxPending, queueTimeoutMs].every((value) => Number.isInteger(value) && value > 0)) {
      throw new Error("Invalid directory refresh or retention interval");
    }
    this.store = store; this.refreshMs = refreshSeconds * 1000; this.retentionSeconds = retentionSeconds;
    this.retryMs = retrySeconds * 1000; this.now = now; this.onEvent = onEvent;
    this.pending = new Map(); this.retryAfter = new Map(); this.active = 0; this.waiters = [];
    this.maxConcurrent = maxConcurrent; this.maxPending = maxPending;
    this.queueTimeoutMs = queueTimeoutMs;
    // Brief fallback only during a Redis outage. Redis remains the durable store.
    this.recent = new MemoryCache({ maxEntries: 256, maxBytes: 8 * 1024 * 1024, now });
  }

  async acquire() {
    if (this.active < this.maxConcurrent) { this.active++; return; }
    await new Promise((resolve, reject) => {
      const waiter = { resolve: () => { clearTimeout(timer); resolve(); } };
      const timer = setTimeout(() => {
        const index = this.waiters.indexOf(waiter);
        if (index !== -1) this.waiters.splice(index, 1);
        reject(new Error("Directory queue timed out"));
      }, this.queueTimeoutMs);
      this.waiters.push(waiter);
    });
  }

  async getOrLoad(key, loader) {
    if (this.pending.has(key)) return this.pending.get(key);
    if (this.pending.size >= this.maxPending) throw new Error("Directory lookup busy");
    const operation = this.load(key, loader);
    this.pending.set(key, operation);
    try { return await operation; } finally { this.pending.delete(key); }
  }

  async load(key, loader) {
    let entry;
    try { entry = await this.store.read(key, this.retentionSeconds); }
    catch { entry = await this.recent.get(key, "json"); }
    let candidates;
    const age = this.now() - Number(entry?.checkedAt);
    if (entry && Number.isFinite(age) && age >= 0 && age <= this.retentionSeconds * 1000) {
      try { candidates = JSON.parse(entry.data); } catch { /* Discard malformed cache data. */ }
      if (!Array.isArray(candidates)) candidates = undefined;
    }
    if (candidates && age < (candidates.length ? this.refreshMs : this.retryMs)) return candidates;
    if ((this.retryAfter.get(key) || 0) > this.now()) {
      if (candidates?.length) return candidates;
      throw new Error("Directory retry deferred");
    }
    let acquired = false;
    try {
      await this.acquire(); acquired = true;
      const value = await loader();
      if (!Array.isArray(value)) throw new Error("Invalid directory response");
      const record = { data: JSON.stringify(value), checkedAt: this.now() };
      // An authoritative empty response replaces the previous directory too.
      try { await this.store.put(key, record.data, {
        checkedAt: record.checkedAt, expirationTtl: value.length ? this.retentionSeconds : Math.ceil(this.retryMs / 1000),
      }); } catch { /* A cache outage must not discard a valid upstream result. */ }
      await this.recent.put(key, JSON.stringify(record), { expirationTtl: 60 });
      this.retryAfter.delete(key);
      this.onEvent("refreshed");
      return value;
    } catch {
      if (this.retryAfter.size >= 4096) this.retryAfter.delete(this.retryAfter.keys().next().value);
      this.retryAfter.set(key, this.now() + this.retryMs);
      const fallbackAge = this.now() - Number(entry?.checkedAt);
      if (candidates?.length && fallbackAge >= 0 && fallbackAge <= this.retentionSeconds * 1000) {
        this.onEvent("stale_used"); return candidates;
      }
      throw new Error("Directory temporarily unavailable");
    } finally {
      if (acquired) {
        const next = this.waiters.shift();
        if (next) next.resolve();
        else this.active--;
      }
    }
  }
}
