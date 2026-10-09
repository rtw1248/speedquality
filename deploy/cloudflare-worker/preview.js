import { readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";

import { renderReport, validateNodeQualitySnapshot } from "./src/index.js";

const host = "127.0.0.1";
const port = Number(process.env.PORT || 4173);

function sampleReport(origin, bindStatus, options = {}) {
  const now = Math.floor(Date.now() / 1000);
  const testedAt = now - 120;
  const linked = ["verified", "verified_stale", "verified_time_unknown"].includes(bindStatus);
  const stale = bindStatus === "verified_stale";
  const timeUnknown = bindStatus === "verified_time_unknown";
  const timeGapSeconds = linked && !timeUnknown ? (stale ? 4200 : 300) : null;
  const targetMbps = Number(options.targetMbps || 200);
  const speedReports = sampleSpeedReports(now, { ...options, targetMbps });
  const regionNames = [...new Set(speedReports.map((report) => report.region.name))];
  const families = [...new Set(speedReports.map((report) => report.family))];
  return {
    id: options.id || "AbCdEfGhIjKl",
    created_at: now,
    expires_at: now + 90 * 86400,
    tested_at: testedAt,
    regions: regionNames.join("、"),
    mode: "s",
    ip_mode: families.length > 1 ? "v6" : families[0] || "v4",
    source_ip_masked: "203.0.*.*",
    target_mbps: targetMbps,
    speed_url: "",
    speed_text: "SpeedQuality preview",
    speed_data: JSON.stringify(speedReports),
    duration_seconds: 5,
    traffic_rx_bytes: Object.hasOwn(options, "trafficRxBytes")
      ? options.trafficRxBytes
      : 1830000000,
    traffic_tx_bytes: Object.hasOwn(options, "trafficTxBytes")
      ? options.trafficTxBytes
      : 620000000,
    nq_url: linked
      ? "https://nodequality.com/r/Q9jkabAvIcQMO49iTVJ72hYjMqwN3Veo"
      : "",
    nq_tested_at: timeGapSeconds == null ? null : testedAt - timeGapSeconds,
    nq_time_source: linked && !timeUnknown ? "network.log" : "",
    time_gap_seconds: timeGapSeconds,
    nq_identity_reason: linked ? options.identityReason || "masked_ip_and_asn" : "",
    bind_status: bindStatus,
    version: "1.0.10",
  };
}

function sampleSpeedReport(code, name, family, now, options = {}) {
  const targetMbps = Number(options.targetMbps || 200);
  const values = options.values || [
    ["ct", "电信", 9.9, targetMbps, targetMbps * 0.1],
    ["cu", "联通", 14.2, targetMbps * 0.9325, targetMbps * 0.092],
    ["cm", "移动", 18.7, targetMbps * 0.8615, targetMbps * 0.079],
  ];
  return {
    version: 1,
    lease_id: `preview_${code}_${family}`,
    started_at: now - 60,
    completed_at: now,
    region: { code, name },
    family,
    duration_seconds: 5,
    target_mbps: targetMbps,
    modes: ["s"],
    results: values.map(([carrier, carrierName, latency, singleDown, singleUp], index) => ({
      carrier,
      label: `${name}${carrierName}`,
      node_id: String(index + 1).repeat(32),
      latency_ms: latency,
      status: "ok",
      error: "",
      single: { download_mbps: singleDown, upload_mbps: singleUp, download_bytes: 100000000, upload_bytes: 10000000 },
    })),
  };
}

function sampleSpeedReports(now, options = {}) {
  const targetMbps = Number(options.targetMbps || 200);
  const report = (code, name, family, extra = {}) =>
    sampleSpeedReport(code, name, family, now, { targetMbps, ...extra });
  switch (options.profile) {
    case "single":
      return [report("hb", "湖北", "v4")];
    case "dual-stack":
      return [
        report("hb", "湖北", "v4"),
        report("hb", "湖北", "v6", {
          values: [
            ["ct", "电信", 109.2, 1.54, 1.31],
            ["cu", "联通", 198.1, 104.88, 113.7],
            ["cm", "移动", 91.56, targetMbps, targetMbps],
          ],
        }),
      ];
    case "node-unavailable": {
      const unavailable = report("hb", "湖北", "v4");
      for (const index of [1, 2]) {
        unavailable.results[index] = {
          ...unavailable.results[index],
          latency_ms: null,
          status: "failed",
          error: "没有可连接的候选节点",
          single: null,
        };
      }
      return [unavailable];
    }
    case "transfer-failed": {
      const failed = report("sh", "上海", "v4");
      failed.results[1] = {
        ...failed.results[1],
        latency_ms: null,
        status: "failed",
        error: "测速连接未产生有效数据",
        single: null,
      };
      return [failed];
    }
    case "thresholds":
      return [report("sh", "上海", "v4", {
        values: [
          ["ct", "电信", 80, targetMbps, targetMbps * 0.9],
          ["cu", "联通", 150, targetMbps * 0.5, targetMbps * 0.79],
          ["cm", "移动", 250, targetMbps * 0.2, targetMbps * 0.29],
        ],
      })];
    default:
      return [
        report("hb", "湖北", "v4"),
        report("bj", "北京", "v4", {
          values: [
            ["ct", "电信", 42.1, targetMbps, targetMbps * 0.9695],
            ["cu", "联通", 38.3, targetMbps, targetMbps * 0.0096],
            ["cm", "移动", 51.6, targetMbps, targetMbps * 0.9765],
          ],
        }),
      ];
  }
}

function sampleSnapshot() {
  const snapshot = {
    version: 2,
    truncated: false,
    omitted_files: 0,
    pages: [
      {
        id: "basic",
        title: "基本信息",
        format: "ansi",
        truncated: false,
        content: [
          "\x1b[36m++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++\x1b[0m",
          "                         \x1b[1m硬件质量体检报告：\x1b[36m45.78.*.*\x1b[0m",
          "                    \x1b[4mhttps://github.com/xykt/HardwareQuality\x1b[0m",
          "            报告时间：2026-09-26 16:20:18 CST  脚本版本：preview",
          "\x1b[36m++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++\x1b[0m",
          "一、操作系统信息",
          "\x1b[36m容器/虚拟化：          \x1b[32mKVM 虚拟机\x1b[0m",
          "\x1b[36m架构：                 \x1b[32mx86_64\x1b[0m",
          "\x1b[36m操作系统/内核：        \x1b[32mDebian GNU/Linux 12 (bookworm) 6.1.0-cloud-amd64\x1b[0m",
          "\x1b[36m运行时间：             \x1b[32m12 天 8 小时 36 分钟\x1b[0m",
          "二、CPU 测评",
          "\x1b[36mCPU：                  \x1b[1;30;47mAMD EPYC Processor 2核心 / 2线程\x1b[0m",
          "\x1b[36mSysbench：             \x1b[32m单线程 3227.72     多线程 6330.01\x1b[0m",
          "三、内存与硬盘",
          "\x1b[36m内存：                 \x1b[32m3.8 GB，可用 3.4 GB (88%)\x1b[0m",
          "\x1b[36m硬盘：                 \x1b[32m20 GB，可用 16 GB (85%)\x1b[0m",
          "================================================================================",
        ].join("\n"),
        image_url: "",
        source: "hardware_quality.log",
      },
      {
        id: "ip-quality",
        title: "IP质量",
        format: "ansi",
        truncated: false,
        content: [
          "########################################################################",
          "                      \x1b[1mIP质量体检报告：\x1b[36m45.78.*.*\x1b[0m",
          "                   \x1b[4mhttps://github.com/xykt/IPQuality\x1b[0m",
          "        报告时间：2026-09-26 16:20:18 CST  脚本版本：preview",
          "########################################################################",
          "一、基础信息（Maxmind 数据库）",
          "\x1b[36m自治系统号：            \x1b[32mAS25820\x1b[0m",
          "\x1b[36m组织：                  \x1b[32mFixture Network\x1b[0m",
          "\x1b[36m城市：                  \x1b[32mHong Kong\x1b[0m",
          "\x1b[36mIP类型：                \x1b[1;37;41m 机房 IP \x1b[0m",
          "二、风险评分",
          "\x1b[36mIP2Location：           \x1b[1;32m低风险\x1b[0m",
          "\x1b[36mScamalytics：           \x1b[1;32m低风险\x1b[0m",
          "三、流媒体及 AI 服务解锁检测",
          "\x1b[36mNetflix：               \x1b[37;42m 解锁 \x1b[0m",
          "\x1b[36mYouTube Premium：       \x1b[37;42m 解锁 \x1b[0m",
          "\x1b[36mChatGPT：               \x1b[37;42m 解锁 \x1b[0m",
          "========================================================================",
        ].join("\n"),
        image_url: "",
        source: "ip_quality.log",
      },
      {
        id: "network-quality",
        title: "网络质量",
        format: "image",
        truncated: false,
        content: "",
        image_url: "https://i.111666.best/image/mmLMb3ere9yBLZGmrJXp1i.webp",
        source: "nodequality.md",
      },
      {
        id: "return-route",
        title: "回程路由",
        format: "image",
        truncated: false,
        content: "",
        image_url: "https://i.111666.best/image/RmXPpQzQner7jVeloG10BY.webp",
        source: "nodequality.md",
      },
    ],
  };
  const header = [
    "\x1b[0;36m########################################################################",
    "                  bash <(curl -sL https://run.NodeQuality.com)",
    "                   https://github.com/LloydAsp/NodeQuality",
    "        报告时间：2026-09-29 14:31:36 CST  脚本版本：preview",
    "########################################################################\x1b[0m",
  ].join("\n");
  const networkQuality = [
    "********************************************************************************",
    "                         \x1b[1m网络质量体检报告：\x1b[36m203.0.113.*\x1b[0m",
    "                       \x1b[4mhttps://github.com/xykt/NetQuality\x1b[0m",
    "            报告时间：2026-09-29 14:43:50 CST  脚本版本：preview",
    "********************************************************************************",
    "一、BGP信息（BGP.TOOLS & HE.NET）",
    "\x1b[36m注册信息：          \x1b[32mTEST-NET, AS64496 Example Network, Prefix/24\x1b[0m",
    "二、本地策略",
    "\x1b[36mNAT类型：            \x1b[42;37;1m 开放网络无NAT \x1b[0m",
    "\x1b[36mTCP拥塞控制算法：    \x1b[32mbbr\x1b[0m",
    "三、三网TCP大包延迟（依次为电信|联通|移动）",
    "\x1b[36m京\x1b[32m⣀⣀⣀⣀⣀\x1b[1m46  ⣀⣀⣀⣀⣀53  ⣀⣀⣀⣀⣀66\x1b[0m",
    "\x1b[36m沪\x1b[32m⣀⣀⣀⣀⣀\x1b[1m28  ⣀⣀⣀⣀⣀40  ⣀⣀⣀⣀⣀36\x1b[0m",
    "四、三网回程路由",
    "\x1b[36m北京TCP：\x1b[32m电信 AS64496->163 || 联通 AS64496->4837 || 移动 AS64496->CMI\x1b[0m",
  ].join("\n");
  const returnRoute = [
    "********************************************************************************",
    "                         \x1b[1m网络质量体检报告：\x1b[36m203.0.113.*\x1b[0m",
    "                       \x1b[4mhttps://github.com/xykt/NetQuality\x1b[0m",
    "            报告时间：2026-09-29 14:57:01 CST  脚本版本：preview",
    "********************************************************************************",
    "五、三网回程路由（NextTrace API）",
    "\x1b[46;37;1m  北京 电信  \x1b[0m\x1b[47;36;1m  AS64496 -> 163  \x1b[0m",
    "\x1b[44;37m地理路径：gateway -> 香港 -> 北京    自治系统路径：AS64496 -> AS4134\x1b[0m",
    "\x1b[1m 1   \x1b[0m    \x1b[32m0.27ms\x1b[0m  192.0.2.*    \x1b[1mAS64496   [EXAMPLE]\x1b[0m",
    "\x1b[1m 8   \x1b[0m   \x1b[32m42.10ms\x1b[0m  203.0.113.*  \x1b[1mAS4134    [CHINANET-BACKBONE]\x1b[0m",
    "\x1b[42;37;1m  上海 联通  \x1b[0m\x1b[47;32;1m  AS64496 -> 4837  \x1b[0m",
    "\x1b[44;37m地理路径：gateway -> 香港 -> 上海    自治系统路径：AS64496 -> AS4837\x1b[0m",
    "\x1b[1m 1   \x1b[0m    \x1b[32m0.25ms\x1b[0m  192.0.2.*    \x1b[1mAS64496   [EXAMPLE]\x1b[0m",
    "\x1b[1m10   \x1b[0m   \x1b[32m38.30ms\x1b[0m  203.0.113.*  \x1b[1mAS4837    [CU169-BACKBONE]\x1b[0m",
  ].join("\n");
  snapshot.pages[2] = {
    id: "network-quality", title: "网络质量", format: "ansi", truncated: false,
    content: networkQuality, image_url: "", source: "net_quality.log",
  };
  snapshot.pages[3] = {
    id: "return-route", title: "回程路由", format: "ansi", truncated: false,
    content: returnRoute, image_url: "", source: "backroute_trace.log",
  };
  snapshot.pages.unshift({
    id: "all",
    title: "全部",
    format: "ansi",
    truncated: false,
    content: [header, ...snapshot.pages.map((page) => page.content)].join("\n\n"),
    image_url: "",
    source: "header_info.log,hardware_quality.log,ip_quality.log,net_quality.log,backroute_trace.log",
  });
  return snapshot;
}

async function loadPreviewSnapshot() {
  const filename = String(process.env.SPEEDQUALITY_PREVIEW_SNAPSHOT || "").trim();
  const source = filename || new URL("../../.cache/nodequality-snapshot.json", import.meta.url);
  let contents;
  try {
    contents = await readFile(source, "utf8");
  } catch (error) {
    if (!filename && error?.code === "ENOENT") return sampleSnapshot();
    throw error;
  }
  const validated = validateNodeQualitySnapshot(contents);
  if (validated.error) throw new Error(`Invalid preview snapshot: ${validated.error}`);
  return validated.value;
}

const previewSnapshot = await loadPreviewSnapshot();

function html(response, status = 200) {
  return {
    status,
    headers: {
      "content-type": "text/html; charset=utf-8",
      "cache-control": "no-store",
    },
    body: response,
  };
}

const server = createServer(async (request, response) => {
  const origin = `http://${request.headers.host || `${host}:${port}`}`;
  const url = new URL(request.url || "/", origin);

  if (url.pathname === "/") {
    response.writeHead(302, { location: "/r/AbCdEfGhIjKl" });
    response.end();
    return;
  }

  const variants = {
    "/r/AbCdEfGhIjKl": "verified",
    "/r/StaleDemo123": "verified_stale",
    "/r/TimeUnknown1": "verified_time_unknown",
    "/r/SoloDemo1234": "standalone",
  };
  const bindStatus = variants[url.pathname];
  if (!bindStatus) {
    response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
    response.end("Not Found\n");
    return;
  }

  const tab = url.searchParams.get("tab") || "overview";
  const result = html(renderReport(sampleReport(origin, bindStatus), {
    tab,
    reportPath: url.pathname,
    reportUrl: `${origin}${url.pathname}`,
    snapshot: bindStatus === "standalone" ? null : previewSnapshot,
  }));
  response.writeHead(result.status, result.headers);
  response.end(result.body);
});

if (process.argv.includes("--build")) {
  const outputs = {
    "report.html": { status: "verified", tab: "nq-all" },
    "report-basic.html": { status: "verified", tab: "nq-basic" },
    "report-ip-quality.html": { status: "verified", tab: "nq-ip-quality" },
    "report-network-quality.html": { status: "verified", tab: "nq-network-quality" },
    "report-route.html": { status: "verified", tab: "nq-return-route" },
    "report-speed.html": { status: "verified", tab: "sq" },
    "report-nodequality.html": { status: "verified", tab: "nodequality" },
    "report-stale.html": { status: "verified_stale", tab: "nq-all" },
    "report-stale-basic.html": { status: "verified_stale", tab: "nq-basic" },
    "report-stale-ip-quality.html": { status: "verified_stale", tab: "nq-ip-quality" },
    "report-stale-network-quality.html": { status: "verified_stale", tab: "nq-network-quality" },
    "report-stale-route.html": { status: "verified_stale", tab: "nq-return-route" },
    "report-stale-speed.html": { status: "verified_stale", tab: "sq" },
    "report-stale-nodequality.html": { status: "verified_stale", tab: "nodequality" },
    "report-time-unknown-speed.html": { status: "verified_time_unknown", tab: "sq" },
    "report-standalone.html": { status: "standalone", tab: "sq" },
    "report-standalone-speed.html": { status: "standalone", tab: "speed" },
    "result-success-ipv4.html": {
      status: "standalone", tab: "sq", profile: "single", id: "SuccessIPv4A",
    },
    "result-dual-stack.html": {
      status: "standalone", tab: "sq", profile: "dual-stack", id: "DualStack123",
    },
    "result-node-unavailable.html": {
      status: "standalone", tab: "sq", profile: "node-unavailable", id: "NoNodeDemo12",
    },
    "result-transfer-failed.html": {
      status: "standalone", tab: "sq", profile: "transfer-failed", id: "FailedDemo12",
    },
    "result-color-thresholds.html": {
      status: "standalone", tab: "sq", profile: "thresholds", id: "ColorsDemo12",
    },
    "result-speed-100.html": {
      status: "standalone", tab: "sq", profile: "single", targetMbps: 100, id: "Speed100Demo",
    },
    "result-speed-400.html": {
      status: "standalone", tab: "sq", profile: "single", targetMbps: 400, id: "Speed400Demo",
    },
    "result-no-traffic.html": {
      status: "standalone", tab: "sq", profile: "single", trafficRxBytes: null,
      trafficTxBytes: null, id: "NoTraffic123",
    },
    "report-nq-snapshot-unavailable.html": {
      status: "verified", tab: "nodequality", snapshotMode: "none", id: "NoSnapshot12",
    },
    "report-nq-page-truncated.html": {
      status: "verified", tab: "nq-basic", snapshotMode: "truncated", id: "Truncated123",
    },
    "validation-full-ip-ok.html": {
      status: "verified", tab: "sq", identityReason: "full_ip", id: "FullIPDemo12",
    },
    "validation-masked-ip-ok.html": {
      status: "verified", tab: "sq", identityReason: "masked_ip_and_asn", id: "MaskedIPDemo",
    },
    "validation-time-stale.html": {
      status: "verified_stale", tab: "sq", identityReason: "masked_ip_and_asn", id: "StaleDemo123",
    },
    "validation-time-unknown.html": {
      status: "verified_time_unknown", tab: "sq", identityReason: "masked_ip_and_asn", id: "TimeUnknown1",
    },
    "validation-identity-mismatch.html": {
      status: "mismatch", tab: "sq", id: "MismatchDemo1",
    },
    "validation-report-unavailable.html": {
      status: "unverified", tab: "sq", id: "UnreadableNQ1",
    },
  };
  const linkedTabs = {
    "nq-all": "./report.html",
    "nq-basic": "./report-basic.html",
    "nq-ip-quality": "./report-ip-quality.html",
    "nq-network-quality": "./report-network-quality.html",
    "nq-return-route": "./report-route.html",
    sq: "./report-speed.html",
  };
  const staleTabs = {
    "nq-all": "./report-stale.html",
    "nq-basic": "./report-stale-basic.html",
    "nq-ip-quality": "./report-stale-ip-quality.html",
    "nq-network-quality": "./report-stale-network-quality.html",
    "nq-return-route": "./report-stale-route.html",
    sq: "./report-stale-speed.html",
  };
  const standaloneTabs = {
    sq: "./report-standalone.html",
  };
  const timeUnknownTabs = {
    ...linkedTabs,
    sq: "./report-time-unknown-speed.html",
  };
  const snapshotFor = (config) => {
    if (!["verified", "verified_stale", "verified_time_unknown"].includes(config.status) ||
        config.snapshotMode === "none") return null;
    if (config.snapshotMode !== "truncated") return previewSnapshot;
    const snapshot = JSON.parse(JSON.stringify(previewSnapshot));
    const page = snapshot.pages.find((entry) => entry.id === "basic");
    if (page) page.truncated = true;
    snapshot.truncated = true;
    return snapshot;
  };
  await Promise.all(Object.entries(outputs).map(([filename, config]) =>
    writeFile(
      new URL(`./preview/${filename}`, import.meta.url),
      renderReport(sampleReport(".", config.status, config), {
        tab: config.tab,
        reportUrl: `https://sq.example.com/r/${config.id || "AbCdEfGhIjKl"}`,
        snapshot: snapshotFor(config),
        promotion: {
          text: "SpeedQuality 社区节点计划",
          projectUrl: "https://github.com/rtw1248/speedquality",
        },
        usage: { today: 128, total: 12680 },
        tabLinks: {
          ...(!["verified", "verified_stale", "verified_time_unknown"].includes(config.status)
            ? standaloneTabs
            : config.status === "verified_stale"
              ? staleTabs
              : config.status === "verified_time_unknown" ? timeUnknownTabs : linkedTabs),
          ...(filename.startsWith("validation-") &&
              ["verified", "verified_stale", "verified_time_unknown"].includes(config.status)
            ? { sq: `./${filename}` }
            : {}),
          ...(config.snapshotMode === "none"
            ? {
                nodequality: "./report-nq-snapshot-unavailable.html",
                sq: "./report-speed.html",
              }
            : {}),
          ...(config.snapshotMode === "truncated"
            ? { "nq-basic": "./report-nq-page-truncated.html" }
            : {}),
        },
      }),
      "utf8",
    )
  ));
  const catalog = [
    {
      title: "测速结果状态",
      items: [
        ["result-success-ipv4.html", "单省 IPv4 全部成功"],
        ["result-dual-stack.html", "单省 IPv4 + IPv6 双栈"],
        ["report-standalone.html", "多省独立报告"],
        ["result-node-unavailable.html", "部分运营商没有候选节点"],
        ["result-transfer-failed.html", "节点存在但测速传输失败"],
        ["result-color-thresholds.html", "延迟与速度的绿、橙、红阈值"],
        ["result-speed-100.html", "100 Mbps 档位"],
        ["result-speed-400.html", "400 Mbps 档位"],
        ["result-no-traffic.html", "无法取得流量计数时的报告"],
      ],
    },
    {
      title: "NodeQuality 联合报告分页",
      items: [
        ["report.html", "全部"],
        ["report-basic.html", "基本信息"],
        ["report-ip-quality.html", "IP 质量"],
        ["report-network-quality.html", "网络质量"],
        ["report-route.html", "回程路由"],
        ["report-speed.html", "速度质量"],
        ["report-nq-snapshot-unavailable.html", "NQ 快照无法读取"],
        ["report-nq-page-truncated.html", "NQ 页面因安全大小限制被截断"],
      ],
    },
    {
      title: "NodeQuality 校验结果",
      items: [
        ["validation-full-ip-ok.html", "完整 IP 一致，时间正常"],
        ["validation-masked-ip-ok.html", "脱敏 IP 网段与 ASN 一致，时间正常"],
        ["validation-time-stale.html", "服务器一致，时间超过 60 分钟"],
        ["validation-time-unknown.html", "服务器一致，时间无法确认"],
        ["validation-identity-mismatch.html", "服务器身份不匹配，拒绝绑定"],
        ["validation-report-unavailable.html", "NQ 报告无法读取或解析"],
      ],
    },
  ];
  const catalogHtml = catalog.map((group) => `
    <section>
      <h2>${group.title}</h2>
      <ul>${group.items.map(([href, label]) =>
        `<li><a href="./${href}">${label}</a><code>${href}</code></li>`).join("")}</ul>
    </section>`).join("");
  await writeFile(new URL("./preview/index.html", import.meta.url), `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SpeedQuality 报告状态预览</title>
<style>
  :root{color-scheme:dark}*{box-sizing:border-box}body{max-width:960px;margin:0 auto;padding:32px 20px 56px;background:#050508;color:#f6f5fb;font:15px/1.6 system-ui,sans-serif;letter-spacing:0}h1{margin:0 0 6px;font-size:28px}p{margin:0 0 28px;color:#9aa7a7}section{padding:18px 0;border-top:1px solid #ffffff24}h2{margin:0 0 10px;font-size:18px}ul{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:8px 18px;margin:0;padding:0;list-style:none}li{display:flex;min-width:0;flex-direction:column;padding:9px 11px;background:#ffffff0d}a{color:#37ff8b;text-underline-offset:3px}code{overflow:hidden;color:#8f98a4;font-size:12px;text-overflow:ellipsis;white-space:nowrap}
</style></head><body><h1>SpeedQuality 报告状态预览</h1>
<p>这些页面使用同一份生产报告模板，只替换了示例测试结果和校验状态。</p>
${catalogHtml}</body></html>`, "utf8");
  console.log("Static previews written to deploy/cloudflare-worker/preview/");
} else {
  server.listen(port, host, () => {
    console.log(`SpeedQuality preview: http://${host}:${port}/r/AbCdEfGhIjKl`);
  });
}
