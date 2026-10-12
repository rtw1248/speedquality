// Keep browser code as literal source: bundling a function before toString()
// can add Worker-only helpers that are unavailable in the browser.
export const nodeMapClient = `(() => {
  'use strict';
  const $ = selector => document.querySelector(selector);
  const $$ = selector => [...document.querySelectorAll(selector)];
  const carrierNames = {ct:'电信',cu:'联通',cm:'移动',edu:'教育网'};
  const regions = $$('.province').map(path => ({code:path.dataset.code,
    name:path.querySelector('title').textContent, supported:!['hk','mo','tw'].includes(path.dataset.code), nodes:[]}));
  const byCode = new Map(regions.map(region => [region.code, region]));
  const selected = {region:'hb',carrier:'all',family:'v4',source:'all'};
  let loaded = false;
  const tooltip = $('#map-tooltip');
  function element(tag, className, content) {
    const item = document.createElement(tag);
    item.className = className;
    if (content !== undefined) item.textContent = content;
    return item;
  }
  for (const region of regions) {
    const option = element('option','',region.name);
    option.value = region.code;
    $('#region-picker').append(option);
  }
  const familyName = () => selected.family === 'v4' ? 'IPv4' : 'IPv6';
  const filterCarriers = () => selected.carrier === 'all' ? ['ct','cu','cm'] : [selected.carrier];
  const visibleNodes = (region, carrier) => region.nodes.filter(node => node.carrier === carrier &&
    node.family === selected.family && (selected.source === 'all' || node.source === selected.source));
  const successfulNodes = (region, carrier) => visibleNodes(region, carrier).filter(node => node.status === 'verified');
  function coverage(region) {
    if (!loaded) return {state:'loading',covered:0,count:0};
    if (!region.supported) return {state:'unsupported',covered:0,count:0};
    const carriers = filterCarriers();
    const counts = carriers.map(carrier => successfulNodes(region,carrier).length);
    const covered = counts.filter(Boolean).length;
    const pending = carriers.some(carrier => visibleNodes(region,carrier).some(node => node.status === 'pending'));
    return {state:covered === carriers.length ? 'complete' : covered ? 'partial' : pending ? 'pending' : 'empty',
      covered,count:counts.reduce((a,b)=>a+b,0)};
  }
  function stateLabel(state) {
    return {complete:selected.carrier === 'all'?'三网齐全':'已覆盖',partial:'部分覆盖',pending:'已收录',
      empty:'暂无记录',unsupported:'暂未开放',loading:'等待数据'}[state];
  }
  function cityLabel(node) {
    if (node.city_status === 'conflict') return '城市待核实';
    return node.city || '';
  }
  function nodeStatusLabel(node) {
    if (node.status === 'verified') return '已计入覆盖';
    return {location_mismatch:'地区或运营商归属冲突',verification_pending:'地区和运营商待核验',
      verification_expired:'归属核验已过期',node_unavailable:'节点当前不可用',no_success:'尚无成功测速',
      recent_failure:'最近一次测速未成功',success_expired:'近7天暂无成功测速'}[node.pending_reason] || '尚未满足点亮条件';
  }
  function dateLabel(value) {
    return value ? new Date(value * 1000).toLocaleString('zh-CN',{hour12:false}) : '尚无成功记录';
  }
  function update() {
    tooltip.hidden = true;
    let coveredCount = 0, complete = 0, count = 0;
    for (const region of regions) {
      const info = coverage(region);
      coveredCount += Number(info.covered > 0);
      complete += Number(info.state === 'complete');
      count += info.count;
      const path = document.getElementById(\`province-\${region.code}\`);
      const active = region.code === selected.region;
      path.setAttribute('class',\`province state-\${info.state}\${active?' selected':''}\`);
      path.setAttribute('aria-label',\`\${region.name}，\${stateLabel(info.state)}\${loaded?\`，\${info.count}个端点\`:''}\`);
      path.setAttribute('aria-pressed',String(active));
      document.getElementById(\`label-\${region.code}\`)?.setAttribute('class',\`province-label state-\${info.state}\${active?' selected':''}\`);
    }
    for (const [id,value] of [['covered-count',coveredCount],['complete-count',complete],['node-count',count],['missing-count',31-coveredCount]]) {
      $(\`#\${id}\`).textContent = loaded ? value : '—';
    }
    $('#coverage-progress').style.width = \`\${loaded?coveredCount/31*100:0}%\`;
    $('#complete-label').textContent = selected.carrier==='all'?'三网覆盖齐全':\`\${carrierNames[selected.carrier]}已覆盖\`;
    $('#complete-legend').textContent = selected.carrier==='all'?'三网齐全':'已覆盖';
    $('#partial-legend').style.display = selected.carrier==='all'?'flex':'none';
    $('#education-notice').hidden = selected.carrier !== 'edu';
    const sourceNames = {all:'公开端点',community:'社区贡献',third_party:'第三方节点'};
    $('#scope-label').textContent = \`\${sourceNames[selected.source]} · \${familyName()} · \${selected.carrier==='all'?'三网概览':carrierNames[selected.carrier]}\`;
    const region = byCode.get(selected.region), info = coverage(region);
    const activePath = document.getElementById(\`province-\${region.code}\`);
    activePath.parentNode.appendChild(activePath);
    $('#region-picker').value = region.code;
    $('#region-name').textContent = region.name;
    $('#region-badge').textContent = stateLabel(info.state);
    $('#region-subtitle').textContent = !loaded ? '等待覆盖数据' : !region.supported ? '暂未纳入公共分省测速范围' :
      \`\${familyName()} · \${selected.carrier==='all'?\`三网覆盖\${info.covered}/3\`:\`\${carrierNames[selected.carrier]} · \${info.count}个已验证端点\`}\`;
    const list = $('#carrier-list');
    list.replaceChildren();
    if (loaded) for (const carrier of selected.carrier==='all'?['ct','cu','cm','edu']:[selected.carrier]) {
      const nodes = visibleNodes(region,carrier), verified = nodes.filter(node => node.status === 'verified');
      const state = verified.length?'verified':nodes.length?'pending':'empty';
      const row = element('div','carrier-row'), heading = element('div','carrier-heading'), title = element('div','carrier-name');
      title.append(element('i',\`carrier-dot \${state}\`),document.createTextNode(carrierNames[carrier]));
      if (carrier === 'edu') title.append(element('span','small-tag','建设中'));
      heading.append(title,element('span',\`carrier-amount \${state}\`,verified.length?\`\${verified.length}个端点\`:nodes.length?'暂未点亮':'暂无记录'));
      row.append(heading);
      if (nodes.length) {
        const details = element('details','node-list');
        details.append(element('summary','',\`查看\${nodes.length}个已收录端点\`));
        for (const node of nodes) {
          const identity = element('div','node-identifiers');
          const city = cityLabel(node);
          if (city) identity.append(element('span','node-city',city));
          const code = element('code','',node.id.slice(0,12));
          code.title = \`完整编号：\${node.id}\`;
          identity.append(code,element('small','',\`\${node.source==='community'?'自建':'第三方'} · \${nodeStatusLabel(node)} · 最近成功：\${dateLabel(node.last_success_at)}\`));
          details.append(identity);
        }
        row.append(details);
      } else row.append(element('div','carrier-description',carrier==='edu'?'等待教育网节点加入':'暂无近期验证的公开节点'));
      list.append(row);
    }
    const missing = filterCarriers().filter(carrier=>!successfulNodes(region,carrier).length);
    $('#region-gap').textContent = !loaded ? '新增节点通过验证并完成测速后，可计入覆盖。' : selected.carrier==='edu' ?
      '教育网建设中，暂未开放公共测速。' : !region.supported ? '可查看自建节点部署说明。' : missing.length ?
      missing.some(carrier=>visibleNodes(region,carrier).length) ? '已收录节点尚未满足点亮条件，展开列表可查看原因。' :
      \`\${region.name}\${missing.map(c=>carrierNames[c]).join('、')}，等你点亮。\` : '已经点亮，也欢迎增加更多节点。';
    const copy = $('#copy-command');
    copy.disabled = !loaded || selected.carrier==='edu' || !region.supported;
    copy.replaceChildren(document.createTextNode(selected.carrier==='edu'?'教育网暂未开放公共测速':\`复制\${region.name}三网测速命令\`),element('span','','⧉'));
    $('#copy-status').textContent = '';
  }
  async function loadCoverage() {
    $('#retry').hidden = true;
    $('#data-status').textContent = '正在加载节点覆盖数据…';
    try {
      const response = await fetch('/api/nodes/coverage',{headers:{Accept:'application/json'},signal:AbortSignal.timeout(15000)});
      if (!response.ok) throw new Error('unavailable');
      const data = await response.json();
      if (data.version !== 1 || !Array.isArray(data.nodes) || !Number.isFinite(data.generated_at)) throw new Error('invalid');
      for (const region of regions) region.nodes = [];
      for (const node of data.nodes) byCode.get(node.region)?.nodes.push(node);
      loaded = true;
      document.body.classList.remove('map-loading');
      $('#data-status').textContent = \`最近更新：\${dateLabel(data.generated_at)} · 第三方与公开自建节点均计入 · 更新最多延迟5分钟\`;
      update();
    } catch {
      $('#data-status').textContent = '覆盖数据暂时无法加载，请稍后重试。';
      $('#retry').hidden = false;
    }
  }
  $('#retry').addEventListener('click',loadCoverage);
  $('#region-picker').addEventListener('change',event=>{selected.region=event.target.value;update();});
  for (const kind of ['carrier','family']) $$(\`[data-\${kind}]\`).forEach(button=>button.addEventListener('click',()=>{
    selected[kind]=button.dataset[kind];
    $$(\`[data-\${kind}]\`).forEach(item=>{item.classList.toggle('active',item===button);item.setAttribute('aria-pressed',String(item===button));});
    update();
  }));
  $('#source-filter').addEventListener('change',event=>{selected.source=event.target.value;update();});
  $$('.province').forEach(path=>{
    const region=byCode.get(path.dataset.code);
    const select=()=>{selected.region=region.code;update();};
    path.addEventListener('click',select);
    path.addEventListener('keydown',event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();select();}});
    path.addEventListener('pointermove',event=>{
      if (event.pointerType==='touch') return;
      const bounds=$('.map-canvas').getBoundingClientRect(), info=coverage(region);
      tooltip.textContent=\`\${region.name} · \${stateLabel(info.state)}\${loaded?\` · \${info.count}个端点\`:''}\`;
      tooltip.hidden=false;
      tooltip.style.left=\`\${Math.max(110,Math.min(bounds.width-110,event.clientX-bounds.left))}px\`;
      tooltip.style.top=\`\${Math.max(42,event.clientY-bounds.top-5)}px\`;
    });
    path.addEventListener('pointerleave',()=>{tooltip.hidden=true;});
  });
  $('#copy-command').addEventListener('click',async()=>{
    const command=\`bash <(curl -fsSL \${location.origin}/run) -p \${selected.region} -\${selected.family}\`;
    try { await navigator.clipboard.writeText(command); $('#copy-status').textContent='已复制，请在VPS终端执行。'; }
    catch { $('#copy-status').textContent=command; $('#copy-status').style.userSelect='all'; }
  });
  const dialog=$('#contribute-dialog');
  $$('.contribute').forEach(button=>button.addEventListener('click',()=>{
    const region=byCode.get(selected.region);
    $('#dialog-title').textContent=selected.carrier==='edu'?\`为\${region.name}贡献教育网节点\`:\`为\${region.name}补充一个节点\`;
    $('#dialog-description').textContent=selected.carrier==='edu'?'安装sq-node并验证教育网归属，通过Route Key完成定向测速；选择public即可公开地图覆盖。教育网公共测速暂未开放。':'把你愿意公开共享的节点接入SQ，为所在地区增加一个测速选择。';
    dialog.showModal();
  }));
  $('.dialog-close').addEventListener('click',()=>dialog.close());
  dialog.addEventListener('click',event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)dialog.close();}});
  update();
  loadCoverage();
})();`;
