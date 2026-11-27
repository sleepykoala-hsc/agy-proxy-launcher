/* G PROXY dashboard — polls /api/state once a second and renders it. */

const el = (id) => document.getElementById(id);
const POLL_MS = 1000;

let lastState = null;
let timer = null;

/* ---------- formatting ---------- */

function fmtBytes(n) {
  if (!n || n < 1) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? Math.round(v).toString() : v.toFixed(2)) + ' ' + units[i];
}

function fmtRate(n) {
  if (!n || n < 1) return '0 B/s';
  return fmtBytes(n) + '/s';
}

function fmtSeconds(ms) {
  if (!ms || ms < 1) return '–';
  return (ms / 1000).toFixed(2) + 's';
}

/* ---------- render ---------- */

function render(st) {
  const chain = st.chain || {};
  const app = st.app || {};
  const traffic = st.traffic || {};
  const latency = st.latency || {};

  // status card
  const card = el('statusCard');
  card.classList.remove('warn', 'bad');
  if (st.direct) card.classList.add('warn');
  else if (!chain.ok) card.classList.add('bad');
  el('statusDot').style.background = '';

  el('statusTitle').textContent = chain.title || '—';
  el('statusDetail').textContent = chain.detail || '';

  const tick = el('statusTick');
  tick.classList.remove('action');
  delete tick.dataset.action;
  if (!app.running) {
    tick.textContent = '▶';
    tick.classList.add('action');
    tick.dataset.action = 'restart';
    tick.title = '启动 Antigravity';
  } else {
    tick.textContent = chain.ok ? '✓' : '!';
    tick.title = 'Antigravity 运行中';
  }

  // traffic
  el('downTotal').textContent = fmtBytes(traffic.downBytes);
  el('upTotal').textContent = fmtBytes(traffic.upBytes);
  el('downRate').textContent = fmtRate(traffic.downRate);
  el('upRate').textContent = fmtRate(traffic.upRate);

  const pill = document.querySelector('.pill');
  const monitorOn = app.running || (traffic.active || 0) > 0;
  pill.classList.toggle('off', !monitorOn);
  el('monitorPill').textContent = monitorOn ? '监控中' : '待启动';

  // latency + success rate
  el('avgLatency').textContent = fmtSeconds(latency.avgMs);
  const total = latency.total || 0;
  const ok = latency.ok || 0;
  el('successRate').textContent = total > 0 ? Math.round((ok / total) * 100) + '%' : '–';
  el('successCount').textContent = ok + '/' + total + ' 次';

  renderBars(latency);

  // exit node
  const exit = st.exit || {};
  el('exitIP').textContent = exit.masked || '—';
  el('exitGeo').textContent = exitGeo(exit);
  el('exitOrg').textContent = exit.ok
    ? (exit.org || '未知出口')
    : (exit.err || '等待出口探测');
  const exitTick = el('exitTick');
  exitTick.textContent = exit.ok ? '✓' : '!';
  exitTick.style.background = exit.ok ? 'var(--green)' : 'var(--red)';
  exitTick.style.color = exit.ok ? '#06140d' : '#210707';

  // google api
  renderGoogle(st.google || [], st.googleEvery);

  // upstream
  const up = st.upstreamInfo || {};
  const kindEl = el('upKind');
  kindEl.textContent = up.direct ? '直连' : String(up.kind || 'http').toUpperCase();
  kindEl.classList.toggle('direct', !!up.direct);
  el('upAddr').textContent = up.direct ? '未设置上游' : (up.addr || '—');
  el('upSource').textContent = up.source || '';

  // footer
  el('proxyAddr').textContent = st.proxyAddr || '';
  el('updatedAt').textContent = '更新 ' + (st.updatedAt || '—');

  // banner: one message at a time, most actionable first
  const candidates = (st.warnings || []).slice();
  if (!app.running && app.path) {
    candidates.push('Antigravity 未运行，点击状态卡右侧的 ▶ 启动');
  }
  const banner = el('banner');
  if (candidates.length) {
    banner.textContent = candidates[0];
    banner.title = candidates.join('\n');
    banner.classList.add('show');
  } else {
    banner.textContent = '';
    banner.classList.remove('show');
  }
}

function exitGeo(exit) {
  const parts = [];
  if (exit.country) parts.push(exit.country);
  if (exit.city) parts.push(exit.city);
  return parts.length ? '· ' + parts.join(' / ') : '';
}

function renderBars(latency) {
  const size = latency.maxSize || 24;
  const trend = latency.trend || [];
  const vals = trend.filter((p) => p.ok).map((p) => p.ms);
  const max = vals.length ? Math.max.apply(null, vals) : 1;
  const bars = el('bars');
  const frag = document.createDocumentFragment();

  for (let i = 0; i < size; i++) {
    const p = trend[trend.length - size + i];
    const bar = document.createElement('span');
    bar.className = 'bar';
    if (!p) {
      bar.style.height = '6px';
      bar.style.background = '#2f3437';
    } else if (!p.ok) {
      bar.style.height = '10px';
      bar.style.background = '#4a3436';
      bar.title = '探测失败';
    } else {
      const h = 14 + Math.round((p.ms / max) * 38);
      bar.style.height = h + 'px';
      bar.title = Math.round(p.ms) + ' ms';
    }
    frag.appendChild(bar);
  }
  bars.replaceChildren(frag);
}

function renderGoogle(rows, every) {
  el('googleHint').textContent = '每 ' + (every || 5) + ' 轮刷新';
  const list = el('googleList');
  const frag = document.createDocumentFragment();

  if (!rows.length) {
    const li = document.createElement('li');
    li.style.color = 'var(--faint)';
    li.textContent = '等待首轮探测…';
    frag.appendChild(li);
  }

  rows.forEach((r) => {
    const li = document.createElement('li');
    if (!r.ok) li.classList.add('bad');

    const check = document.createElement('span');
    check.className = 'g-check';
    check.textContent = r.ok ? '✓' : '!';

    const host = document.createElement('span');
    host.className = 'g-host';
    host.textContent = r.label || r.host;

    const code = document.createElement('span');
    code.className = 'g-code';
    code.textContent = r.ok ? String(r.code || '') : '×';

    const ms = document.createElement('span');
    ms.className = 'g-ms';
    ms.textContent = r.ms ? (r.ms / 1000).toFixed(2) + 's' : '—';

    li.append(check, host, code, ms);
    li.title = r.host + (r.err ? ' — ' + r.err : '');
    frag.appendChild(li);
  });
  list.replaceChildren(frag);
}

function showOffline() {
  el('statusCard').classList.remove('warn');
  el('statusCard').classList.add('bad');
  el('statusTitle').textContent = '网关已停止';
  el('statusDetail').textContent = '无法连接本地监控服务';
  el('statusDot').style.background = 'var(--red)';
  const banner = el('banner');
  banner.textContent = '与本地网关的连接已断开，请重新启动工具。';
  banner.classList.add('show');
}

/* ---------- actions ---------- */

async function act(action) {
  try {
    const r = await fetch('/api/action', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action }),
    });
    const data = await r.json().catch(() => ({}));
    if (!data.ok && data.error) {
      toast(data.error);
    }
  } catch (e) {
    toast('操作失败：' + e.message);
  }
}

function toast(msg) {
  const banner = el('banner');
  banner.textContent = msg;
  banner.classList.add('show');
}

async function quit() {
  const st = lastState || {};
  if (st.app && st.app.managed) {
    if (!confirm('退出网关会同时关闭 Antigravity（如有未保存内容会被询问），确定退出？')) return;
  }
  await act('quit');
  setTimeout(() => window.close(), 200);
}

el('statusTick').addEventListener('click', (ev) => {
  const action = ev.currentTarget.dataset.action;
  if (action) act(action);
});

el('closeBtn').addEventListener('click', quit);

/* ---------- upstream settings ---------- */

let upBusy = false;

function setPanelOpen(open) {
  el('upPanel').hidden = !open;
  el('upToggle').textContent = open ? '收起' : '更换';
}

function lockUpstream(busy) {
  upBusy = busy;
  ['upToggle', 'scanBtn', 'upApply', 'upDirect'].forEach((id) => {
    el(id).disabled = busy;
  });
}

async function scanUpstreams() {
  if (upBusy) return;
  const hint = el('scanHint');
  el('upError').textContent = '';
  el('scanList').replaceChildren();
  hint.textContent = '正在扫描本机代理…';
  try {
    const r = await fetch('/api/upstream/scan', { cache: 'no-store' });
    const data = await r.json();
    if (!data.ok) throw new Error(data.error || '扫描失败');
    renderScan(data.items || []);
  } catch (e) {
    hint.textContent = '扫描失败';
    el('upError').textContent = e.message;
  }
}

function renderScan(items) {
  const hint = el('scanHint');
  const st = lastState || {};
  const cur = st.upstreamInfo || {};
  const list = el('scanList');
  const frag = document.createDocumentFragment();

  if (!items.length) {
    hint.textContent = '没发现正在运行的本地代理，可在下方手动填写';
  } else {
    hint.textContent = '发现 ' + items.length + ' 个端口，点选即可切换';
  }

  items.forEach((it) => {
    const isCurrent = !cur.direct && cur.kind === it.kind && cur.addr === it.addr;

    const li = document.createElement('li');
    if (isCurrent) li.classList.add('current');
    li.title = it.verified ? '已验证可转发流量' : '端口开放，但握手未通过';

    const badge = document.createElement('span');
    badge.className = 'badge' + (it.verified ? '' : ' direct');
    badge.textContent = String(it.kind).toUpperCase();

    const addr = document.createElement('span');
    addr.className = 's-addr';
    addr.textContent = it.addr;

    const src = document.createElement('span');
    src.className = 's-src';
    src.textContent = it.source || '';

    const tail = document.createElement('span');
    if (isCurrent) {
      tail.className = 's-tick';
      tail.textContent = '✓';
    } else {
      tail.className = it.verified ? 's-ok' : 's-warn';
      tail.textContent = it.verified ? '可用' : '未通过';
    }

    li.append(badge, addr, src, tail);
    li.addEventListener('click', () => {
      if (upBusy || isCurrent) return;
      el('upManualKind').value = it.kind;
      el('upManualAddr').value = it.addr;
      applyUpstream(it.kind, it.addr);
    });
    frag.appendChild(li);
  });
  list.replaceChildren(frag);
}

async function applyUpstream(kind, addr) {
  if (upBusy) return;
  const hint = el('scanHint');
  const previous = hint.textContent;
  lockUpstream(true);
  el('upError').textContent = '';
  hint.textContent = '正在验证新链路…';
  try {
    const r = await fetch('/api/upstream', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind, addr }),
    });
    const data = await r.json().catch(() => ({}));
    if (!data.ok) throw new Error(data.error || '应用失败');
    await pollOnce();
    setPanelOpen(false);
  } catch (e) {
    el('upError').textContent = e.message;
    hint.textContent = previous;
  } finally {
    lockUpstream(false);
  }
}

el('upToggle').addEventListener('click', () => {
  const open = el('upPanel').hidden;
  setPanelOpen(open);
  if (!open) return;
  const cur = (lastState && lastState.upstreamInfo) || {};
  if (!cur.direct) {
    el('upManualKind').value = cur.kind || 'http';
    el('upManualAddr').value = cur.addr || '';
  }
  scanUpstreams();
});

el('scanBtn').addEventListener('click', scanUpstreams);
el('upApply').addEventListener('click', () => {
  applyUpstream(el('upManualKind').value, el('upManualAddr').value);
});
el('upDirect').addEventListener('click', () => applyUpstream('direct', ''));
el('upManualAddr').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter') {
    ev.preventDefault();
    applyUpstream(el('upManualKind').value, el('upManualAddr').value);
  }
});

document.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Escape') return;
  if (!el('upPanel').hidden) {
    setPanelOpen(false);
    return;
  }
  quit();
});

/* ---------- loop ---------- */

async function pollOnce() {
  try {
    const r = await fetch('/api/state', { cache: 'no-store' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    lastState = await r.json();
    render(lastState);
    document.body.classList.remove('offline');
  } catch (e) {
    showOffline();
  }
}

async function loop() {
  await pollOnce();
  timer = setTimeout(loop, POLL_MS);
}

// #upstream opens the settings panel directly.
if (location.hash === '#upstream') {
  setPanelOpen(true);
  scanUpstreams();
}

loop();
