package transport

// DashboardHTML is the embedded dashboard page. The page's script is served
// separately at /dashboard.js so we can keep CSP as "script-src 'self'"
// without relying on brittle inline-script hashes.
const DashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DFMT Dashboard</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
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
</style>
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
<div id="stats" style="display:none">
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
</div>
</div>
<script src="/dashboard.js"></script>
</body>
</html>
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
  statsEl.style.display = 'none';
}

function showStats() {
  loadingEl.style.display = 'none';
  statsEl.style.display = 'block';
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
      return;
    }
    daemons.forEach(function(d) {
      var opt = document.createElement('option');
      opt.value = d.project_path || '';
      opt.textContent = (d.project_path || '').split(/[/\\]/).pop() + ' (' + (d.project_path || '') + ')';
      projectSelect.appendChild(opt);
    });
  } catch (err) {
    console.error('Failed to load daemons:', err);
  }
}

async function loadStatsForProject(projectPath) {
  showLoading();
  try {
    var resp = await fetch('/api/proxy', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        target_project_path: projectPath,
        method: 'dfmt.stats',
        params: {no_cache: true}
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

function init() {
  errorEl = document.getElementById('error');
  loadingEl = document.getElementById('loading');
  statsEl = document.getElementById('stats');
  refreshBtn = document.getElementById('refreshBtn');
  projectSelect = document.getElementById('projectSelect');
  daemonBadge = document.getElementById('daemonBadge');

  refreshBtn.addEventListener('click', loadStats);
  projectSelect.addEventListener('change', function() {
    var selected = projectSelect.value;
    if (selected) {
      loadStatsForProject(selected);
    }
  });
  loadDaemons();
  loadStats();
}

document.addEventListener('DOMContentLoaded', init);
})();
`
