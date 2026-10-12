import { nodeMapGeometry } from "./node-map-geometry.js";
import { nodeMapStyle } from "./node-map-style.js";
import { nodeMapClient } from "./node-map-client.js";

export function renderNodeMap(scriptNonce) {
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="dark">
  <title>SpeedQuality · 节点地图</title>
  <style>${nodeMapStyle}</style>
</head>
<body class="map-loading">
  <div class="demo-banner map-data-status" role="status"><span id="data-status">正在加载节点覆盖数据…</span><button class="retry" id="retry" hidden>重新加载</button></div>
  <header class="site-header wrap">
    <a class="brand" href="#" aria-label="SpeedQuality节点地图"><span class="brand-symbol">SQ<span></span></span><span>SpeedQuality</span></a>
    <nav aria-label="主导航"><a class="nav-current" href="#">节点地图</a><a href="/run" target="_blank" rel="noopener">使用指南<span class="external">↗</span></a><a href="https://github.com/rtw1248/speedquality" target="_blank" rel="noopener">GitHub<span class="external">↗</span></a></nav>
    <button class="button contribute header-contribute">贡献节点<span>↗</span></button>
  </header>
  <main class="wrap">
    <section class="intro">
      <div><p class="eyebrow"><span class="line"></span> SPEEDQUALITY NETWORK</p><h1>一起点亮更多地区<span class="heading-dot">.</span></h1><p class="intro-copy">看看哪里已经有节点，哪里还缺一个你。</p></div>
      <div class="intro-aside"><span class="tiny-orbit">◎</span><div>每一个新节点<br><strong>都让测速多一个选择。</strong></div></div>
    </section>
    <section class="summary" aria-label="节点覆盖统计">
      <div class="summary-main"><span class="stat-label">已点亮地区</span><div class="stat-value"><strong id="covered-count">—</strong><span>/ 31</span><span class="stat-unit">省级地区</span></div></div>
      <div><span class="stat-label" id="complete-label">三网覆盖齐全</span><div class="stat-value"><strong id="complete-count">—</strong><span class="stat-unit">个地区</span></div></div>
      <div><span class="stat-label">近期测速成功端点</span><div class="stat-value"><strong id="node-count">—</strong><span class="stat-unit">个</span></div></div>
      <div class="summary-end"><span class="stat-label">还待点亮</span><div class="stat-value"><strong id="missing-count">—</strong><span class="stat-unit">个地区</span></div><div class="coverage-track"><span id="coverage-progress"></span></div></div>
    </section>
    <section class="explorer" aria-label="节点覆盖地图">
      <div class="toolbar">
        <div class="carrier-filters" role="group" aria-label="运营商筛选"><button class="filter active" data-carrier="all" aria-pressed="true">三网概览</button><button class="filter" data-carrier="ct" aria-pressed="false">电信</button><button class="filter" data-carrier="cu" aria-pressed="false">联通</button><button class="filter" data-carrier="cm" aria-pressed="false">移动</button><button class="filter education" data-carrier="edu" aria-pressed="false">教育网<small>建设中</small></button></div>
        <div class="extra-filters"><label class="source-select"><span class="sr-only">节点来源</span><select id="source-filter"><option value="all">全部来源</option><option value="community">社区贡献</option><option value="third_party">第三方节点</option></select></label><div class="family-filters" role="group" aria-label="IP协议"><button class="active" data-family="v4" aria-pressed="true">IPv4</button><button data-family="v6" aria-pressed="false">IPv6</button></div></div>
      </div>
      <p class="education-notice" id="education-notice" hidden>教育网建设中，暂未开放公共测速。支持自建教育网节点，通过Route Key定向测试；公开节点可参与点亮。</p>
      <div class="explorer-body">
        <div class="map-area">
          <div class="map-meta"><div><span class="live-dot"></span><span id="scope-label">公共节点 · IPv4 · 三网概览</span></div><span>点击省份查看详情</span><label class="region-picker"><span class="sr-only">选择地区</span><select id="region-picker" aria-label="选择地区"></select></label></div>
          <div class="map-canvas">${nodeMapGeometry}<div id="map-tooltip" class="map-tooltip" role="tooltip" hidden></div></div>
          <div class="map-legend"><span><i class="legend-dot complete"></i><span id="complete-legend">三网齐全</span></span><span id="partial-legend"><i class="legend-dot partial"></i>部分覆盖</span><span><i class="legend-dot pending"></i>待核验</span><span><i class="legend-dot empty"></i>待点亮</span></div>
          <div class="map-footnote">按所选网络统计近7天测速成功、归属有效的公开端点。同一地址的别名不重复计数；暂时繁忙不熄灭，私有节点不展示。</div>
        </div>
        <aside class="region-panel" aria-label="所选地区详情" aria-live="polite">
          <div class="region-kicker"><span>REGION DETAILS</span></div>
          <div class="region-title-row"><h2 id="region-name">湖北</h2><span class="coverage-badge" id="region-badge">加载中</span></div>
          <div class="region-subtitle" id="region-subtitle">等待覆盖数据</div>
          <div id="carrier-list" class="carrier-list"></div>
          <div class="region-bottom"><p id="region-gap">新增节点通过验证并完成测速后，可计入覆盖。</p><button class="button primary contribute region-contribute">我来补充节点<span>↗</span></button><button disabled id="copy-command" class="copy-command">复制湖北测速命令<span>⧉</span></button><p id="copy-status" class="copy-status" role="status"></p></div>
        </aside>
      </div>
    </section>
    <section class="contribution-banner">
      <div class="contribution-symbol" aria-hidden="true"><span></span><span></span><span></span><span></span><span></span><span></span><span></span><span></span><span></span></div>
      <div class="contribution-copy"><h2>从你所在的地方，点亮下一块地图。</h2><p>有可分享的宽带或服务器？完成节点接入与验证，就能为当地增加一个测速选择。</p></div><button class="button contribute">了解如何贡献<span>→</span></button>
    </section>
    <footer><div><span class="footer-brand">SQ</span> 每一次测速，都有参照。</div><p>覆盖来自平台近期观察，未收录或尚未测速的地区不代表没有节点。地图边界：<a href="https://datav.aliyun.com/portal/school/atlas/area_selector" target="_blank" rel="noopener">DataV.GeoAtlas</a></p><span>每5分钟缓存更新</span></footer>
  </main>
  <dialog id="contribute-dialog">
    <button class="dialog-close" aria-label="关闭">×</button><span class="dialog-icon">✦</span><p class="eyebrow">LIGHT UP YOUR REGION</p><h2 id="dialog-title">为湖北补充一个节点</h2><p id="dialog-description">把你愿意公开共享的节点接入SQ，为所在地区增加一个测速选择。</p><div class="contribution-steps"><div><span>01</span><strong>准备节点</strong><p>确认所在地区、运营商和可分享的带宽。</p></div><div><span>02</span><strong>部署与验证</strong><p>安装sq-node，完成地址、网络归属与可用性验证。</p></div><div><span>03</span><strong>公开共享</strong><p>选择public公开展示，通过网络归属验证并完成测速后计入覆盖。</p></div></div><p class="dialog-note">私有节点不展示。教育网节点的public模式仅公开地图覆盖，测速仍需Route Key。请仅部署你有权管理和共享的网络。</p><a class="button primary" href="https://github.com/rtw1248/speedquality/blob/main/docs/sq-node.md" target="_blank" rel="noopener">查看节点部署文档<span>↗</span></a>
  </dialog>
  <noscript><p>请启用JavaScript查看覆盖数据；也可前往<a href="/run">使用指南</a>。</p></noscript>
  <script nonce="${scriptNonce}">(${nodeMapClient.toString()})();</script>
</body>
</html>
`;
}
