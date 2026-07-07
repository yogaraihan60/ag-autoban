package main

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AG Autoban</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:#1a1a2e;color:#e0e0e0;padding:20px}
h1{font-size:1.5rem;margin-bottom:16px;color:#0f84d6}
.summary{display:flex;gap:16px;margin-bottom:20px;flex-wrap:wrap}
.card{background:#16213e;border-radius:8px;padding:16px 24px;min-width:120px}
.card .num{font-size:2rem;font-weight:700}
.card .lbl{font-size:.8rem;color:#888;margin-top:4px}
.card.banned .num{color:#e74c3c}
.card.active .num{color:#2ecc71}
.card.total .num{color:#0f84d6}
.toolbar{display:flex;gap:8px;margin-bottom:16px;align-items:center}
input[type=text]{flex:1;padding:8px 12px;background:#16213e;border:1px solid #333;border-radius:6px;color:#e0e0e0;font-size:.9rem}
input[type=text]:focus{outline:none;border-color:#0f84d6}
button{padding:8px 16px;border:none;border-radius:6px;cursor:pointer;font-size:.85rem;font-weight:600;transition:opacity .2s}
button:hover{opacity:.85}
button:disabled{opacity:.4;cursor:not-allowed}
.btn-release{background:#e74c3c;color:#fff}
.btn-ban{background:#f39c12;color:#000}
.btn-refresh{background:#0f84d6;color:#fff}
table{width:100%;border-collapse:collapse;background:#16213e;border-radius:8px;overflow:hidden}
th,td{padding:10px 12px;text-align:left;border-bottom:1px solid #0a0a1a;font-size:.85rem}
th{background:#0f0f23;color:#888;font-weight:600;font-size:.75rem;text-transform:uppercase;letter-spacing:.05em}
tr:hover{background:#1a2744}
.banned-row{opacity:.6}
.badge{display:inline-block;padding:2px 8px;border-radius:4px;font-size:.7rem;font-weight:600}
.badge-429{background:#e74c3c33;color:#e74c3c}
.badge-manual{background:#f39c1233;color:#f39c12}
.badge-401{background:#9b59b633;color:#9b59b6}
.badge-active{background:#2ecc7133;color:#2ecc71}
.empty{text-align:center;padding:40px;color:#666}
.toast{position:fixed;bottom:20px;right:20px;padding:12px 20px;border-radius:8px;font-size:.85rem;opacity:0;transition:opacity .3s;pointer-events:none;z-index:999}
.toast.show{opacity:1}
.toast-ok{background:#2ecc71;color:#000}
.toast-err{background:#e74c3c;color:#fff}
</style>
</head>
<body>
<h1>AG Autoban Dashboard</h1>
<div class="summary">
  <div class="card total"><div class="num" id="stat-total">-</div><div class="lbl">Total Bans</div></div>
  <div class="card banned"><div class="num" id="stat-429">-</div><div class="lbl">429 Quota</div></div>
  <div class="card banned"><div class="num" id="stat-manual">-</div><div class="lbl">Manual</div></div>
  <div class="card banned"><div class="num" id="stat-invalid">-</div><div class="lbl">Invalid Auth</div></div>
</div>
<div class="toolbar">
  <input type="text" id="filter" placeholder="Filter accounts...">
  <button class="btn-refresh" onclick="loadData()">Refresh</button>
  <button class="btn-release" onclick="releaseAll()">Release All</button>
</div>
<table>
  <thead>
    <tr><th>Account</th><th>Reason</th><th>Status Code</th><th>Banned At</th><th>Reset At</th><th>Action</th></tr>
  </thead>
  <tbody id="ban-list"></tbody>
</table>
<div class="empty" id="empty-msg" style="display:none">No active bans. All accounts are running.</div>
<div class="toast" id="toast"></div>
<script>
const API = "/v0/management/plugins/ag-autoban";
let bans = {}, invalids = {};

function getAuthHeader() {
  const m = document.cookie.match(/mgmt_key=([^;]+)/);
  if (m) return "Bearer " + m[1];
  const params = new URLSearchParams(location.search);
  const key = params.get("key");
  if (key) return "Bearer " + key;
  return "";
}

function fmtTime(ts) {
  if (!ts) return "-";
  const d = new Date(ts * 1000);
  return d.toLocaleString();
}

function showToast(msg, ok) {
  const t = document.getElementById("toast");
  t.textContent = msg;
  t.className = "toast show " + (ok ? "toast-ok" : "toast-err");
  setTimeout(() => t.className = "toast " + (ok ? "toast-ok" : "toast-err"), 2500);
}

async function loadData() {
  try {
    const r = await fetch(API + "/status", { headers: { "Authorization": getAuthHeader() } });
    const d = await r.json();
    bans = d.bans || {};
    invalids = d.invalids || {};
    render();
  } catch(e) {
    showToast("Failed to load: " + e.message, false);
  }
}

function render() {
  const filter = document.getElementById("filter").value.toLowerCase();
  const rows = [];
  let n429 = 0, nManual = 0, nInvalid = 0;

  for (const [key, e] of Object.entries(bans)) {
    if (!e.active) continue;
    if (filter && !key.toLowerCase().includes(filter)) continue;
    const isManual = e.reason === "manual ban";
    const is429 = e.reason && e.reason.includes("429");
    if (is429) n429++;
    if (isManual) nManual++;
    rows.push({ key, ...e, type: isManual ? "manual" : "429" });
  }
  for (const [key, e] of Object.entries(invalids)) {
    if (!e.active) continue;
    if (filter && !key.toLowerCase().includes(filter)) continue;
    nInvalid++;
    rows.push({ key, ...e, type: "invalid" });
  }

  document.getElementById("stat-total").textContent = rows.length;
  document.getElementById("stat-429").textContent = n429;
  document.getElementById("stat-manual").textContent = nManual;
  document.getElementById("stat-invalid").textContent = nInvalid;

  const tbody = document.getElementById("ban-list");
  tbody.innerHTML = "";
  document.getElementById("empty-msg").style.display = rows.length ? "none" : "block";

  for (const row of rows.sort((a, b) => a.key.localeCompare(b.key))) {
    const tr = document.createElement("tr");
    tr.className = "banned-row";
    const badge = row.type === "manual"
      ? '<span class="badge badge-manual">MANUAL</span>'
      : row.type === "invalid"
      ? '<span class="badge badge-401">INVALID</span>'
      : '<span class="badge badge-429">429</span>';
    tr.innerHTML = '<td>' + row.key.replace("antigravity-","").replace(".json","") + '</td>'
      + '<td>' + (row.reason || "-") + ' ' + badge + '</td>'
      + '<td>' + (row.status_code || "-") + '</td>'
      + '<td>' + fmtTime(row.banned_at) + '</td>'
      + '<td>' + fmtTime(row.reset_at) + '</td>'
      + '<td><button class="btn-release" onclick="releaseOne(\'' + row.key + '\')">Release</button></td>';
    tbody.appendChild(tr);
  }
}

async function releaseOne(key) {
  try {
    const r = await fetch(API + "/release", {
      method: "POST",
      headers: { "Authorization": getAuthHeader(), "Content-Type": "application/json" },
      body: JSON.stringify({ scope: "selected", items: [key] })
    });
    const d = await r.json();
    showToast("Released: " + (d.released || 0), true);
    loadData();
  } catch(e) { showToast(e.message, false); }
}

async function releaseAll() {
  if (!confirm("Release ALL bans?")) return;
  try {
    const r = await fetch(API + "/release", {
      method: "POST",
      headers: { "Authorization": getAuthHeader(), "Content-Type": "application/json" },
      body: JSON.stringify({ scope: "all" })
    });
    const d = await r.json();
    showToast("Released: " + (d.released || 0), true);
    loadData();
  } catch(e) { showToast(e.message, false); }
}

document.getElementById("filter").addEventListener("input", render);
loadData();
setInterval(loadData, 15000);
</script>
</body>
</html>`
