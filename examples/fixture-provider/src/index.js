const encoder = new TextEncoder();
const REGIONS = Object.freeze({ bj: "北京", sh: "上海", gd: "广东" });
const CARRIERS = new Set(["ct", "cu", "cm"]);

function response(value, status = 200) {
  return Response.json(value, {
    status,
    headers: { "cache-control": "no-store", "x-content-type-options": "nosniff" },
  });
}

function hostPort(address, port) {
  return address.includes(":") ? `[${address}]:${port}` : `${address}:${port}`;
}

function validNode(node, family) {
  if (!node || typeof node !== "object" || Array.isArray(node) || !CARRIERS.has(node.carrier)) return false;
  if (!Number.isInteger(node.port) || node.port < 1 || node.port > 65535) return false;
  const address = String(node.address || "");
  if (family === "v4") {
    const parts = address.split(".");
    return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255);
  }
  return address.includes(":") && /^[0-9a-f:.]+$/i.test(address);
}

async function nodeID(node) {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    encoder.encode(`${node.carrier}|${node.address}|${node.port}`),
  );
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("")
    .slice(0, 32);
}

function parseConfiguration(env) {
  try {
    const value = JSON.parse(String(env.FIXTURE_NODES || "{}"));
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  } catch {
    return {};
  }
}

async function createLease(input, env) {
  if (!Object.hasOwn(REGIONS, input?.region) || !["v4", "v6"].includes(input?.family) ||
      input?.duration_seconds !== 5 || ![100, 200, 400].includes(input?.target_mbps) ||
      !Array.isArray(input?.modes) || input.modes.length !== 1 || input.modes[0] !== "s") {
    return response({ error: "invalid_request" }, 400);
  }
  const configured = parseConfiguration(env);
  const nodes = Array.isArray(configured[input.region])
    ? configured[input.region].filter((node) => validNode(node, input.family))
    : [];
  if (nodes.length === 0) return response({ error: "no_fixture_nodes" }, 503);
  const groups = [];
  for (const carrier of CARRIERS) {
    const matching = nodes.filter((node) => node.carrier === carrier).slice(0, 3);
    if (matching.length === 0) continue;
    groups.push({
      carrier,
      label: `${REGIONS[input.region]}${carrier.toUpperCase()}`,
      candidates: await Promise.all(matching.map(async (node) => {
        const endpoint = hostPort(node.address, node.port);
        return {
          id: await nodeID(node),
          address: node.address,
          port: node.port,
          activate: {
            method: "POST",
            url: `http://${endpoint}/activate`,
            key_prefix_bytes: 0,
            ready_delay_ms: 0,
          },
          download: {
            method: "GET",
            url: `http://${endpoint}/download?key={key}&nonce={nonce}`,
          },
          upload: {
            method: "POST",
            url: `http://${endpoint}/upload?key={key}`,
            content_length: 900000000,
          },
          release: {
            method: "POST",
            url: `http://${endpoint}/release?key={key}`,
          },
        };
      })),
    });
  }
  if (groups.length === 0) return response({ error: "no_fixture_nodes" }, 503);
  const now = Math.floor(Date.now() / 1000);
  return response({
    version: 1,
    lease_id: `fixture_${input.region}_${input.family}_${now}`,
    issued_at: now,
    expires_at: now + 300,
    region: { code: input.region, name: REGIONS[input.region] },
    family: input.family,
    duration_seconds: input.duration_seconds,
    target_mbps: input.target_mbps,
    modes: input.modes,
    targets: groups,
  });
}

export async function handleFetch(request, env) {
  const url = new URL(request.url);
  if (request.headers.get("x-node-core-secret") !== String(env.INTERNAL_SECRET || "")) {
    return response({ error: "unauthorized" }, 401);
  }
  if (url.pathname === "/feedback" && request.method === "POST") {
    return response({ accepted: 0 });
  }
  if (url.pathname !== "/lease" || request.method !== "POST") {
    return response({ error: "not_found" }, 404);
  }
  try {
    return createLease(await request.json(), env);
  } catch {
    return response({ error: "invalid_json" }, 400);
  }
}

export default { fetch: handleFetch };
