package transport

// DashboardHTML is the embedded dashboard page. Both CSS and JS are served
// from external files (/dashboard.css, /dashboard.js) so the CSP can stay
// strict ("style-src 'self'; script-src 'self'") without inline-element
// hashes that drift every time the markup changes.
const DashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DFMT Dashboard</title>
<link rel="stylesheet" href="/dashboard.css">
<link rel="icon" href="data:,">
</head>
<body>
<div class="container">
<div class="header">
<div class="header-left">
<h1>DFMT Dashboard</h1>
<span class="daemon-badge" id="daemonBadge">Local</span>
</div>
<div class="header-right">
<select id="projectSelect">
<option value="">Loading projects...</option>
</select>
<button class="btn" id="refreshBtn">Refresh</button>
</div>
</div>
<div id="error" class="error"></div>
<div id="loading" class="loading">Loading stats...</div>
<div id="stats" class="hidden">
<div class="cards">
<div class="card"><div class="card-value" id="total-events">0</div><div class="card-label">Total Events</div></div>
<div class="card"><div class="card-value" id="session-duration">-</div><div class="card-label">Session Duration</div></div>
</div>
<h2>MCP Byte Savings</h2>
<div class="cards">
<div class="card"><div class="card-value" id="raw-bytes">0</div><div class="card-label">Raw Bytes</div></div>
<div class="card"><div class="card-value" id="returned-bytes">0</div><div class="card-label">Returned Bytes</div></div>
<div class="card"><div class="card-value" id="bytes-saved">0</div><div class="card-label">Bytes Saved</div></div>
<div class="card"><div class="card-value" id="compression-ratio">0%</div><div class="card-label">Compression</div></div>
<div class="card"><div class="card-value" id="dedup-hits">0</div><div class="card-label">Stash Dedup Hits</div></div>
</div>
<h2>LLM Token Metrics</h2>
<div class="cards">
<div class="card"><div class="card-value" id="total-input">0</div><div class="card-label">Input Tokens</div></div>
<div class="card"><div class="card-value" id="total-output">0</div><div class="card-label">Output Tokens</div></div>
<div class="card"><div class="card-value" id="token-savings">0</div><div class="card-label">Cache Savings</div></div>
<div class="card"><div class="card-value" id="cache-hit-rate">0%</div><div class="card-label">Cache Hit Rate</div></div>
</div>
<h2>Events by Type</h2>
<div class="chart"><div class="bar-chart" id="type-chart"></div></div>
<h2>Events by Priority</h2>
<div class="chart"><div class="bar-chart" id="priority-chart"></div></div>
<h2>Session Info</h2>
<div class="session">
<div class="session-row"><span>Session Start</span><span id="session-start">-</span></div>
<div class="session-row"><span>Session End</span><span id="session-end">-</span></div>
</div>
<h2>Live Events</h2>
<div class="event-log" id="eventLog"><div class="event-log-empty">Waiting for events...</div></div>
</div>
</div>
<script src="/dashboard.js"></script>
</body>
</html>
`

// DashboardCSS is the dashboard's stylesheet, served at /dashboard.css so the
// CSP `style-src 'self'` directive is enough — no inline-style hashes to keep
// in sync with the source. The colors/layout match the prior inline block
// byte-for-byte; only the delivery channel changed.
const DashboardCSS = `* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #1a1a2e; color: #eee; min-height: 100vh; padding: 20px; }
h1 { color: #00d4ff; margin-bottom: 20px; font-size: 1.5rem; }
h2 { color: #aaa; margin: 20px 0 10px; font-size: 1rem; text-transform: uppercase; letter-spacing: 1px; }
.container { max-width: 900px; margin: 0 auto; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; flex-wrap: wrap; gap: 10px; }
.header-left { display: flex; align-items: center; gap: 15px; }
.header-right { display: flex; align-items: center; gap: 10px; }
select { background: #16213e; color: #eee; border: 1px solid #0f3460; border-radius: 6px; padding: 8px 12px; font-size: 0.9rem; min-width: 200px; cursor: pointer; }
select:focus { outline: none; border-color: #00d4ff; }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 15px; margin-bottom: 20px; }
.card { background: #16213e; border-radius: 8px; padding: 15px; border: 1px solid #0f3460; }
.card-value { font-size: 2rem; font-weight: bold; color: #00d4ff; }
.card-label { font-size: 0.8rem; color: #888; margin-top: 5px; }
.chart { background: #16213e; border-radius: 8px; padding: 15px; border: 1px solid #0f3460; }
.bar-chart { display: flex; flex-direction: column; gap: 8px; }
.bar-row { display: flex; align-items: center; gap: 10px; }
.bar-label { width: 120px; font-size: 0.8rem; color: #aaa; text-align: right; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bar-container { flex: 1; height: 20px; background: #0f3460; border-radius: 4px; overflow: hidden; }
.bar-fill { height: 100%; background: linear-gradient(90deg, #00d4ff, #00ff88); border-radius: 4px; transition: width 0.3s; min-width: 2px; }
.bar-value { width: 60px; font-size: 0.8rem; color: #888; text-align: right; }
.btn { background: #0f3460; color: #00d4ff; border: 1px solid #00d4ff; border-radius: 6px; padding: 10px 20px; cursor: pointer; font-size: 0.9rem; }
.btn:hover { background: #00d4ff; color: #1a1a2e; }
.session { background: #16213e; border-radius: 8px; padding: 15px; border: 1px solid #0f3460; font-size: 0.9rem; }
.session-row { display: flex; justify-content: space-between; padding: 5px 0; border-bottom: 1px solid #0f3460; }
.session-row:last-child { border-bottom: none; }
.loading { text-align: center; padding: 40px; color: #888; }
.error { background: #ff4444; color: white; padding: 15px; border-radius: 8px; margin-bottom: 20px; display: none; }
.daemon-badge { background: #00ff88; color: #1a1a2e; padding: 4px 8px; border-radius: 4px; font-size: 0.75rem; font-weight: bold; }
.daemon-badge.dead { background: #ff4444; color: white; }
.hidden { display: none; }
.event-log { background: #16213e; border: 1px solid #0f3460; border-radius: 6px; padding: 10px; max-height: 280px; overflow-y: auto; font-family: 'SF Mono', Monaco, Menlo, Consolas, monospace; font-size: 0.78rem; }
.event-log-empty { color: #666; font-style: italic; padding: 6px; }
.event-log-row { padding: 4px 6px; border-bottom: 1px solid #0f3460; line-height: 1.5; }
.event-log-row:last-child { border-bottom: none; }
.event-log-ts { color: #888; }
.event-log-type { color: #00d4ff; margin-left: 8px; font-weight: 600; }
.event-log-prio-p1 { color: #ff6b6b; }
.event-log-prio-p2 { color: #ffd93d; }
.event-log-prio-p3 { color: #6bcf7f; }
.event-log-prio-p4 { color: #888; }
.event-log-msg { color: #ccc; margin-left: 8px; }
`

// DashboardJS is the dashboard's JavaScript, served at /dashboard.js so CSP
// can stay strict ("script-src 'self'"). The dashboard only talks to its own
// origin — we removed the prior cross-daemon fetch feature because it fought
// CORS/CSRF and did not carry the per-daemon auth token anyway.
const DashboardJS = `(function() {
var errorEl, loadingEl, statsEl, refreshBtn, projectSelect, daemonBadge;

function showError(msg) {
  errorEl.textContent = msg;
  errorEl.style.display = 'block';
  loadingEl.style.display = 'none';
}

function showLoading() {
  errorEl.style.display = 'none';
  loadingEl.style.display = 'block';
  statsEl.classList.add('hidden');
}

function showStats() {
  loadingEl.style.display = 'none';
  statsEl.classList.remove('hidden');
}

function formatDuration(ms) {
  var hours = Math.floor(ms / 3600000);
  var mins = Math.floor((ms % 3600000) / 60000);
  if (hours > 0) return hours + 'h ' + mins + 'm';
  return mins + 'm';
}

function formatNumber(num) {
  if (num >= 1000000) return (num / 1000000).toFixed(1) + 'M';
  if (num >= 1000) return (num / 1000).toFixed(1) + 'K';
  return num.toString();
}

function renderChart(containerId, data) {
  var container = document.getElementById(containerId);
  container.innerHTML = '';

  if (!data || Object.keys(data).length === 0) {
    var empty = document.createElement('div');
    empty.style.cssText = 'color:#888;text-align:center;padding:20px;';
    empty.textContent = 'No data';
    container.appendChild(empty);
    return;
  }

  var entries = Object.entries(data).sort(function(a, b) { return b[1] - a[1]; });
  var max = Math.max.apply(null, entries.map(function(e) { return e[1]; }));

  entries.forEach(function(entry) {
    var label = entry[0];
    var value = entry[1];

    var row = document.createElement('div');
    row.className = 'bar-row';

    var labelEl = document.createElement('div');
    labelEl.className = 'bar-label';
    labelEl.textContent = label.length > 15 ? label.substring(0, 12) + '...' : label;

    var barContainer = document.createElement('div');
    barContainer.className = 'bar-container';

    var barFill = document.createElement('div');
    barFill.className = 'bar-fill';
    barFill.style.width = (value / max * 100) + '%';

    barContainer.appendChild(barFill);

    var valueEl = document.createElement('div');
    valueEl.className = 'bar-value';
    valueEl.textContent = value;

    row.appendChild(labelEl);
    row.appendChild(barContainer);
    row.appendChild(valueEl);
    container.appendChild(row);
  });
}

async function loadDaemons() {
  try {
    var resp = await fetch('/api/all-daemons');
    var daemons = await resp.json();
    projectSelect.innerHTML = '';
    if (!daemons || daemons.length === 0) {
      var opt = document.createElement('option');
      opt.value = '';
      opt.textContent = 'No running daemons';
      projectSelect.appendChild(opt);
      return null;
    }
    daemons.forEach(function(d) {
      var opt = document.createElement('option');
      opt.value = d.project_path || '';
      opt.textContent = (d.project_path || '').split(/[/\\]/).pop() + ' (' + (d.project_path || '') + ')';
      projectSelect.appendChild(opt);
    });
    return daemons[0].project_path || null;
  } catch (err) {
    console.error('Failed to load daemons:', err);
    return null;
  }
}

async function loadStatsForProject(projectPath) {
  showLoading();
  try {
    // Phase 2: the host-wide daemon serves every project from one
    // process, so we just POST /api/stats with project_id stamped
    // in params and let the daemon route to the right cache. The
    // older /api/proxy path (registry-lookup + cross-daemon HTTP
    // forward) is preserved on the daemon side for v0.3.x straddle
    // setups but the dashboard no longer needs it for the common
    // global-mode flow.
    var resp = await fetch('/api/stats', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        jsonrpc: '2.0',
        method: 'dfmt.stats',
        params: {project_id: projectPath, no_cache: true},
        id: 1
      })
    });
    var data = await resp.json();
    if (data.error) throw new Error(data.error.message || 'Unknown error');
    var stats = data.result;
    document.getElementById('total-events').textContent = stats.events_total;
    document.getElementById('total-input').textContent = formatNumber(stats.total_input_tokens || 0);
    document.getElementById('total-output').textContent = formatNumber(stats.total_output_tokens || 0);
    document.getElementById('token-savings').textContent = formatNumber(stats.token_savings || 0);
    document.getElementById('cache-hit-rate').textContent = (stats.cache_hit_rate || 0).toFixed(1) + '%';
    document.getElementById('raw-bytes').textContent = formatNumber(stats.total_raw_bytes || 0);
    document.getElementById('returned-bytes').textContent = formatNumber(stats.total_returned_bytes || 0);
    document.getElementById('bytes-saved').textContent = formatNumber(stats.bytes_saved || 0);
    document.getElementById('compression-ratio').textContent = ((stats.compression_ratio || 0) * 100).toFixed(1) + '%';
    document.getElementById('dedup-hits').textContent = formatNumber(stats.dedup_hits || 0);
    if (stats.session_start && stats.session_end) {
      var start = new Date(stats.session_start);
      var end = new Date(stats.session_end);
      document.getElementById('session-duration').textContent = formatDuration(end - start);
      document.getElementById('session-start').textContent = start.toLocaleString();
      document.getElementById('session-end').textContent = end.toLocaleString();
    }
    renderChart('type-chart', stats.events_by_type);
    renderChart('priority-chart', stats.events_by_priority);
    showStats();
  } catch (err) {
    showError('Error loading stats for ' + projectPath + ': ' + err.message);
  }
}

async function loadStats() {
  showLoading();
  try {
    var resp = await fetch('/api/stats', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({jsonrpc: '2.0', method: 'dfmt.stats', params: {}, id: 1})
    });
    var data = await resp.json();
    if (data.error) throw new Error(data.error.message || 'Unknown error');
    var stats = data.result;
    document.getElementById('total-events').textContent = stats.events_total;
    document.getElementById('total-input').textContent = formatNumber(stats.total_input_tokens || 0);
    document.getElementById('total-output').textContent = formatNumber(stats.total_output_tokens || 0);
    document.getElementById('token-savings').textContent = formatNumber(stats.token_savings || 0);
    document.getElementById('cache-hit-rate').textContent = (stats.cache_hit_rate || 0).toFixed(1) + '%';
    document.getElementById('raw-bytes').textContent = formatNumber(stats.total_raw_bytes || 0);
    document.getElementById('returned-bytes').textContent = formatNumber(stats.total_returned_bytes || 0);
    document.getElementById('bytes-saved').textContent = formatNumber(stats.bytes_saved || 0);
    document.getElementById('compression-ratio').textContent = ((stats.compression_ratio || 0) * 100).toFixed(1) + '%';
    document.getElementById('dedup-hits').textContent = formatNumber(stats.dedup_hits || 0);
    if (stats.session_start && stats.session_end) {
      var start = new Date(stats.session_start);
      var end = new Date(stats.session_end);
      document.getElementById('session-duration').textContent = formatDuration(end - start);
      document.getElementById('session-start').textContent = start.toLocaleString();
      document.getElementById('session-end').textContent = end.toLocaleString();
    }
    renderChart('type-chart', stats.events_by_type);
    renderChart('priority-chart', stats.events_by_priority);
    showStats();
  } catch (err) {
    showError('Error: ' + err.message);
  }
}

// Live event log via /api/stream SSE. Pre-v0.5.0 the endpoint was
// one-shot playback (closed at HEAD); v0.5.0 added a polling tail
// loop server-side, so this client subscription stays open and
// receives events in near-real-time. closeEventLog cleanly tears
// down the prior EventSource when the user switches projects so the
// dashboard never accumulates orphan SSE connections.
var eventLogES = null;
var EVENT_LOG_MAX_ROWS = 100;

function closeEventLog() {
  if (eventLogES) {
    try { eventLogES.close(); } catch (_) {}
    eventLogES = null;
  }
}

function escapeText(s) {
  // Plain text helper — never inject HTML. The dashboard's CSP forbids
  // inline scripts but a leaked <script> tag in event data could still
  // confuse a future maintainer reading the DOM.
  if (s == null) return '';
  return String(s);
}

function appendEventRow(e) {
  var log = document.getElementById('eventLog');
  if (!log) return;
  // Drop the placeholder once the first event arrives.
  var empty = log.querySelector('.event-log-empty');
  if (empty) empty.remove();

  var row = document.createElement('div');
  row.className = 'event-log-row';

  var ts = document.createElement('span');
  ts.className = 'event-log-ts';
  var d = e.ts ? new Date(e.ts) : new Date();
  ts.textContent = d.toLocaleTimeString();
  row.appendChild(ts);

  var type = document.createElement('span');
  type.className = 'event-log-type event-log-prio-' + escapeText(e.priority || 'p4');
  type.textContent = escapeText(e.type || '');
  row.appendChild(type);

  // Surface a one-line summary from common fields. Code/path/url cover
  // exec/read/fetch; tags cover remember/note. Truncate so a long line
  // doesn't blow out the log row width.
  var msgText = '';
  if (e.data) {
    msgText = e.data.code || e.data.path || e.data.url || '';
  }
  if (!msgText && Array.isArray(e.tags) && e.tags.length > 0) {
    msgText = e.tags.join(' ');
  }
  if (msgText) {
    var msg = document.createElement('span');
    msg.className = 'event-log-msg';
    msg.textContent = ' ' + (msgText.length > 80 ? msgText.slice(0, 77) + '...' : msgText);
    row.appendChild(msg);
  }

  // Append-bottom + auto-scroll matches a streaming log feel. Cap row
  // count so a long-running session doesn't grow the DOM unboundedly.
  log.appendChild(row);
  while (log.childElementCount > EVENT_LOG_MAX_ROWS) {
    log.removeChild(log.firstChild);
  }
  log.scrollTop = log.scrollHeight;
}

function openEventLog(projectPath) {
  closeEventLog();
  if (!projectPath) return;
  var url = '/api/stream?follow=true&project_id=' + encodeURIComponent(projectPath);
  try {
    eventLogES = new EventSource(url);
  } catch (err) {
    console.error('EventSource construction failed:', err);
    return;
  }
  eventLogES.onmessage = function(ev) {
    try {
      var e = JSON.parse(ev.data);
      appendEventRow(e);
    } catch (err) {
      console.error('SSE parse error:', err);
    }
  };
  eventLogES.onerror = function() {
    // Browsers retry EventSource automatically; we just log and rely
    // on the built-in retry. If the server is down for good, the user
    // will notice via the stats card going stale.
    console.warn('SSE connection error (will retry)');
  };
}

function refreshCurrentView() {
  // Refresh button must respect the project selector. Pre-Phase-2 the
  // dashboard had a single daemon view so loadStats() was unambiguous;
  // in global-daemon mode every project lives behind project_id, so a
  // bare loadStats() POSTs an empty params and the daemon answers
  // -32603 (errProjectIDRequired). Route through the selected project
  // when one is picked; fall back to loadStats() only for the legacy
  // single-project daemon path where the daemon has a defaultProject.
  var selected = projectSelect && projectSelect.value;
  if (selected) {
    loadStatsForProject(selected);
  } else {
    loadStats();
  }
}

async function init() {
  errorEl = document.getElementById('error');
  loadingEl = document.getElementById('loading');
  statsEl = document.getElementById('stats');
  refreshBtn = document.getElementById('refreshBtn');
  projectSelect = document.getElementById('projectSelect');
  daemonBadge = document.getElementById('daemonBadge');

  refreshBtn.addEventListener('click', refreshCurrentView);
  projectSelect.addEventListener('change', function() {
    var selected = projectSelect.value;
    if (selected) {
      loadStatsForProject(selected);
      openEventLog(selected);
    } else {
      closeEventLog();
    }
  });
  // Resolve which project to load before kicking off the first stats
  // request. In global-daemon mode we MUST stamp project_id on the
  // first call; without this the initial /api/stats POST returns
  // -32603 and the page shows an error before the user has done
  // anything. loadDaemons returns the first daemon's project path (or
  // null when no daemons / legacy single-project mode).
  var firstProject = await loadDaemons();
  if (firstProject) {
    projectSelect.value = firstProject;
    loadStatsForProject(firstProject);
    openEventLog(firstProject);
  } else {
    loadStats();
  }
}

document.addEventListener('DOMContentLoaded', init);
})();
`
