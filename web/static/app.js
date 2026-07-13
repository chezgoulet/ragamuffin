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
  showExplanation: true,
};

// ── Engineering Standard: fetch wrapper (#812) ──────────────────────────────────
// Every API call goes through apiFetch: retry with exponential backoff, per-attempt
// timeout, circuit breaker per endpoint, and offline detection. See
// ENGINEERING-STANDARD.md for the contract every view must satisfy.
const RETRY_ATTEMPTS = 3;
const RETRY_BACKOFF_MS = [1000, 2000, 4000];
const ATTEMPT_TIMEOUT_MS = 10000;
const CIRCUIT_THRESHOLD = 3;
const CIRCUIT_COOLDOWN_MS = 30000;

const circuitFailures = {}; // endpoint -> consecutive failure count
const circuitOpenedAt = {}; // endpoint -> timestamp when circuit opened

function circuitKey(url) {
  // Collapse dynamic path segments so the breaker tracks logical endpoints.
  return url.split('?')[0].replace(/\/[0-9a-f-]{8,}/gi, '/{id}');
}

async function apiFetch(url, options = {}) {
  const key = circuitKey(url);
  const fails = circuitFailures[key] || 0;
  const opened = circuitOpenedAt[key] || 0;

  // Circuit breaker: if open and still within cooldown, fail fast.
  if (fails >= CIRCUIT_THRESHOLD) {
    if (Date.now() - opened < CIRCUIT_COOLDOWN_MS) {
      throw new ApiError(`endpoint temporarily unavailable (circuit open): ${key}`, 0, true);
    }
    // Cooldown expired — half-open: allow one probe, set to threshold-1
    circuitFailures[key] = CIRCUIT_THRESHOLD - 1;
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
  if (circuitFailures[key] >= CIRCUIT_THRESHOLD) {
    circuitOpenedAt[key] = Date.now();
    setOnline(false);
  }
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
  // Periodic health probe — detects server-side outages navigator.onLine misses.
  // Uses AbortSignal.timeout() with a fallback for older browsers.
  setInterval(async () => {
    let signal;
    try {
      signal = AbortSignal.timeout(5000);
    } catch {
      const ctrl = new AbortController();
      setTimeout(() => ctrl.abort(), 5000);
      signal = ctrl.signal;
    }
    try {
      const res = await fetch('/health', { signal });
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

  // Review queue refresh
  const reviewRefresh = document.getElementById('review-refresh');
  if (reviewRefresh) reviewRefresh.addEventListener('click', loadReview);
  const reviewReason = document.getElementById('review-reason');
  if (reviewReason) reviewReason.addEventListener('change', loadReview);

  // Facts search
  const factSearchBtn = document.getElementById('fact-search-btn');
  if (factSearchBtn) factSearchBtn.addEventListener('click', loadFacts);
  const factSearch = document.getElementById('fact-search');
  if (factSearch) factSearch.addEventListener('keydown', e => { if (e.key === 'Enter') loadFacts(); });

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
  const selectors = ['search-vault', 'browse-vault', 'audit-vault', 'graph-vault', 'facts-vault', 'ingest-vault'];
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
  if (name === 'review') loadReview();
  if (name === 'facts') loadFacts();
  if (name === 'debt') loadDebt();
  if (name === 'gaps') loadGaps();
  if (name === 'agents') loadAgents();
  if (name === 'ingest') loadIngest();
  if (name === 'vaultadmin') loadVaultAdmin();
  if (name === 'graph') {
    state.activeVault = document.getElementById('graph-vault').value || state.activeVault;
  }
}

// ── Search ─────────────────────────────────────────────────────────────────────
async function doSearch() {
  const query = document.getElementById('search-query').value.trim();
  if (!query) return;
  const vault = document.getElementById('search-vault').value;
  const useAsk = document.getElementById('search-mode-ask').checked;
  const container = document.getElementById('search-results');
  renderLoading(container, useAsk ? 'Synthesizing…' : 'Searching…');

  try {
    let data;
    if (useAsk) {
      const url = vault ? `/vault/${vault}/ask` : '/ask';
      data = await apiJSON(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({query, top_k: 8, mode: 'auto'}),
      });
      renderAskResults(data);
    } else {
      const url = vault ? `/vault/${vault}/recall?query=${encodeURIComponent(query)}` : `/recall?query=${encodeURIComponent(query)}`;
      data = await apiJSON(url);
      renderSearchResults(data);
    }
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

function renderAskResults(data) {
  const container = document.getElementById('search-results');
  if (!data.answer) {
    renderEmpty(container, 'No answer generated', 'The LLM could not answer this query.');
    return;
  }
  let html = `<div class="card" style="margin-bottom:1rem">
    <h3>Answer</h3>
    <p style="line-height:1.6">${escapeHtml(data.answer)}</p>
    <div style="color:#8b949e;font-size:0.85rem;margin-top:0.5rem">
      Mode: ${data.mode_used || 'rag'} | Sources: ${(data.sources || []).length}
    </div>
  </div>`;

  // Explanation toggle (#804)
  if (data.explanation && data.explanation.length) {
    const showExplain = state.showExplanation !== false;
    html += `<div class="card" style="margin-bottom:1rem">
      <button id="explain-toggle" class="retry-btn" style="margin-bottom:0.5rem" onclick="toggleExplanation()">
        ${showExplain ? 'Hide' : 'Show'} chunk explanation (${data.explanation.length} chunks)
      </button>
      <div id="explain-panel" style="${showExplain ? '' : 'display:none'}">`;
    data.explanation.forEach(e => {
      html += `<div class="result-item" style="margin-bottom:0.4rem;padding:0.5rem 0.75rem">
        <div class="source">${escapeHtml(e.source_file || 'unknown')} [${e.chunk_index || 0}]</div>
        <div class="score">Score: ${(e.score * 100).toFixed(1)}% ${e.included ? '✓ included' : '✗ excluded'}</div>
        ${e.text ? `<div class="preview" style="font-size:0.8rem">${escapeHtml(e.text).slice(0, 300)}</div>` : ''}
      </div>`;
    });
    html += '</div></div>';
  }

  if (data.sources && data.sources.length) {
    html += `<div class="card"><h3>Sources</h3><ul style="color:#58a6ff;font-size:0.85rem">`;
    data.sources.forEach(s => { html += `<li>${escapeHtml(s)}</li>`; });
    html += '</ul></div>';
  }

  container.innerHTML = html;
}

function toggleExplanation() {
  state.showExplanation = !state.showExplanation;
  const panel = document.getElementById('explain-panel');
  const btn = document.getElementById('explain-toggle');
  if (panel) panel.style.display = state.showExplanation ? '' : 'none';
  if (btn) btn.textContent = (state.showExplanation ? 'Hide' : 'Show') + ' chunk explanation';
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

// ── Review Queue (promoted from dashboard.html) ──────────────────────────────
async function loadReview() {
  const container = document.getElementById('review-list');
  const statsContainer = document.getElementById('review-stats');
  renderLoading(container, 'Loading review queue…');
  renderLoading(statsContainer, '');

  try {
    const [stats, entries] = await Promise.all([
      apiJSON('/v1/review/stats'),
      apiJSON('/v1/review'),
    ]);
    state.reviewStats = stats;
    state.reviewEntries = entries.entries || [];

    renderReviewStats(stats);
    renderReviewList(state.reviewEntries);
  } catch (err) {
    renderError(container, `Review queue failed: ${err.message}`, loadReview);
    if (statsContainer) statsContainer.innerHTML = '';
  }
}

function renderReviewStats(stats) {
  const container = document.getElementById('review-stats');
  if (!stats || !stats.total_needs_review) {
    renderEmpty(container, 'No items pending review', 'All facts are confirmed or resolved.');
    return;
  }
  let html = '<div class="card-grid" style="margin-bottom:1rem">';
  html += `<div class="card"><h3>Pending</h3><div class="value">${stats.total_needs_review}</div></div>`;
  if (stats.by_reason) {
    for (const [reason, count] of Object.entries(stats.by_reason)) {
      html += `<div class="card"><h3>${reason}</h3><div class="value">${count}</div></div>`;
    }
  }
  if (stats.avg_pending_days) {
    html += `<div class="card"><h3>Avg Age</h3><div class="value">${stats.avg_pending_days.toFixed(1)}d</div></div>`;
  }
  html += '</div>';
  container.innerHTML = html;
}

function renderReviewList(entries) {
  const container = document.getElementById('review-list');
  if (!entries.length) {
    renderEmpty(container, 'No review items', 'All facts are in good standing.');
    return;
  }
  let html = '';
  entries.forEach(e => {
    const reasons = (e.review_reasons || []).map(r => r.type || r).join(', ');
    html += `<div class="card result-item">
      <div class="source">${escapeHtml(e.key)}</div>
      <div class="score">Confidence: ${(e.confidence * 100).toFixed(0)}%</div>
      <div class="preview">${escapeHtml(e.value || '').slice(0, 200)}</div>
      ${reasons ? `<div class="links">Flagged: ${escapeHtml(reasons)}</div>` : ''}
    </div>`;
  });
  container.innerHTML = html;
}

// ── Facts Manager ────────────────────────────────────────────────────────────
async function loadFacts() {
  const container = document.getElementById('facts-list');
  renderLoading(container, 'Loading facts…');

  try {
    const vault = document.getElementById('facts-vault').value;
    const url = vault ? `/vault/${vault}/v1/facts?limit=50` : '/v1/facts?limit=50';
    const data = await apiJSON(url);
    const entries = data.entries || [];
    if (!entries.length) {
      renderEmpty(container, 'No facts found', 'Create a fact via POST /v1/facts or from an agent session.');
      return;
    }
    let html = '';
    entries.forEach(e => {
      html += `<div class="card result-item" style="cursor:pointer" onclick="showFactDetail('${escapeHtml(e.key)}')">
        <div class="source">${escapeHtml(e.key)}</div>
        <div class="score">${(e.confidence * 100).toFixed(0)}% · ${e.status || 'active'}</div>
        <div class="preview">${escapeHtml(e.value || '').slice(0, 300)}</div>
        ${e.source ? `<div class="links">Source: ${escapeHtml(e.source)}</div>` : ''}
      </div>`;
    });
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Facts failed: ${err.message}`, loadFacts);
  }
}

async function showFactDetail(key) {
  const container = document.getElementById('fact-detail');
  renderLoading(container, 'Loading fact detail…');
  try {
    const [detail, provenance] = await Promise.all([
      apiJSON(`/v1/facts?key=${encodeURIComponent(key)}`),
      apiJSON(`/v1/facts/${encodeURIComponent(key)}/provenance`).catch(() => null),
    ]);
    const entry = detail.entries && detail.entries[0];
    if (!entry) {
      renderEmpty(container, 'Fact not found');
      return;
    }
    let html = '<div class="card"><h3 style="color:#58a6ff">' + escapeHtml(entry.key) + '</h3>';
    html += '<p>' + escapeHtml(entry.value) + '</p>';
    html += '<p style="color:#8b949e;font-size:0.85rem">Confidence: ' + (entry.confidence * 100).toFixed(0) + '% | Status: ' + (entry.status || 'active') + ' | Version: ' + (entry.version || 0) + '</p>';
    if (entry.source) html += '<p style="color:#8b949e;font-size:0.85rem">Source: ' + escapeHtml(entry.source) + '</p>';
    if (entry.created_at) html += '<p style="color:#8b949e;font-size:0.85rem">Created: ' + escapeHtml(entry.created_at) + '</p>';
    if (provenance && provenance.source) {
      html += '<p style="color:#8b949e;font-size:0.85rem">Lineage: ' + escapeHtml(provenance.source) + ' (' + escapeHtml(provenance.source_type || 'manual') + ')</p>';
    }
    if (entry.tags && entry.tags.length) {
      html += '<p style="color:#8b949e;font-size:0.85rem">Tags: ' + entry.tags.map(t => '<span class="mini-tag">' + escapeHtml(t) + '</span>').join(' ') + '</p>';
    }
    html += '</div>';
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Fact detail failed: ${err.message}`, () => showFactDetail(key));
  }
}

// ── Knowledge Debt ───────────────────────────────────────────────────────────
async function loadDebt() {
  const container = document.getElementById('debt-content');
  renderLoading(container, 'Assessing knowledge debt…');

  try {
    const data = await apiJSON('/v1/debt');
    let html = '';

    html += `<div class="card"><h3>Vaults</h3><div class="value">${data.vault_count || 0}</div><div class="sub">configured</div></div>`;
    html += `<div class="card"><h3>Files</h3><div class="value">${data.total_files || 0}</div><div class="sub">indexed</div></div>`;
    html += `<div class="card"><h3>Chunks</h3><div class="value">${data.total_chunks || 0}</div><div class="sub">total</div></div>`;
    html += `<div class="card"><h3>Pending Review</h3><div class="value">${data.review_queue_size || 0}</div><div class="sub">items</div></div>`;

    if (data.review_by_reason) {
      for (const [reason, count] of Object.entries(data.review_by_reason)) {
        html += `<div class="card"><h3>${reason}</h3><div class="value">${count}</div><div class="sub">flagged</div></div>`;
      }
    }

    if (data.pruner) {
      html += `<div class="card"><h3>Pruner</h3><div class="value">${data.pruner.enabled ? 'On' : 'Off'}</div><div class="sub">${data.pruner.enabled ? (data.pruner.last_scan || 'active') : 'disabled'}</div></div>`;
    }

    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Debt assessment failed: ${err.message}`, loadDebt);
  }
}

// ── Knowledge Gaps ───────────────────────────────────────────────────────────
async function loadGaps() {
  const container = document.getElementById('gaps-content');
  renderLoading(container, 'Analyzing coverage…');

  try {
    const data = await apiJSON('/v1/gaps');
    const gaps = data.poorly_covered || [];
    if (!gaps.length) {
      renderEmpty(container, 'No coverage gaps detected', 'All vaults have adequate coverage.');
      return;
    }
    let html = '<div class="card-grid">';
    gaps.forEach(g => {
      html += `<div class="card"><h3>${escapeHtml(g.vault)}</h3><div class="value">${g.files}</div><div class="sub">files · ${g.chunks} chunks · severity: ${g.severity}</div></div>`;
    });
    html += '</div>';
    if (data.recommendations && data.recommendations.length) {
      html += '<div class="card" style="margin-top:1rem"><h3>Recommendations</h3><ul>';
      data.recommendations.forEach(r => { html += '<li>' + escapeHtml(r) + '</li>'; });
      html += '</ul></div>';
    }
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Gap analysis failed: ${err.message}`, loadGaps);
  }
}

// ── Agent Heatmap ────────────────────────────────────────────────────────────
async function loadAgents() {
  const container = document.getElementById('agents-content');
  renderLoading(container, 'Loading agent stats…');

  try {
    const data = await apiJSON('/v1/agents/stats');
    const agents = data.agents || [];
    if (!agents.length) {
      renderEmpty(container, 'No agent data', 'No agents have written facts yet.');
      return;
    }
    let html = '<div class="card-grid">';
    agents.forEach(a => {
      html += `<div class="card"><h3>${escapeHtml(a.agent)}</h3>
        <div class="value">${a.facts_written}</div><div class="sub">facts written</div>
        <div style="color:#8b949e;font-size:0.85rem">Avg confidence: ${(a.avg_confidence * 100).toFixed(0)}%</div>
        <div style="color:#8b949e;font-size:0.85rem">Flag rate: ${a.flag_rate_pct.toFixed(1)}%</div></div>`;
    });
    html += '</div>';
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Agent stats failed: ${err.message}`, loadAgents);
  }
}

// ── Ingest Log ───────────────────────────────────────────────────────────────
async function loadIngest() {
  const container = document.getElementById('ingest-list');
  renderLoading(container, 'Loading ingest log…');

  try {
    const vault = document.getElementById('ingest-vault').value;
    const url = vault ? `/vault/${vault}/v1/logs?limit=50` : '/v1/logs?limit=50';
    const data = await apiJSON(url);
    const entries = data.entries || [];
    if (!entries.length) {
      renderEmpty(container, 'No log entries', 'Ingest activity will appear here as agents interact.');
      return;
    }
    let html = '';
    entries.forEach(e => {
      html += `<div class="card result-item">
        <div class="source">${escapeHtml(e.agent || 'system')}</div>
        <div class="score">${e.type || 'info'}</div>
        <div class="preview">${escapeHtml(e.body || '').slice(0, 300)}</div>
        <div class="links">${escapeHtml(e.timestamp || '')}</div>
      </div>`;
    });
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Ingest log failed: ${err.message}`, loadIngest);
  }
}

// ── Vault Admin ──────────────────────────────────────────────────────────────
async function loadVaultAdmin() {
  const container = document.getElementById('vaultadmin-content');
  renderLoading(container, 'Loading vault info…');

  try {
    const data = await apiJSON('/vaults');
    const vaults = data.vaults || [];
    if (!vaults.length) {
      renderEmpty(container, 'No vaults configured', 'Create a vault to get started.');
      return;
    }
    let html = '<div class="card-grid">';
    vaults.forEach(v => {
      html += `<div class="card"><h3>${escapeHtml(v.name)}</h3>
        <div class="value">${v.indexed_files || 0}</div><div class="sub">files · ${v.total_chunks || 0} chunks</div>
        <div style="color:#8b949e;font-size:0.85rem">Path: ${escapeHtml(v.path || '')}</div>
        <div style="color:#8b949e;font-size:0.85rem">${v.last_indexed ? 'Last indexed: ' + escapeHtml(v.last_indexed) : 'Not indexed yet'}</div>
        ${v.indexing ? '<div style="color:#7ee787">Indexing in progress…</div>' : ''}
      </div>`;
    });
    html += '</div>';
    container.innerHTML = html;
  } catch (err) {
    renderError(container, `Vault admin failed: ${err.message}`, loadVaultAdmin);
  }
}
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
