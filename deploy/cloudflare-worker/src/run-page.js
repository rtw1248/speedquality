// An explicit HTML preference selects the guide. curl/wget and Accept: */*
// continue to receive the script, regardless of their User-Agent.
export function prefersRunPage(request) {
  let html = 0;
  let plain = 0;
  for (const part of (request.headers.get("accept") || "").split(",")) {
    const [type, ...parameters] = part.trim().toLowerCase().split(";");
    const weight = parameters.map((value) => value.trim()).find((value) => value.startsWith("q="));
    const quality = weight === undefined ? 1 : Number(weight.slice(2));
    if (!Number.isFinite(quality) || quality <= 0 || quality > 1) continue;
    if (type.trim() === "text/html") html = Math.max(html, quality);
    if (type.trim() === "text/plain") plain = Math.max(plain, quality);
  }
  return html > 0 && html >= plain;
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[character]);
}

export function renderRunPage({ origin, projectUrl, nqEnabled, scriptNonce }) {
  const command = `bash <(curl -fsSL ${origin}/run)`;
  const params = [
    ["-p hb,bj", "选择湖北、北京；最多5个省份，也支持中文。bsg表示北京、上海、广东。"],
    ["-s 100", "测速档位：100、200、400 Mbps，默认200。档位也是本次测速的限速上限。"],
    ["-v4 / -v6", "仅测IPv4或IPv6；默认测试服务器可用的两种协议。"],
    ["--sq 报告链接", "关联历史SQ，含本次最多2份。联合链接会在终端列出时间、省份、档位和IP类型，选择保留哪份后只需再测一次。"],
    ["--sq-keep 1 / 2", "自动运行时按终端选项编号指定保留哪份SQ。普通终端运行可直接按提示选择。"],
    ...(nqEnabled ? [["--nq 报告链接", "关联NodeQuality报告；若历史SQ也带有NQ，逐份校验身份，选择通过校验且距本次测速时间最近的一份。"]] : []),
    ["-l", "查看省份代码。无法自动识别SSH来源省份时，用-p手动指定。"],
    ["-i", "打开手动选择菜单。"],
    ["--help", "查看完整帮助。"],
  ];
  return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>SpeedQuality · 使用指南</title>
<meta name="description" content="在VPS终端运行SpeedQuality，查看各省三网的单线程速度，生成分享报告。">
<style>
:root{color-scheme:dark;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;background:#11151c;color:#e8edf5;line-height:1.65}
*{box-sizing:border-box}body{margin:0}main{max-width:880px;margin:0 auto;padding:64px 28px 32px}h1{font-size:clamp(30px,5vw,42px);line-height:1.2;letter-spacing:-.04em;margin:0 0 14px;font-weight:700}h2{font-size:20px;margin:0 0 12px;font-weight:600}p{margin:10px 0}.subtitle{color:#adb9ca;font-size:17px}.intro{margin-bottom:32px}.start{background:#1a202b;border:1px solid #323b4a;border-radius:14px;padding:24px}.lead{margin-top:0;color:#c5cfdd}.command{background:#10151d;border:1px solid #354153;border-radius:8px;padding:18px;margin:20px 0 16px;white-space:pre-wrap;overflow-wrap:anywhere;user-select:all;font-size:15px;line-height:1.7;color:#d7f0ff}code{font-family:ui-monospace,SFMono-Regular,Consolas,"Liberation Mono",monospace}button{font:inherit;font-weight:600;border:1px solid transparent;border-radius:8px;padding:10px 20px;color:#10202c;background:#9edaff;cursor:pointer;min-height:44px}button:hover{background:#c0e8ff}button:focus-visible,a:focus-visible{outline:3px solid #9edaff;outline-offset:4px}.actions{display:flex;align-items:center;gap:14px;flex-wrap:wrap}.status{font-size:14px;color:#bdccde}.note{color:#99a9bd;font-size:14px;margin-top:18px}.parameters{margin-top:36px}dl{margin:16px 0 0}dl>div{display:grid;grid-template-columns:180px 1fr;gap:16px;padding:14px 0;border-top:1px solid #2c3543}dt{color:#b9e2ff}dd{margin:0;color:#c0ccdc}.example{margin-top:28px}.example pre{background:#1a202b;border-radius:8px;padding:16px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px}footer{display:flex;flex-wrap:wrap;gap:12px 24px;border-top:1px solid #2c3543;margin-top:36px;padding-top:20px;font-size:14px}a{color:#b9e2ff;text-underline-offset:4px}a:hover{color:#e4f5ff}noscript p{color:#bdccde}
@media(max-width:560px){main{padding:32px 18px 24px}.start{padding:20px 16px}.command{padding:14px;font-size:14px}dl>div{grid-template-columns:1fr;gap:4px;padding:12px 0}.subtitle{font-size:16px}.actions button{width:100%}.status{min-height:23px}}
</style>
</head>
<body>
<main>
  <header class="intro">
    <h1>SpeedQuality</h1>
    <p class="subtitle">分省三网 · 单线程测速 · 可分享报告</p>
  </header>
  <section class="start" aria-labelledby="start-title">
    <h2 id="start-title">请在VPS终端执行</h2>
    <p class="lead">通过SSH登录你的VPS，复制下面整条命令到终端运行。</p>
    <pre class="command"><code id="run-command">${escapeHTML(command)}</code></pre>
    <div class="actions">
      <button id="copy-command" type="button">复制命令</button>
      <span class="status" id="copy-status" role="status" aria-live="polite"></span>
    </div>
    <noscript><p>选中上方命令即可手动复制。</p></noscript>
    <p class="note">支持Linux amd64、arm64。测速结果会显示在终端，完成后生成网页报告链接。</p>
  </section>
  <p class="note">默认按SSH连接来源识别省份，以200 Mbps档位测试可用的IPv4和IPv6。测速会消耗流量，开始前会给出估算。</p>
  <section class="parameters" aria-labelledby="params-title">
    <h2 id="params-title">常用参数</h2>
    <p class="lead">把需要的参数加在命令末尾即可。</p>
    <dl>${params.map(([flag, text]) => `<div><dt><code>${escapeHTML(flag)}</code></dt><dd>${escapeHTML(text)}</dd></div>`).join("")}</dl>
  </section>
  <section class="example" aria-labelledby="example-title">
    <h2 id="example-title">例如：湖北，100 Mbps，仅IPv4</h2>
    <pre><code>${escapeHTML(command)} -p hb -s 100 -v4</code></pre>
  </section>
  <footer>
    <a href="/nodes">节点地图 · 查看覆盖与贡献节点</a>
    ${projectUrl ? `<a href="${escapeHTML(projectUrl)}" target="_blank" rel="noopener noreferrer">GitHub项目链接</a>` : ""}
    <a href="/run.sh">查看脚本源码</a>
  </footer>
</main>
<script nonce="${escapeHTML(scriptNonce)}">
const button = document.getElementById('copy-command');
const command = document.getElementById('run-command');
const status = document.getElementById('copy-status');
button.addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(command.textContent);
    status.textContent = '已复制，请粘贴到VPS终端执行';
  } catch {
    const range = document.createRange();
    range.selectNodeContents(command);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    status.textContent = '请长按或选中上方命令，手动复制';
  }
});
</script>
</body>
</html>`;
}
