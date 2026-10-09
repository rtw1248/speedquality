import { timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import { isIP } from "node:net";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";

function equalSecret(actual, expected) {
  const left = Buffer.from(actual || "");
  const right = Buffer.from(expected || "");
  return left.length === right.length && timingSafeEqual(left, right);
}

function normalizedIP(value) {
  const address = String(value || "").replace(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/, "$1");
  return isIP(address) ? address : "";
}

function readBody(request, maximum) {
  return new Promise((resolve, reject) => {
    let size = 0;
    let failed = false;
    const chunks = [];
    request.on("data", (chunk) => {
      if (failed) return;
      size += chunk.length;
      if (size > maximum) {
        failed = true;
        chunks.length = 0;
        reject(Object.assign(new Error("Request body too large"), { status: 413 }));
      } else chunks.push(chunk);
    });
    request.once("end", () => { if (!failed) resolve(Buffer.concat(chunks)); });
    request.once("error", reject);
    request.once("aborted", () => reject(Object.assign(new Error("Request aborted"), { status: 400 })));
  });
}

export function createHTTPServer({ handler, origin, proxySecret = "", resolveGeo,
  maxBodyBytes = 512 * 1024, maxRequests = 32 }) {
  const canonical = new URL(origin);
  let active = 0;
  const server = createServer(async (incoming, outgoing) => {
    const fail = (status, message) => {
      if (outgoing.headersSent) { outgoing.destroy(); return; }
      outgoing.writeHead(status, { "content-type": "text/plain; charset=utf-8",
        "cache-control": "no-store", connection: "close" });
      outgoing.end(`${message}\n`);
      incoming.resume();
    };
    if (active >= maxRequests) { fail(503, "Server busy"); return; }
    active++;
    const controller = new AbortController();
    incoming.once("aborted", () => controller.abort());
    outgoing.once("close", () => { if (!outgoing.writableFinished) controller.abort(); });
    try {
      if (!incoming.url?.startsWith("/") || incoming.url.startsWith("//")) {
        fail(400, "Invalid request target"); return;
      }
      const url = new URL(incoming.url, canonical);
      if (url.origin !== canonical.origin) { fail(400, "Invalid request target"); return; }
      const health = url.pathname === "/health" && ["GET", "HEAD"].includes(incoming.method);
      if (proxySecret && !health && !equalSecret(incoming.headers["x-sq-proxy-secret"], proxySecret)) {
        fail(403, "Trusted proxy required"); return;
      }
      const clientIP = proxySecret && !health
        ? normalizedIP(incoming.headers["x-sq-client-ip"])
        : normalizedIP(incoming.socket.remoteAddress);
      if (!clientIP) { fail(400, "Invalid client address"); return; }
      const declaredLength = Number(incoming.headers["content-length"] || 0);
      if (!Number.isSafeInteger(declaredLength) || declaredLength < 0 || declaredLength > maxBodyBytes) {
        fail(413, "Request body too large"); return;
      }
      const headers = new Headers();
      for (const [key, value] of Object.entries(incoming.headers)) {
        if (value !== undefined) headers.set(key, Array.isArray(value) ? value.join(", ") : value);
      }
      for (const name of ["connection", "transfer-encoding", "keep-alive", "upgrade",
        "proxy-authorization", "proxy-connection", "x-forwarded-for", "x-forwarded-host",
        "x-forwarded-proto", "true-client-ip", "x-sq-proxy-secret", "x-sq-client-ip"]) headers.delete(name);
      headers.set("host", canonical.host);
      headers.set("cf-connecting-ip", clientIP);
      const body = ["GET", "HEAD"].includes(incoming.method)
        ? undefined : await readBody(incoming, maxBodyBytes);
      const request = new Request(url, { method: incoming.method, headers, body, signal: controller.signal });
      if (resolveGeo && /^\/api\/(?:session|node-lease|results|nodes\/(?:detect|register|heartbeat))$/.test(url.pathname)) {
        Object.defineProperty(request, "cf", { value: await resolveGeo(clientIP) });
      }
      const response = await handler(request);
      outgoing.writeHead(response.status, Object.fromEntries(response.headers));
      if (!response.body || incoming.method === "HEAD") {
        await response.body?.cancel();
        outgoing.end();
      } else {
        await pipeline(Readable.fromWeb(response.body), outgoing);
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        if (!error.status) console.error(JSON.stringify({ event: "vps.request_failed", error_type: error.name }));
        fail(error.status || 500, error.status === 413 ? "Request body too large" : "Request failed");
      }
    } finally { active--; }
  });
  server.requestTimeout = 15_000;
  server.headersTimeout = 10_000;
  server.keepAliveTimeout = 5_000;
  server.maxConnections = 256;
  server.setTimeout(65_000, (socket) => socket.destroy());
  return server;
}

export async function listen(server, host, port) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => { server.off("error", reject); resolve(); });
  });
  return server.address();
}

export async function closeServer(server) {
  const timer = setTimeout(() => server.closeAllConnections(), 15_000);
  timer.unref();
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  clearTimeout(timer);
}

export function scheduleTask(name, task, interval) {
  let running = null;
  let stopped = false;
  const run = () => {
    if (running || stopped) return;
    running = Promise.resolve().then(task).catch((error) => {
      console.error(JSON.stringify({ event: "vps.scheduled_failed", task: name, error_type: error.name }));
    }).finally(() => { running = null; });
  };
  const timer = setInterval(run, interval);
  timer.unref();
  run();
  return async () => { stopped = true; clearInterval(timer); await running; };
}
