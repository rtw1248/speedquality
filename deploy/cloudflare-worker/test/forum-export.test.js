import assert from "node:assert/strict";
import test from "node:test";
import { formatReportCopies, renderReport } from "../src/index.js";

const strip = text => text.replace(/\x1b\[[0-9;]*m/g, "");
const width = text => [...text].reduce((sum, char) => sum + (/^[\x00-\x7f·✓]$/.test(char) ? 1 : 2), 0);
function fixture(city = "咸宁", family = "v4") {
  const results = ["ct", "cu", "cm"].map((carrier, index) => ({
    carrier, label: "湖北" + ["电信", "联通", "移动"][index], status: "ok", latency_ms: 144,
    node_id: String(index + 1).repeat(32), node_used: true,
    node_info: { city, city_status: "verified", source: "third_party", name: `${city}${["电信", "联通", "移动"][index]}1号` },
    single: { upload_mbps: 167.40, download_mbps: 114.29,
      upload_retransmission: { sent_bytes: 10000, retransmitted_bytes: 311 } },
  }));
  return {
    id: "ForumExample", tested_at: 1792098158, source_ip_masked: "203.0.*.*", version: "1.6.1",
    target_mbps: 200, traffic_rx_bytes: 253540000, traffic_tx_bytes: 368410000,
    speed_data: JSON.stringify([{ version: 1, region: { code: "hb", name: "湖北" }, family,
      target_mbps: 200, results }]),
    nq_url: "https://nodequality.com/r/VerifiedReport", bind_status: "verified_stale",
    nq_identity_reason: "linked_sq", time_gap_seconds: (106 * 24 + 9) * 3600,
  };
}
const ansiBlocks = markdown => [...markdown.matchAll(/```ansi\n([\s\S]*?)\n```/g)].map(match => match[1]);

test("forum rows and long verification fit 80 columns without losing metrics, names or colors", () => {
  for (const family of ["v4", "v6"]) for (const city of ["咸宁", "乌鲁木齐"]) {
    const report = fixture(city, family);
    const copies = formatReportCopies(report, { reportUrl: "https://sq.example/r/ForumExample" });
    const ansi = ansiBlocks(copies.nodeseek)[0];
    const plain = strip(ansi);
    for (const line of plain.split("\n")) assert.ok(width(line) <= 80, `${width(line)}: ${line}`);
    assert.match(plain, new RegExp(`电信 +144ms +3\\.11% +167\\.40Mbps +114\\.29Mbps +${city}电信1号（IPv${family.slice(1)}） · 第三方`));
    assert.match(ansi, /\x1b\[38;2;255;165;0m +3\.11%\x1b\[0m/);
    const notice = plain.slice(plain.indexOf("关联校验："), plain.indexOf("\n\n湖北"));
    assert.equal(notice.split("\n").length, 3);
    assert.ok(notice.replace(/\s/g, "").includes("NodeQuality已与新测速校验身份，此历史SQ的出口身份与新测速匹配；两次检测相差106天9小时，超过60分钟。"));
    assert.match(copies.text, /114\.29Mbps {6}/);
    assert.match(copies.markdown, /114\.29Mbps {6}/);
    const page = renderReport(report, { tab: "sq" });
    assert.match(page, /114\.29Mbps<\/span> {6}/);
  }
});

test("oversized names get exact references instead of truncation or broken rows", () => {
  const city = "巴音郭楞蒙古自治州";
  const copies = formatReportCopies(fixture(city));
  const plain = strip(ansiBlocks(copies.nodeseek)[0]);
  for (const line of plain.split("\n")) assert.ok(width(line) <= 80, line);
  for (const [index, carrier] of ["电信", "联通", "移动"].entries()) {
    assert.match(plain, new RegExp(`${carrier} +144ms[^\\n]+\\[${index + 1}\\]`));
    assert.ok(plain.includes(`[${index + 1}] ${city}${carrier}1号（IPv4） · 第三方`));
  }
});

test("forum adaptation preserves NQ content and legacy SQ rows", () => {
  const report = fixture();
  report.speed_data = "";
  report.speed_text = "\x1b[32m湖北电信 167.40Mbps\x1b[0m";
  const content = "\x1b[36mNodeQuality 原始内容\x1b[0m\n  固定空白和布局";
  const copies = formatReportCopies(report, { snapshot: { pages: [
    { id: "basic", title: "基本信息", format: "ansi", content },
  ] } });
  assert.ok(copies.nodeseek.includes(content));
  assert.ok(copies.nodeseek.includes(report.speed_text));
  assert.deepEqual([...copies.nodeseek.matchAll(/^::: tab-item (.+)$/gm)].map(match => match[1]), ["基本信息", "速度质量"]);
});
