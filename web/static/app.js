// ── App State ──────────────────────────────────────────────────────────────────
const state = {
  vaults: [],
  activeVault: null,
  page: 'search',
  searchResults: [],
  browseTree: {},
  auditData: null,
  graphData: null,
};

// ── Init ───────────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', async () => {
  // Load vaults
  await loadVaults();

  // Tab switching
  document.querySelectorAll('.tabs button').forEach(btn => {
    btn.addEventListener('click', () => switchPage(btn.dataset.page));
  });

  // Search
  document.getElementById('search-form').addEventListener('submit', e => {
    e.preventDefault();
    doSearch();
  });

  // Graph
  document.getElementById('graph-load').addEventListener('click', loadGraph);

  // Auto-load audit and browse on tab switch
  // (lazy loaded when tab is shown)

  // Listen for vault selector changes
  document.querySelectorAll('select[id$="-vault"]').forEach(sel => {
    sel.addEventListener('change', () => {
      state.activeVault = sel.value || null;
    });
  });
});

async function loadVaults() {
  try {
    const res = await fetch('/vaults');
    const data = await res.json();
    state.vaults = data.vaults || [];
    populateVaultSelectors();
  } catch (err) {
    console.error('Failed to load vaults:', err);
  }
}

function populateVaultSelectors() {
  const selectors = ['search-vault', 'browse-vault', 'audit-vault', 'graph-vault'];
  selectors.forEach(id => {
    const sel = document.getElementById(id);
    if (!sel) return;
    sel.innerHTML = '';
    // In single-tenant, vaults might be a single "default"
    state.vaults.forEach(v => {
      const opt = document.createElement('option');
      opt.value = v.name;
      opt.textContent = v.name;
      sel.appendChild(opt);
    });
    if (state.vaults.length === 1) {
      state.activeVault = state.vaults[0].name;
    }
  });
}

function switchPage(name) {
  state.page = name;
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.getElementById(`page-${name}`).classList.add('active');
  document.querySelectorAll('.tabs button').forEach(b => b.classList.remove('active'));
  document.querySelector(`.tabs button[data-page="${name}"]`).classList.add('active');

  // Lazy load
  if (name === 'browse') loadBrowse();
  if (name === 'audit') loadAudit();
  if (name === 'graph') {
    state.activeVault = document.getElementById('graph-vault').value || state.activeVault;
  }
}

// ── Search ─────────────────────────────────────────────────────────────────────
async function doSearch() {
  const query = document.getElementById('search-query').value.trim();
  if (!query) return;
  const vault = document.getElementById('search-vault').value;
  const container = document.getElementById('search-results');
  container.innerHTML = '<div class="loading">Searching...</div>';

  try {
    const url = vault ? `/vault/${vault}/recall?query=${encodeURIComponent(query)}` : `/recall?query=${encodeURIComponent(query)}`;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    renderSearchResults(data);
  } catch (err) {
    container.innerHTML = `<div class="error">Search failed: ${err.message}</div>`;
  }
}

function renderSearchResults(data) {
  const container = document.getElementById('search-results');
  const results = data.results || [];
  if (results.length === 0) {
    container.innerHTML = '<div class="card">No results found.</div>';
    return;
  }
  let html = `<div class="card"><div class="value">${results.length}</div><div class="sub">results</div></div>`;
  results.forEach(r => {
    const links = r.links_to && r.links_to.length ? r.links_to.join(', ') : '';
    html += `<div class="result-item">
      <div class="source">${r.source_file || 'unknown'}</div>
      <div class="score">Score: ${(r.score * 100).toFixed(1)}%</div>
      <div class="preview">${escapeHtml(r.text || '').slice(0, 500)}</div>
      ${links ? `<div class="links">↗ ${escapeHtml(links)}</div>` : ''}
    </div>`;
  });
  container.innerHTML = html;
}

// ── Browse ─────────────────────────────────────────────────────────────────────
async function loadBrowse() {
  const vault = document.getElementById('browse-vault').value;
  const container = document.getElementById('browse-tree');
  container.innerHTML = '<div class="loading">Loading...</div>';

  try {
    // Use recall with empty query to get all indexed files, or use the graph
    // For simplicity, we'll fetch graph to discover files
    const url = vault ? `/vault/${vault}/graph?limit=100` : '/graph?limit=100';
    const res = await fetch(url);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    if (!data.nodes) { container.innerHTML = '<div class="card">No files indexed.</div>'; return; }

    const files = data.nodes.filter(n => n.type === 'file').map(n => n.label);
    const dirs = buildDirTree(files);
    container.innerHTML = renderDirTree(dirs);
  } catch (err) {
    container.innerHTML = `<div class="error">Browse failed: ${err.message}</div>`;
  }
}

function buildDirTree(files) {
  const root = {};
  files.forEach(f => {
    const parts = f.split(' / ');
    let node = root;
    parts.forEach((part, i) => {
      if (!node[part]) node[part] = i === parts.length - 1 ? null : {};
      node = node[part];
    });
  });
  return root;
}

function renderDirTree(tree, depth = 0) {
  let html = '';
  const indent = '  '.repeat(depth);
  for (const [key, val] of Object.entries(tree)) {
    if (val === null) {
      html += `<div class="tree-entry tree-file" style="padding-left:${depth * 1.2}rem">${escapeHtml(key)}</div>`;
    } else {
      html += `<div class="tree-entry tree-dir" style="padding-left:${depth * 1.2}rem;font-weight:600">${escapeHtml(key)}/</div>`;
      html += renderDirTree(val, depth + 1);
    }
  }
  return html;
}

// ── Audit ──────────────────────────────────────────────────────────────────────
async function loadAudit() {
  const vault = document.getElementById('audit-vault').value;
  const container = document.getElementById('audit-summary');
  container.innerHTML = '<div class="loading">Loading...</div>';

  try {
    const url = vault ? `/vault/${vault}/audit` : '/audit';
    const res = await fetch(url);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    state.auditData = data;
    renderAudit(data);
  } catch (err) {
    container.innerHTML = `<div class="error">Audit failed: ${err.message}</div>`;
  }
}

function renderAudit(data) {
  const container = document.getElementById('audit-summary');
  const detail = document.getElementById('audit-detail');

  const staleness = (data.staleness || []).length;
  const contradictions = (data.contradictions || []).length;
  const gaps = (data.gaps || []).length;

  container.innerHTML = `
    <div class="card"><h3>Staleness</h3><div class="value">${staleness}</div><div class="sub">outdated facts</div></div>
    <div class="card"><h3>Contradictions</h3><div class="value">${contradictions}</div><div class="sub">conflicts</div></div>
    <div class="card"><h3>Gaps</h3><div class="value">${gaps}</div><div class="sub">missing info</div></div>
  `;

  detail.innerHTML = '';
  if (data.staleness && data.staleness.length) {
    detail.innerHTML += '<div class="audit-section"><h3>Stale Facts</h3><pre>' + escapeHtml(JSON.stringify(data.staleness, null, 2)) + '</pre></div>';
  }
  if (data.contradictions && data.contradictions.length) {
    detail.innerHTML += '<div class="audit-section"><h3>Contradictions</h3><pre>' + escapeHtml(JSON.stringify(data.contradictions, null, 2)) + '</pre></div>';
  }
  if (data.gaps && data.gaps.length) {
    detail.innerHTML += '<div class="audit-section"><h3>Gaps</h3><pre>' + escapeHtml(JSON.stringify(data.gaps, null, 2)) + '</pre></div>';
  }
}

// ── Graph ──────────────────────────────────────────────────────────────────────
async function loadGraph() {
  const vault = document.getElementById('graph-vault').value;
  const entity = document.getElementById('graph-entity').value.trim();
  const depth = document.getElementById('graph-depth').value;
  const container = document.getElementById('graph-viz');
  container.innerHTML = '<div class="loading">Loading...</div>';

  try {
    let url = vault ? `/vault/${vault}/graph?` : '/graph?';
    const params = [`depth=${depth}`, 'limit=200'];
    if (entity) params.push(`entity=${encodeURIComponent(entity)}`);
    url += params.join('&');

    const res = await fetch(url);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    state.graphData = data;
    renderGraph(data);
  } catch (err) {
    container.innerHTML = `<div class="error">Graph failed: ${err.message}</div>`;
  }
}

function renderGraph(data) {
  const container = document.getElementById('graph-viz');
  const legend = document.getElementById('graph-legend');
  const nodes = data.nodes || [];
  const edges = data.edges || [];

  if (nodes.length === 0) {
    container.innerHTML = '<div class="card">Empty graph.</div>';
    return;
  }

  // Render nodes
  let html = '';
  nodes.forEach(n => {
    const typeClass = n.type === 'entity' ? 'entity' : 'file';
    const icon = n.type === 'entity' ? '◆' : '📄';
    html += `<div class="graph-node ${typeClass}" title="${escapeHtml(n.id)}">${icon} ${escapeHtml(n.label)}</div>`;
  });
  container.innerHTML = html;

  // Render legend
  legend.innerHTML = `
    <span class="graph-node file">📄 File</span>
    <span class="graph-node entity">◆ Entity</span>
    <span style="margin-left:1rem;color:#8b949e">${nodes.length} nodes, ${edges.length} edges</span>
  `;
}

// ── Helpers ────────────────────────────────────────────────────────────────────
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
