import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { requireSecret } from "./lib/runtime.mjs";

const options = {};
for (let i = 2; i < process.argv.length; i += 2) options[process.argv[i]] = process.argv[i + 1];
if (!options["--config"] || !options["--output"] || !options["--cert"] || !options["--key"]) {
  console.log("用法: node deploy/vps/nginx.mjs --config PRIVATE/public.json --output NEW.conf --cert /absolute/cert.pem --key /absolute/key.pem [--port 52800]\n生成独立 SQ 虚拟主机；不修改已有站点。文件包含代理密钥，权限 0600，禁止提交 Git。");
} else {
  try {
    let config;
    try { config = JSON.parse(readFileSync(options["--config"], "utf8")); } catch { throw new Error("无法读取私有 JSON 配置"); }
    requireSecret(config, "PROXY_SECRET");
    if (!/^[A-Za-z0-9_-]{32,128}$/.test(config.PROXY_SECRET)) throw new Error("代理密钥格式不适用于 Nginx");
    const domain = new URL(config.ORIGIN).hostname;
    if (!/^[a-z0-9.-]+$/.test(domain)) throw new Error("无效域名");
    const port = Number(options["--port"] || 52800);
    if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error("无效本地端口");
    for (const name of ["--cert", "--key"]) {
      if (!/^\/[A-Za-z0-9_./-]+$/.test(options[name])) throw new Error("证书路径必须是无空格的绝对路径");
    }
    const response = await fetch("https://api.cloudflare.com/client/v4/ips", { signal: AbortSignal.timeout(10000) });
    if (!response.ok) throw new Error("无法读取 Cloudflare 代理地址范围");
    const ranges = (await response.json()).result;
    const networks = [...(ranges?.ipv4_cidrs || []), ...(ranges?.ipv6_cidrs || [])];
    if (networks.length < 10 || networks.some((value) => !/^[a-f0-9:./]+$/i.test(value))) throw new Error("无效 Cloudflare 地址范围");
    const text = `# Generated for ${domain}. Includes a private proxy secret. Refresh CF ranges during upgrades.
# Place in an nginx http{} include such as sites-enabled, not inside another server{}.
log_format sq_access escape=json '{"time":"$time_iso8601","request_id":"$request_id","method":"$request_method","path":"$uri","status":$status,"duration":$request_time}';
limit_req_zone $binary_remote_addr zone=sq_per_ip:10m rate=50r/s;
server {
    listen 80;
    listen [::]:80;
    server_name ${domain};
    return 301 https://${domain}$request_uri;
}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name ${domain};
    ssl_certificate ${options["--cert"]};
    ssl_certificate_key ${options["--key"]};
    ssl_protocols TLSv1.2 TLSv1.3;
    ${networks.map((range) => `set_real_ip_from ${range};`).join("\n    ")}
    real_ip_header CF-Connecting-IP;
    real_ip_recursive on;
    client_max_body_size 512k;
    client_body_timeout 15s;
    access_log /var/log/nginx/speedquality.access.log sq_access;
    error_log /var/log/nginx/speedquality.error.log warn;
    location / {
        limit_req zone=sq_per_ip burst=100 nodelay;
        limit_req_status 429;
        proxy_pass http://127.0.0.1:${port};
        proxy_http_version 1.1;
        proxy_set_header Host ${domain};
        proxy_set_header Connection "";
        proxy_set_header X-SQ-Proxy-Secret "${config.PROXY_SECRET}";
        proxy_set_header X-SQ-Client-IP $remote_addr;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Forwarded-For "";
        proxy_set_header CF-Connecting-IP "";
        proxy_connect_timeout 5s;
        proxy_read_timeout 65s;
    }
}
`;
    const output = resolve(options["--output"]); mkdirSync(dirname(output), { recursive: true, mode: 0o700 });
    writeFileSync(output, text, { flag: "wx", mode: 0o600 });
    console.log(`已生成 ${output}；安装前执行 nginx -t，配置内容含密钥，请勿公开。`);
  } catch (error) { console.error(`生成失败: ${error.message}`); process.exitCode = 1; }
}
