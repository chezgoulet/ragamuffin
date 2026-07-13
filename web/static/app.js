// ── App State ──────────────────────────────────────────────────────────────────
const state = {
  vaults: [],
  activeVault: null,
  page: 'search',
  searchResults: [],
  browseTree: {},
  auditData: null,
  graphData: null,
  online: true,
};

// ── Engineering Standard: fetch wrapper (#812) ──────────────────────────────────
// Every API call goes through apiFetch: retry with exponential backoff, per-attempt
// timeout, circuit breaker per endpoint, and offline detection. See
// ENGINEERING-STANDARD.md for the contract every view must satisfy.
const RETRY_ATTEMPTS = 3;
const RETRY_BACKOFF_MS = [1000, 2000, 4000];
const ATTEMPT_TIMEOUT_MS = 10000;
const CIRCUIT_THRESHOLD = 3;

const circuitFailures = {}; // endpoint -> consecutive failure count

function circuitKey(url) {
  // Collapse dynamic path segments so the breaker tracks logical endpoints.
  return url.split('?')[0].replace(/\/[0-9a-f-]{8,}/gi, '/{id}');
}

async function apiFetch(url, options = {}) {
  const key = circuitKey(url);
  if ((circuitFailures[key] || 0) >= CIRCUIT_THRESHOLD) {
    throw new ApiError(`endpoint temporarily unavailable (circuit open): ${key}`, 0, true);
  }

  let lastErr;
  for (let attempt = 0; attempt < RETRY_ATTEMPTS; attempt++) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), ATTEMPT_TIMEOUT_MS);
    try {
      const res = await fetch(url, { ...options, signal: controller.signal });
      clearTimeout(timer);
      if (res.status >= 500) {
        throw new ApiError(`HTTP ${res.status}`, res.status, false);
      }
      circuitFailures[key] = 0;
      setOnline(true);
      return res;
    } catch (err) {
      clearTimeout(timer);
      lastErr = err;
      const isLast = attempt === RETRY_ATTEMPTS - 1;
      // Client errors (4xx) surface immediately without retry.
      if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
        throw err;
      }
      if (isLast) break;
      await sleep(RETRY_BACKOFF_MS[attempt] || 4000);
    }
  }
  circuitFailures[key] = (circuitFailures[key] || 0) + 1;
  if (circuitFailures[key] >= CIRCUIT_THRESHOLD) setOnline(false);
  throw lastErr instanceof ApiError ? lastErr : new ApiError(String(lastErr && lastErr.message || lastErr), 0, false);
}

// apiJSON fetches and parses JSON, surfacing structured backend errors.
async function apiJSON(url, options) {
  const res = await apiFetch(url, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(data.message || `HTTP ${res.status}`, res.status, false);
  }
  return data;
}

class ApiError extends Error {
  constructor(message, status, circuitOpen) {
    super(message);
    this.name = 'ApiError';
    this.status = status || 0;
    this.circuitOpen = !!circuitOpen;
  }
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// ── State rendering helpers (loading / empty / error) ───────────────────────────
function renderLoading(container, label = 'Loading…') {
  container.innerHTML = `<div class="state state-loading" role="status" aria-live="polite">
    <div class="spinner" aria-hidden="true"></div><span>${escapeHtml(label)}</span></div>`;
}

function renderEmpty(container, message, hint = '') {
  container.innerHTML = `<div class="state state-empty" role="status">
    <p class="state-msg">${escapeHtml(message)}</p>
    ${hint ? `<p class="state-hint">${escapeHtml(hint)}</p>` : ''}</div>`;
}

function renderError(container, message, onRetry) {
  container.innerHTML = `<div class="state state-error" role="alert">
    <p class="state-msg">${escapeHtml(message)}</p>
    <button class="retry-btn" type="button" aria-label="Retry">Retry</button></div>`;
  const btn = container.querySelector('.retry-btn');
  if (btn && onRetry) btn.addEventListener('click', onRetry);
}

// ── Offline detection ───────────────────────────────────────────────────────────
function setOnline(online) {
  if (state.online === online) return;
  state.online = online;
  const banner = document.getElementById('offline-banner');
  if (banner) banner.hidden = online;
}

function initOfflineDetection() {
  window.addEventListener('offline', () => setOnline(false));
  window.addEventListener('online', () => setOnline(true));
  // Periodic health probe to detect server-side outages navigator.onLine misses.
  setInterval(async () => {
    try {
      const res = await fetch('/health', { signal: AbortSignal.timeout(5000) });
      setOnline(res.ok);
    } catch { setOnline(false); }
  }, 30000);
}

// ── Init ───────────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', async () => {
  initOfflineDetection();

  // Load vaults
  await loadVaults();

  // Tab switching + keyboard navigation (arrow keys move focus, Enter/Space activate)
  const tabButtons = Array.from(document.querySelectorAll('.tabs button'));
  tabButtons.forEach((btn, i) => {
    btn.addEventListener('click', () => switchPage(btn.dataset.page));
    btn.addEventListener('keydown', e => {
      let target = null;
      if (e.key === 'ArrowRight') target = tabButtons[(i + 1) % tabButtons.length];
      else if (e.key === 'ArrowLeft') target = tabButtons[(i - 1 + tabButtons.length) % tabButtons.length];
      if (target) { e.preventDefault(); target.focus(); switchPage(target.dataset.page); }
    });
  });

  // Search
  document.getElementById('search-form').addEventListener('submit', e => {
    e.preventDefault();
    doSearch();
  });

  // Graph
  document.getElementById('graph-load').addEventListener('click', loadGraph);

  // Listen for vault selector changes
  document.querySelectorAll('select[id$="-vault"]').forEach(sel => {
    sel.addEventListener('change', () => {
      state.activeVault = sel.value || null;
    });
  });
});

async function loadVaults() {
  try {
    const data = await apiJSON('/vaults');
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
  document.querySelectorAll('.tabs button').forEach(b => {
    const active = b.dataset.page === name;
    b.classList.toggle('active', active);
    b.setAttribute('aria-selected', active ? 'true' : 'false');
  });

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
  renderLoading(container, 'Searching…');

  try {
    const url = vault ? `/vault/${vault}/recall?query=${encodeURIComponent(query)}` : `/recall?query=${encodeURIComponent(query)}`;
    const data = await apiJSON(url);
    renderSearchResults(data);
  } catch (err) {
    renderError(container, `Search failed: ${err.message}`, doSearch);
  }
}

function renderSearchResults(data) {
  const container = document.getElementById('search-results');
  const results = data.results || [];
  if (results.length === 0) {
    renderEmpty(container, 'No results found', 'Try a different query or select another vault.');
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
  renderLoading(container, 'Loading files…');

  try {
    const url = vault ? `/vault/${vault}/graph?limit=100` : '/graph?limit=100';
    const data = await apiJSON(url);
    const files = (data.nodes || []).filter(n => n.type === 'file').map(n => n.label);
    if (files.length === 0) {
      renderEmpty(container, 'No files indexed', 'Add files to this vault or trigger a re-index.');
      return;
    }
    const dirs = buildDirTree(files);
    container.innerHTML = renderDirTree(dirs);
  } catch (err) {
    renderError(container, `Browse failed: ${err.message}`, loadBrowse);
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
  const detail = document.getElementById('audit-detail');
  renderLoading(container, 'Running audit…');
  if (detail) detail.innerHTML = '';

  try {
    const url = vault ? `/vault/${vault}/audit` : '/audit';
    const data = await apiJSON(url);
    state.auditData = data;
    const total = (data.staleness || []).length + (data.contradictions || []).length + (data.gaps || []).length;
    if (total === 0) {
      renderEmpty(container, 'No issues found', 'This vault is healthy — no stale files, contradictions, or gaps detected.');
      return;
    }
    renderAudit(data);
  } catch (err) {
    renderError(container, `Audit failed: ${err.message}`, loadAudit);
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
// Canvas-based interactive graph visualization with force-directed layout.
// Features: pan, zoom, click-to-highlight neighbors, tooltip on hover.

let graphSim = null; // active simulation interval

async function loadGraph() {
  const vault = document.getElementById('graph-vault').value;
  const entity = document.getElementById('graph-entity').value.trim();
  const depth = document.getElementById('graph-depth').value;
  const container = document.getElementById('graph-viz');
  renderLoading(container, 'Building graph…');

  // Kill any running sim
  if (graphSim) { clearInterval(graphSim); graphSim = null; }

  try {
    let url = vault ? `/vault/${vault}/graph?` : '/graph?';
    const params = [`depth=${depth}`, 'limit=200'];
    if (entity) params.push(`entity=${encodeURIComponent(entity)}`);
    url += params.join('&');

    const data = await apiJSON(url);
    state.graphData = data;
    if (!(data.nodes || []).length) {
      renderEmpty(container, 'No graph data', 'This vault has no linked entities yet.');
      return;
    }
    renderGraphCanvas(data);
  } catch (err) {
    renderError(container, `Graph failed: ${err.message}`, loadGraph);
  }
}

// ── Canvas Graph Renderer ──────────────────────────────────────────────────────

function renderGraphCanvas(data) {
  const container = document.getElementById('graph-viz');
  const legend = document.getElementById('graph-legend');
  const nodes = data.nodes || [];
  const edges = data.edges || [];

  if (nodes.length === 0) {
    container.innerHTML = '<div class="card">Empty graph.</div>';
    return;
  }

  // Wipe and create canvas
  container.innerHTML = '';
  const canvas = document.createElement('canvas');
  canvas.width = container.clientWidth || 800;
  canvas.height = 500;
  canvas.style.width = '100%';
  canvas.style.height = '500px';
  canvas.style.cursor = 'grab';
  container.appendChild(canvas);

  const ctx = canvas.getContext('2d');

  // Node positions
  const positions = {};
  const nodeRadius = 18;

  // Initial random layout
  nodes.forEach(n => {
    positions[n.id] = {
      x: Math.random() * canvas.width,
      y: Math.random() * canvas.height,
      vx: 0, vy: 0,
    };
  });

  // Build adjacency for neighbor highlighting
  const adj = {};
  edges.forEach(e => {
    if (!adj[e.source]) adj[e.source] = new Set();
    if (!adj[e.target]) adj[e.target] = new Set();
    adj[e.source].add(e.target);
    adj[e.target].add(e.source);
  });

  // Viewport state
  let viewX = 0, viewY = 0, zoom = 1;
  let isDragging = false, dragStartX, dragStartY, viewStartX, viewStartY;
  let hoveredNode = null;

  // Pick color by node type
  function nodeColor(n) {
    return n.type === 'entity' ? '#7ee787' : '#58a6ff';
  }

  // Force-directed simulation step
  function simulationStep() {
    const W = canvas.width;
    const H = canvas.height;

    // Repulsion between all nodes
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const a = nodes[i], b = nodes[j];
        const pa = positions[a.id], pb = positions[b.id];
        let dx = pa.x - pb.x;
        let dy = pa.y - pb.y;
        let dist = Math.sqrt(dx * dx + dy * dy) || 1;
        const force = 5000 / (dist * dist);
        const fx = dx / dist * force;
        const fy = dy / dist * force;
        pa.vx += fx; pa.vy += fy;
        pb.vx -= fx; pb.vy -= fy;
      }
    }

    // Attraction along edges
    edges.forEach(e => {
      const pa = positions[e.source];
      const pb = positions[e.target];
      if (!pa || !pb) return;
      let dx = pb.x - pa.x;
      let dy = pb.y - pa.y;
      const dist = Math.sqrt(dx * dx + dy * dy) || 1;
      const force = dist * 0.01;
      const fx = dx / dist * force;
      const fy = dy / dist * force;
      pa.vx += fx; pa.vy += fy;
      pb.vx -= fx; pb.vy -= fy;
    });

    // Center gravity
    nodes.forEach(n => {
      const p = positions[n.id];
      p.vx += (W / 2 - p.x) * 0.001;
      p.vy += (H / 2 - p.y) * 0.001;
    });

    // Apply velocities with damping
    nodes.forEach(n => {
      const p = positions[n.id];
      p.x += p.vx;
      p.y += p.vy;
      p.vx *= 0.9;
      p.vy *= 0.9;

      // Clamp to canvas
      p.x = Math.max(20, Math.min(W - 20, p.x));
      p.y = Math.max(20, Math.min(H - 20, p.y));
    });
  }

  function worldToScreen(wx, wy) {
    return {
      x: (wx + viewX) * zoom + canvas.width / 2,
      y: (wy + viewY) * zoom + canvas.height / 2,
    };
  }

  function screenToWorld(sx, sy) {
    return {
      x: (sx - canvas.width / 2) / zoom - viewX,
      y: (sy - canvas.height / 2) / zoom - viewY,
    };
  }

  function hitTest(sx, sy) {
    const w = screenToWorld(sx, sy);
    for (const n of nodes) {
      const p = positions[n.id];
      if (!p) continue;
      const dx = w.x - p.x, dy = w.y - p.y;
      if (dx * dx + dy * dy < (nodeRadius * nodeRadius)) {
        return n;
      }
    }
    return null;
  }

  function draw() {
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.save();
    ctx.translate(canvas.width / 2, canvas.height / 2);
    ctx.scale(zoom, zoom);
    ctx.translate(-canvas.width / 2 + viewX * zoom, -canvas.height / 2 + viewY * zoom);

    // Edges
    edges.forEach(e => {
      const pa = positions[e.source];
      const pb = positions[e.target];
      if (!pa || !pb) return;
      ctx.beginPath();
      ctx.moveTo(pa.x, pa.y);
      ctx.lineTo(pb.x, pb.y);
      ctx.strokeStyle = 'rgba(139, 148, 158, 0.3)';
      ctx.lineWidth = 1;
      ctx.stroke();
    });

    // Nodes
    nodes.forEach(n => {
      const p = positions[n.id];
      if (!p) return;
      const isHovered = hoveredNode && hoveredNode.id === n.id;
      const isNeighbor = hoveredNode && adj[hoveredNode.id] && adj[hoveredNode.id].has(n.id);
      const isDimmed = hoveredNode && !isHovered && !isNeighbor;

      ctx.beginPath();
      ctx.arc(p.x, p.y, nodeRadius, 0, Math.PI * 2);
      ctx.fillStyle = nodeColor(n);
      if (isDimmed) ctx.globalAlpha = 0.2;
      ctx.fill();
      if (isHovered) {
        ctx.strokeStyle = '#fff';
        ctx.lineWidth = 2;
        ctx.stroke();
      }
      ctx.globalAlpha = 1;

      // Label
      let label = n.label || n.id;
      if (label.length > 16) label = label.slice(0, 14) + '…';
      ctx.fillStyle = '#c9d1d9';
      ctx.font = '10px monospace';
      ctx.textAlign = 'center';
      ctx.fillText(label, p.x, p.y + nodeRadius + 12);
    });

    ctx.restore();
  }

  // Mouse events
  canvas.addEventListener('mousedown', e => {
    const rect = canvas.getBoundingClientRect();
    const sx = e.clientX - rect.left;
    const sy = e.clientY - rect.top;
    const hit = hitTest(sx, sy);
    if (hit) {
      // Click node — highlight neighbors
      hoveredNode = hoveredNode && hoveredNode.id === hit.id ? null : hit;
      draw();
      updateTooltip(hit);
    } else {
      hoveredNode = null;
      draw();
      clearTooltip();
      isDragging = true;
      canvas.style.cursor = 'grabbing';
      dragStartX = sx;
      dragStartY = sy;
      viewStartX = viewX;
      viewStartY = viewY;
    }
  });

  canvas.addEventListener('mousemove', e => {
    const rect = canvas.getBoundingClientRect();
    const sx = e.clientX - rect.left;
    const sy = e.clientY - rect.top;
    if (isDragging) {
      const dx = (sx - dragStartX) / zoom;
      const dy = (sy - dragStartY) / zoom;
      viewX = viewStartX - dx;
      viewY = viewStartY - dy;
      draw();
      return;
    }
    const hit = hitTest(sx, sy);
    canvas.style.cursor = hit ? 'pointer' : 'grab';
    if (hit && (!hoveredNode || hit.id !== hoveredNode.id)) {
      hoveredNode = hit;
      draw();
      updateTooltip(hit);
    } else if (!hit && hoveredNode) {
      hoveredNode = null;
      draw();
      clearTooltip();
    }
  });

  canvas.addEventListener('mouseup', () => {
    isDragging = false;
    canvas.style.cursor = 'grab';
  });

  canvas.addEventListener('mouseleave', () => {
    isDragging = false;
    hoveredNode = null;
    draw();
    clearTooltip();
  });

  canvas.addEventListener('wheel', e => {
    e.preventDefault();
    const rect = canvas.getBoundingClientRect();
    const sx = e.clientX - rect.left;
    const sy = e.clientY - rect.top;
    const w = screenToWorld(sx, sy);
    const dz = e.deltaY > 0 ? 0.9 : 1.1;
    zoom *= dz;
    zoom = Math.max(0.1, Math.min(5, zoom));
    viewX = (sx - canvas.width / 2) / zoom - w.x;
    viewY = (sy - canvas.height / 2) / zoom - w.y;
    draw();
  }, { passive: false });

  // Resize handler
  function onResize() {
    canvas.width = container.clientWidth || 800;
    // Keep positions in bounds
    draw();
  }
  window.addEventListener('resize', onResize);

  // Run simulation
  let steps = 0;
  const maxSteps = 200;
  graphSim = setInterval(() => {
    simulationStep();
    draw();
    steps++;
    if (steps >= maxSteps) {
      if (graphSim) clearInterval(graphSim);
      graphSim = null;
    }
  }, 30);

  // Tooltip
  function updateTooltip(node) {
    const tt = document.getElementById('graph-tooltip') || (() => {
      const el = document.createElement('div');
      el.id = 'graph-tooltip';
      el.style.cssText = 'position:absolute;background:#161b22;border:1px solid #30363d;border-radius:6px;padding:8px 12px;color:#c9d1d9;font-size:12px;pointer-events:none;z-index:100;max-width:300px;';
      container.appendChild(el);
      return el;
    })();
    tt.style.display = 'block';
    tt.textContent = `${node.type === 'entity' ? '◆' : '📄'} ${node.label || node.id}`;
    // Position near cursor — update on mousemove
    // Remove previous listener before re-adding to avoid accumulation
    canvas.removeEventListener('mousemove', posTooltip);
    canvas.addEventListener('mousemove', posTooltip);
  }

  function posTooltip(e) {
    const tt = document.getElementById('graph-tooltip');
    if (!tt) return;
    const rect = container.getBoundingClientRect();
    tt.style.left = (e.clientX - rect.left + 15) + 'px';
    tt.style.top = (e.clientY - rect.top - 10) + 'px';
  }

  function clearTooltip() {
    const tt = document.getElementById('graph-tooltip');
    if (tt) { tt.style.display = 'none'; }
    canvas.removeEventListener('mousemove', posTooltip);
  }

  // Update legend
  legend.innerHTML = `
    <span style="display:inline-flex;align-items:center;gap:4px;margin-right:12px"><span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#58a6ff"></span> File</span>
    <span style="display:inline-flex;align-items:center;gap:4px;margin-right:12px"><span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:#7ee787"></span> Entity</span>
    <span style="color:#8b949e;font-size:0.85rem">${nodes.length} nodes · ${edges.length} edges · pan/zoom · click to highlight</span>
  `;
}

// ── Helpers ────────────────────────────────────────────────────────────────────
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
