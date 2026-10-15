// PAI browser workspace. No framework or build step: plain DOM plus SSE.
//
// Sections: state → model controls → run status → response parsing →
// API/DOM helpers → rendering → history → snapshot/prompts → activity feed →
// session actions → event wiring.
'use strict';
const $ = id => document.getElementById(id);
let selected = '', generation = 0, stream = null, retry = null, refreshTimer = null, sessionBusy = false;
let sessionOffset = 0, historyOffset = 0, historyTotal = 0, historyRequest = 0, refreshing = false, refreshAgain = false;
let pendingID = null, pendingSession = null;
let defaultModel = '', savedModel = '', liveModel = '', modelDirty = false, modelBlocked = true, modelApplying = false;
let savedReasoning = '', reasoningDirty = false, reasoningApplying = false;
let modelList = [];
// The browser remembers the open session in localStorage, never a cookie: it is
// only needed after the page loads, so it need not travel on every request, and
// it needs no server state. A cleared store just leaves the workspace empty.
const SESSION_KEY = 'pai.session';
// The one-click instruction the Apply plan button sends. It is deliberately a
// plain sentence: the model reads it like any other user turn.
const AUTO_REPLY = 'Apply your plan.';
let trustAllApplying = false;
let toastHost = null;
let savedRole = '', roleDirty = false;
let savedCwd = '', cwdDirty = false, cwdApplying = false;
const THEME_KEY = 'pai.theme';
const REASONING_POS_KEY = 'pai.reasoning-position';
// ── Model controls ──────────────────────────────────────────
function normalizeModel(value) { return value.trim().replace(/\s*:\s*/, ':'); }
function modelControls() {
  const disabled = !selected || modelBlocked || modelApplying;
  $('session-model').disabled = disabled;
  $('apply-model').disabled = disabled || !$('session-model').value.trim() || normalizeModel($('session-model').value) === (liveModel || savedModel || defaultModel);
}
function modelMetadata(model) {
  savedModel = typeof model === 'string' ? model.trim() : '';
  if (!modelDirty) $('session-model').value = liveModel || savedModel || defaultModel;
  modelControls();
}
async function models() {
  const data = await api('models');
  defaultModel = data.default_model || '';
  modelList = Array.isArray(data.models) ? data.models.slice() : [];
  renderModelOptions('');
  if (!modelDirty) $('session-model').value = liveModel || savedModel || defaultModel;
  modelControls();
}
// A datalist filters its suggestions by the field's current value, which hides
// the other presets whenever a model is already set. Render our own dropdown so
// every suggestion is visible on focus, while the field still accepts free text.
function renderModelOptions(filter) {
  const list = $('model-options'), input = $('session-model');
  const query = String(filter || '').trim().toLowerCase();
  const matches = modelList.filter(model => !query || model.toLowerCase().includes(query));
  list.replaceChildren(...matches.map(model => {
    const option = text('li', model, 'model-option');
    option.setAttribute('role', 'option');
    // Mousedown (not click) so preventDefault keeps the input from blurring first.
    option.onmousedown = event => { event.preventDefault(); chooseModel(model); };
    return option;
  }));
  const open = matches.length > 0 && !input.disabled;
  list.hidden = !open;
  input.setAttribute('aria-expanded', String(open));
}
function chooseModel(value) {
  $('session-model').value = value;
  modelDirty = true;
  closeModelOptions();
  modelControls();
}
function closeModelOptions() {
  $('model-options').hidden = true;
  $('session-model').setAttribute('aria-expanded', 'false');
}
async function applyModel(e) {
  e.preventDefault(); if ($('apply-model').disabled) return;
  const name = selected, model = normalizeModel($('session-model').value);
    let version = generation;
  modelApplying = true; modelControls(); $('error').textContent = '';
  try {
    await api('model', {name, model});
    if (version !== generation) return;
    // Retirement closes SSE; invalidate its callbacks and any pre-switch history.
    disconnect(); version = ++generation; historyRequest++;
    liveModel = ''; modelDirty = false; modelMetadata(model);
    await history(true); if (version !== generation) return;
        await loadSnapshot(); if (version === generation) connect();
  } catch (err) { if (name === selected && version === generation) { error(err); await loadSnapshot(); } }
  finally { modelApplying = false; modelControls(); }
}
function workingDirControls() {
  const input = $('session-working-dir');
  const disabled = !selected || modelBlocked || cwdApplying;
  input.disabled = disabled;
  $('apply-working-dir').disabled = disabled || !input.value.trim() || input.value.trim() === savedCwd;
}
async function applyWorkingDir(e) {
  e.preventDefault(); if ($('apply-working-dir').disabled) return;
  const name = selected, cwd = $('session-working-dir').value.trim();
  let version = generation;
  cwdApplying = true; workingDirControls(); $('error').textContent = '';
  try {
    await api('working-dir', {name, working_dir:cwd});
    if (version !== generation) return;
    // Retire the old subscription too: its workspace and pending callbacks
    // belong to the runtime that was just stopped by this settings change.
    disconnect(); version = ++generation; historyRequest++;
    cwdDirty = false;
    await history(true); if (version !== generation) return;
    await loadSnapshot(); if (version !== generation) return;
    connect();
    await sessions(true);
  } catch (err) { if (name === selected && version === generation) { error(err); await loadSnapshot(); } }
  finally { cwdApplying = false; workingDirControls(); }
}
// ── Run status, live transcript, response parsing ───────────
let durable = [], liveSteps = [], currentStep = null, thinking = '';
let liveCard = null, liveType = '', liveText = '';
const liveTextLimit = 16000;
// A confirming card's choices share one aligned row.
const activityLimit = 60;
// Cap the in-memory conversation window (older turns reload from the store).
const historyLimit = 200;
let runTimer = null, runStarted = 0, runState = '', runLabel = '';
function paintRunProgress() {
  const seconds = Math.max(0, Math.floor((Date.now() - runStarted) / 1000));
  const elapsed = seconds < 60 ? seconds + 's' : Math.floor(seconds / 60) + 'm ' + (seconds % 60) + 's';
  $('run-elapsed').textContent = runLabel + ' · ' + elapsed + ' (this browser)';
}
function stopRunProgress() {
  if (runTimer !== null) clearInterval(runTimer);
  runTimer = null; runState = ''; runLabel = ''; runStarted = 0;
  $('run-progress').hidden = true; $('run-elapsed').textContent = '';
}
function runPhase(label, reset = false) {
  if (runTimer === null || reset || label !== runLabel) runStarted = Date.now();
  runLabel = label;
  $('run-progress').hidden = false;
  $('run-progress').firstElementChild.hidden = false;
  paintRunProgress();
  if (runTimer === null) runTimer = setInterval(paintRunProgress, 1000);
}
function statusText(value) { return String(value || '').replace(/[\x00-\x1f\x7f]/g, ' ').replace(/\s+/g, ' ').trim(); }
function phaseLabel(s) {
  if (s.pending) return s.pending.kind === 'confirm' ? 'Waiting for approval' : 'Waiting for answer';
  if (s.phase === 'tool' && s.active_tool) {
    const tool = s.active_tool;
    return 'Running ' + statusText(tool.Name || 'tool') + (tool.Target ? ' · ' + statusText(tool.Target) : '');
  }
  return ({starting:'Starting…', waiting_model:'Waiting for model response', reasoning:'Receiving reasoning', tool:'Running tool', waiting_approval:'Waiting for approval', waiting_question:'Waiting for answer', awaiting:'Awaiting instruction', stopped:'Stopped'})[s.phase || s.state] || 'Waiting for model response';
}
function runSnapshot(s) {
  if (s.pending) {
    stopRunProgress();
    $('run-progress').hidden = false;
    $('run-progress').firstElementChild.hidden = true;
    $('run-elapsed').textContent = s.pending.kind === 'confirm' ? 'Waiting for approval' : 'Waiting for answer';
    return;
  }
  if (!['busy','starting'].includes(s.state) || ['awaiting','stopped'].includes(s.phase)) { stopRunProgress(); return; }
  if (s.phase) runPhase(phaseLabel(s));
  else if (runState !== s.state || runTimer === null) runPhase(s.state === 'starting' ? 'Starting…' : 'Thinking…', true);
  runState = s.state;
}
function activityScroll() { $('events').scrollTop = $('events').scrollHeight; }
function scrollBottom() {
  const scroller = $('conversation');
  scroller.scrollTop = scroller.scrollHeight;
  updateJumpButtons();
}
// Two floating jump buttons ride over the timeline: ↑ to the user turn just above
// the current position (then the one before it, and so on), ↓ to the newest
// message. Each is shown only when it would do something.
function userCards() { return [...$('history').children].filter(node => typeof node.className === 'string' && node.className.includes('turn user')); }
function atConversationBottom() {
  const scroller = $('conversation');
  return scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop <= 80;
}
function updateJumpButtons() {
  $('prev-input').hidden = userCards().length === 0;
  $('to-bottom').hidden = atConversationBottom();
}
function jumpToPrevInput() {
  const scroller = $('conversation');
  const cards = userCards();
  const above = cards.filter(card => (card.offsetTop || 0) < scroller.scrollTop - 4);
  const target = above.length ? above[above.length - 1] : cards[0];
  if (target && typeof target.scrollIntoView === 'function') target.scrollIntoView({block: 'start'});
  updateJumpButtons();
}
function jumpToBottom() {
  const scroller = $('conversation');
  scroller.scrollTop = scroller.scrollHeight;
  updateJumpButtons();
}
function logDetails(node, title, value, open = false) {
  if (!value) return;
  const details = text('details', '', 'tool-log'); details.open = open;
  details.append(text('summary', title), text('pre', value)); node.append(details);
}
function parsedResponse(content) {
  try { const value = JSON.parse(content); if (value && typeof value.action === 'string') return value; } catch (_) {}
  // Accepted model responses can be wrapped in prose or a code fence.
  for (let start = content.indexOf('{'); start >= 0; start = content.indexOf('{', start + 1)) {
    let depth = 0, quoted = false, escaped = false;
    for (let i = start; i < content.length; i++) {
      const c = content[i];
      if (quoted) { if (escaped) escaped = false; else if (c === '\\') escaped = true; else if (c === '"') quoted = false; }
      else if (c === '"') quoted = true;
      else if (c === '{') depth++;
      else if (c === '}' && --depth === 0) { try { const value = JSON.parse(content.slice(start, i + 1)); if (['tool','done','ask','terminate'].includes(value.action)) return value; } catch (_) {} break; }
    }
  }
  return null;
}
// ── API and small DOM helpers ───────────────────────────────
function clearPending() {
  const node = $('pending');
  node.replaceChildren();
  // A dragged card keeps its offset, so re-center before the next prompt.
  node.style.left = ''; node.style.top = ''; node.style.bottom = ''; node.style.transform = '';
  node.classList.remove('dragging');
  pendingID = null; pendingSession = null;
}
// A confirmation card floats over the conversation; let the user drag it by its
// handle out of the way. It stays clamped inside its parent, and its position
// resets whenever the prompt changes. Bound once (by delegation) so it survives
// the card being rebuilt for each prompt.
function freezeAt(el, left, top) {
  el.style.transform = 'none';
  el.style.bottom = '';
  el.style.left = left + 'px';
  el.style.top = top + 'px';
}
function savePosition(key, el) {
  try { localStorage.setItem(key, JSON.stringify({left: parseFloat(el.style.left) || 0, top: parseFloat(el.style.top) || 0})); } catch (_) {}
}
// Restore a remembered drag offset so the card reappears where the user left it.
function restorePosition(el, key) {
  let position = null;
  try { position = JSON.parse(localStorage.getItem(key) || 'null'); } catch (_) {}
  if (position && typeof position.left === 'number' && typeof position.top === 'number') freezeAt(el, position.left, position.top);
}
function initDraggable(el, handle, key) {
  if (!el || typeof el.addEventListener !== 'function') return;
  let drag = null;
  const clamp = (value, max) => Math.min(Math.max(0, Math.min(max, value)), Math.max(0, value));
  el.addEventListener('pointerdown', event => {
    if (event.button !== 0 || !event.target) return;
    if (handle && typeof event.target.matches === 'function' && !event.target.matches(handle)) return;
    const frame = el.parentElement && el.parentElement.getBoundingClientRect();
    if (!frame) return;
    const card = el.getBoundingClientRect();
    const left = card.left - frame.left, top = card.top - frame.top;
    freezeAt(el, left, top);
    drag = {id: event.pointerId, x: event.clientX, y: event.clientY, left, top, width: card.width, height: card.height};
    el.classList.add('dragging');
    try { el.setPointerCapture(event.pointerId); } catch (_) {}
    event.preventDefault();
  });
  el.addEventListener('pointermove', event => {
    if (!drag || event.pointerId !== drag.id) return;
    const frame = el.parentElement.getBoundingClientRect();
    freezeAt(el, clamp(drag.left + event.clientX - drag.x, frame.width - drag.width), clamp(drag.top + event.clientY - drag.y, frame.height - drag.height));
  });
  const end = event => {
    if (!drag || event.pointerId !== drag.id) return;
    drag = null;
    el.classList.remove('dragging');
    try { el.releasePointerCapture(event.pointerId); } catch (_) {}
    if (key) savePosition(key, el);
  };
  el.addEventListener('pointerup', end);
  el.addEventListener('pointercancel', end);
}
function initPendingDrag() { initDraggable($('pending'), '#pending-title'); }
// The reasoning card keeps its position across reasoning sessions, so it can be
// parked out of the way once and stays there (invisible between sessions).
function initReasoningDrag() { initDraggable($('reasoning-preview'), '#reasoning-preview h3', REASONING_POS_KEY); }
function showReasoning() { restorePosition($('reasoning-preview'), REASONING_POS_KEY); $('reasoning-preview').hidden = false; }
const text = (tag, value, className) => { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; };
function actionRow(buttons) { const row = text('div', '', 'pending-actions'); row.append(...buttons); return row; }
// Errors and notices share one top-center stack, so a transient message always
// looks and behaves the same wherever it originates. Each card self-dismisses on
// a timer (or on click); at most three are kept so a burst cannot bury the
// workspace.
function notify(message) {
  const host = $('error');
  if (!host) return;
  toastHost = host;
  const node = text('div', message, 'toast');
  node.onclick = () => node.remove();
  host.append(node);
  while (host.children.length > 3) host.firstElementChild.remove();
  setTimeout(() => node.remove(), 8000);
}
function error(e) { notify(e.message || String(e)); }
function toast(message) { notify(message); }
function showApp() { $('login').hidden = true; $('app').hidden = false; }
function loginNeeded() { $('login').hidden = false; $('app').hidden = true; disconnect(); }
// The workspace shell starts hidden (see index.html), so a signed-out load never
// flashes it before the first request is answered. A 401 needs the login form; any
// other failure reveals the shell, so the user keeps the UI and the error toast
// rather than a blank page.
function bootFailed(e) {
  if (e && e.status === 401) { loginNeeded(); return; }
  if ($('app').hidden) showApp();
  error(e);
}
// ── Theme ───────────────────────────────────────────────────
// Light is the default; the switch sets data-theme on <html> and remembers the
// choice. The system preference only seeds the first visit.
function initialTheme() {
  try { const saved = localStorage.getItem(THEME_KEY); if (saved === 'dark' || saved === 'light') return saved; } catch (_) {}
  try { return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'; } catch (_) { return 'light'; }
}
function applyTheme(theme) {
  if (document.documentElement && document.documentElement.dataset) document.documentElement.dataset.theme = theme;
  const button = $('theme-toggle');
  if (button) { button.textContent = theme === 'dark' ? '☀' : '🌙'; button.title = 'Switch to ' + (theme === 'dark' ? 'light' : 'dark') + ' theme'; }
}
function toggleTheme() {
  const current = document.documentElement && document.documentElement.dataset ? document.documentElement.dataset.theme : 'light';
  const next = current === 'dark' ? 'light' : 'dark';
  try { localStorage.setItem(THEME_KEY, next); } catch (_) {}
  applyTheme(next);
}
async function api(path, body) {
  const options = body === undefined ? {} : { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) };
  const response = await fetch('/api/' + path, options);
  const data = await response.json();
  if (!response.ok) { if (response.status === 401) loginNeeded(); const e = new Error(data.error || response.statusText); e.status = response.status; throw e; }
  return data;
}
function query(endpoint, offset, limit = 100) { return endpoint + '?name=' + encodeURIComponent(selected) + '&offset=' + offset + '&limit=' + limit; }
async function sessions(reset = false) {
  if (reset) { sessionOffset = 0; $('sessions').replaceChildren(); }
  $('sessions-status').textContent = 'Loading sessions…';
  let page;
  try { page = await api('sessions?offset=' + sessionOffset + '&limit=100'); }
  catch (e) { $('sessions-status').textContent = 'Could not load sessions. Please retry or reload.'; throw e; }
  for (const meta of page.sessions || []) {
    const row = text('div', '', 'session-row');
    row.dataset.name = meta.name;
    row.classList.toggle('selected', meta.name === selected);
    const button = text('button', meta.name, 'session-open');
    button.type = 'button';
    button.setAttribute('aria-current', meta.name === selected ? 'page' : 'false');
    button.onclick = () => select(meta.name, meta.cwd);
    const remove = text('button', '🗑', 'session-delete');
    remove.type = 'button';
    remove.title = 'Delete this session';
    remove.setAttribute('aria-label', 'Delete session ' + meta.name);
    remove.onclick = event => { event.stopPropagation(); return deleteSession(meta.name); };
    row.append(button, remove);
    $('sessions').append(row);
  }
  sessionOffset += (page.sessions || []).length; $('more-sessions').hidden = sessionOffset >= page.total;
  $('sessions-status').textContent = sessionOffset ? '' : 'No saved sessions yet. Create a named session above.';
  return page.sessions || [];
}

function controls() {
  const ready = !!selected, filled = !!$('message').value.trim();
  $('send').disabled = !ready || !filled;
  // Steer only redirects a running task; while idle it would just be a Send.
  $('steer').disabled = !ready || !filled || !sessionBusy;
  $('cancel').disabled = !ready;
  // Auto-reply needs only a session: it sends a canned instruction, not the draft.
  $('apply-plan').disabled = !ready;
  trustAllControls();
  reasoningControls();
  $('rename-session').hidden = !ready;
  $('rename-session').disabled = !ready || sessionBusy;
  $('session-label').hidden = !ready;
  // Changing session settings requires an idle worker (it retires the runtime).
  const configReady = ready && !modelBlocked;
  const roleSelect = $('session-role-select');
  roleSelect.disabled = !configReady;
  $('apply-role').disabled = !configReady || !roleSelect.value || roleSelect.value === savedRole;
  workingDirControls();
  $('composer-hint').textContent = !ready
    ? 'Open a session to send instructions'
    : sessionBusy
      ? 'Enter to send · Shift+Enter for newline · While busy: Sends queue, Steer redirects, Cancel stops'
      : 'Enter to send · Shift+Enter for newline';
}
// ── Trust-all switch ────────────────────────────────────────
// A per-session auto-approval toggle. The server is the authority (it keeps the
// flag across worker restarts between instructions); the switch mirrors it. While
// on, no confirmation card appears — every command and file change just runs.
function trustAllControls() {
  $('trust-all').disabled = !selected || trustAllApplying;
}
async function loadTrustAll() {
  const version = generation, name = selected;
  if (!name) { $('trust-all').checked = false; return; }
  try {
    const data = await api('trust-all?name=' + encodeURIComponent(name));
    if (version !== generation || selected !== name) return;
    $('trust-all').checked = !!data.trust_all;
  } catch (e) { if (version === generation && selected === name) $('trust-all').checked = false; }
}
async function setTrustAll(on) {
  if (!selected) { $('trust-all').checked = false; return; }
  const name = selected;
  trustAllApplying = true; trustAllControls();
  try {
    await api('trust-all', {name, trust_all: on});
    if (selected === name) { $('trust-all').checked = on; if (on) await loadSnapshot(); }
    if (on) toast('Trusting every command and file change for this session');
  } catch (e) { error(e); $('trust-all').checked = !on; }
  finally { trustAllApplying = false; trustAllControls(); }
}
// ── Reasoning effort (per-session, in-memory) ────────────────────
// Changing it retires the idle runtime, like the model: the override lives on the
// server for the session (no persistence) and applies to the next run.
function reasoningControls() {
  const select = $('reasoning-select');
  const disabled = !selected || modelBlocked || reasoningApplying;
  select.disabled = disabled;
  $('apply-reasoning').disabled = disabled || !select.value || select.value === savedReasoning;
}
function reasoningMetadata(value) {
  savedReasoning = typeof value === 'string' ? value.trim() : '';
  if (!reasoningDirty) $('reasoning-select').value = savedReasoning || 'none';
  reasoningControls();
}
async function loadReasoning() {
  const version = generation, name = selected;
  try {
    const data = await api('reasoning' + (name ? '?name=' + encodeURIComponent(name) : ''));
    if (version !== generation || selected !== name) return;
    const select = $('reasoning-select');
    const options = Array.isArray(data.options) && data.options.length ? data.options : ['none','low','medium','high'];
    if (!select.children.length) select.replaceChildren(...options.map(value => { const option = text('option', value); option.value = value; return option; }));
    reasoningMetadata(data.reasoning);
  } catch (_) { if (version === generation && selected === name) reasoningControls(); }
}
async function applyReasoning(e) {
  e.preventDefault();
  if ($('apply-reasoning').disabled) return;
  const name = selected, effort = $('reasoning-select').value;
  let version = generation;
  reasoningApplying = true; reasoningControls(); $('error').textContent = '';
  try {
    await api('reasoning', {name, reasoning: effort});
    if (version !== generation) return;
    // Retiring the runtime closes SSE; reconnect under a new generation.
    disconnect(); version = ++generation; historyRequest++;
    reasoningDirty = false; reasoningMetadata(effort);
    await loadSnapshot(); if (version === generation) connect();
  } catch (err) { if (name === selected && version === generation) { error(err); await loadSnapshot(); } }
  finally { reasoningApplying = false; reasoningControls(); }
}
// ── Rendering ───────────────────────────────────────────────
function diff(value) {
  const pre = text('pre', '', 'diff');
  const lines = String(value).split('\n');
  lines.forEach((line, index) => pre.append(text('span', line + (index < lines.length - 1 ? '\n' : ''), 'diff-line ' + (line.startsWith('+') ? 'diff-add' : line.startsWith('-') ? 'diff-remove' : ''))));
  return pre;
}
function prose(value) {
  const node = text('div', '', 'prose');
  String(value).split(/(```[\s\S]*?```)/g).forEach(part => {
    if (part.startsWith('```')) node.append(text('pre', part.slice(3, -3).replace(/^[\w+-]*\n/, '')));
    else node.append(text('span', part));
  });
  return node;
}
// Bound parser work; oversized or unavailable-library replies remain literal.
const markdownLimit = 100000;
function safeMarkdownURL(value) {
  if (!/^https?:\/\//i.test(value) || /[\s\u0000-\u001f\u007f-\u009f]/u.test(value)) return false;
  try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) && !!url.hostname; } catch { return false; }
}
function completeProse(value) {
  value = String(value);
  const literal = () => text('div', value, 'prose');
  if (value.length > markdownLimit || !globalThis.marked?.parse || !globalThis.DOMPurify?.isSupported) return literal();
  try {
    // Disable raw HTML and images before parsing into a DOM, preventing even
    // detached image elements from initiating model-controlled network requests.
    const escape = raw => raw.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;');
    const renderer = new marked.Renderer();
    renderer.html = token => escape(token.text);
    renderer.image = token => escape(token.text);
    const html = marked.parse(value, {async:false, gfm:true, renderer});
    const fragment = DOMPurify.sanitize(html, {
      RETURN_DOM_FRAGMENT:true,
      ALLOWED_TAGS:['p','br','hr','h1','h2','h3','h4','h5','h6','blockquote','ul','ol','li','pre','code','em','strong','del','table','thead','tbody','tr','th','td','a'],
      ALLOWED_ATTR:['href','title','start'],
      ALLOW_DATA_ATTR:false, ALLOW_ARIA_ATTR:false,
      ALLOWED_URI_REGEXP:/^https?:\/\//i
    });
    for (const link of fragment.querySelectorAll('a')) {
      const href = link.getAttribute('href');
      if (!href || !safeMarkdownURL(href)) link.removeAttribute('href');
      else { link.setAttribute('target', '_blank'); link.setAttribute('rel', 'noopener noreferrer'); }
    }
    const node = text('div', '', 'markdown');
    node.append(fragment);
    return node;
  } catch { return literal(); }
}
const icons = {execute:'💻', remote:'🔐', read:'📖', write:'✍️', edit:'📝', websearch:'🔎', papersearch:'🧑‍🎓'};
function payload(node, value, limit = Infinity) {
  if (typeof value === 'string') { node.append(text('pre', value.slice(0, limit))); return; }
  if (!value || typeof value !== 'object') { if (value !== undefined) node.append(prose(String(value))); return; }
  const known = ['path','host','cmd','source','query','sort','match','content','old_string','new_string','year_from','year_to','offset','limit','replace_all'];
  for (const key of known) {
    if (value[key] === undefined) continue;
    node.append(text('div', key === 'cmd' ? 'CMD' : key.replaceAll('_', ' '), 'field-label'));
    if (key === 'old_string' || key === 'new_string') node.append(diff(String(value[key]).slice(0, limit).split('\n').map(line => (key === 'old_string' ? '-' : '+') + line).join('\n')));
    else if (key === 'content') logDetails(node, 'Content', String(value[key]).slice(0, limit));
        else node.append(text('pre', String(value[key]).slice(0, limit)));
  }
  const extra = Object.fromEntries(Object.entries(value).filter(([key]) => !known.includes(key)));
  if (Object.keys(extra).length) rawDetails(node, extra, limit);
}
function rawDetails(node, value, limit = Infinity) {
  const details = text('details', ''); details.append(text('summary', 'Raw details'), text('pre', JSON.stringify(value, null, 2).slice(0, limit))); node.append(details);
}
function response(node, data) {
  const tool = data.action === 'tool';
  node.append(text('div', (tool ? (icons[data.toolname] || '📃') + ' ' + (data.toolname || 'Tool') : ({done:'✅ Complete',ask:'🙋 Question',terminate:'🤚 Stopped'}[data.action] || data.action)), data.action === 'done' ? 'badge complete' : 'badge'));
  if (data.reason) {
    // The reason is model prose like the payload, so it gets the same sanitized
    // Markdown rendering (and the same literal fallback) rather than plain text.
    const reason = completeProse(data.reason); reason.className += ' reason-text';
    node.append(text('div', 'Reason', 'field-label'), reason, text('hr', '', 'response-divider'));
  }
  if (tool) { node.className += ' tool-card'; if (typeof data.payload === 'string') node.append(text('div', data.toolname === 'execute' ? 'CMD' : 'Query', 'field-label')); payload(node, data.payload); }
  else if (typeof data.payload === 'string') node.append(data.action === 'done' ? completeProse(data.payload) : prose(data.payload));
  else payload(node, data.payload);
  const extra = Object.fromEntries(Object.entries(data).filter(([key]) => !['action','toolname','reason','payload'].includes(key)));
  if (Object.keys(extra).length) rawDetails(node, extra);
}

// A turn's timestamp, shown as a compact local date/time on its card.
function formatTime(value) {
  if (!value) return '';
  const d = new Date(value);
  if (isNaN(d.getTime())) return '';
  const pad = n => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
}
function stampTime(node, value) {
  const label = formatTime(value);
  if (label) node.append(text('time', label, 'turn-time'));
}
function renderTurns(turns) {
  let toolCard = null;
  const cards = [];
  (turns || []).forEach((turn, i) => {
    if (turn.Kind === 'tool_result') {
      if (!toolCard) { toolCard = text('article', '', 'turn assistant tool-card'); toolCard.append(text('div', '⚙ Tool result', 'badge')); stampTime(toolCard, turn.At); $('history').append(toolCard); }
      cards.push(toolCard); logDetails(toolCard, 'Result', turn.Content); toolCard = null; return;
    }
    const parsed = turn.Role === 'assistant' ? parsedResponse(turn.Content) : null;
    const article = text('article', '', 'turn ' + (turn.Role === 'user' ? 'user' : 'assistant'));
    if (!parsed || parsed.action !== 'tool') article.append(text('div', turn.Role === 'user' ? 'You' : '✦ PAI', 'turn-heading'));
    // Only a plain user instruction is a rewind point; ask-answers and tool
    // results share the user role but are not instructions.
    if (turn.Role === 'user' && turn.Kind === 'input') {
      const button = text('button', '↩ from here', 'rewind');
      button.type = 'button';
      button.title = 'Rewind the conversation to before this instruction';
      button.onclick = () => rewind(historyOffset + i, turn.Content);
      article.append(button);
    }
    if (parsed) response(article, parsed); else article.append(prose(turn.Content));
    stampTime(article, turn.At);
    $('history').append(article); cards.push(article); toolCard = parsed && parsed.action === 'tool' ? article : null;
  });
  return cards;
}
function paintConversation() {
  $('history').replaceChildren(); const cards = renderTurns(durable);
  const used = new Set();
  for (const step of liveSteps) {
    let match = -1;
    for (let i = Math.max(0, step.floor - historyOffset); i < durable.length; i++) {
      const p = durable[i].Role === 'assistant' && parsedResponse(durable[i].Content);
      if (!p || p.action !== 'tool' || used.has(i) || p.toolname !== step.call.Name) continue;
      const value = typeof p.payload === 'string' ? p.payload : p.payload && (p.payload.cmd || p.payload.path);
      if (value && ![step.call.Detail, step.call.Target].includes(value)) continue;
      match = i; used.add(i); break;
    }
    let card;
    if (match >= 0) {
      card = cards[match];
    }
    if (card) {
      const represented = match >= 0 && durable[match + 1] && durable[match + 1].Kind === 'tool_result';
      if (step.output) logDetails(card, 'Execution log', step.output, !step.result);
      if (step.result && !represented) { card.append(text('div', step.result.Skipped ? 'Skipped' : step.result.OK ? '✅ Success' : '❌ Failed', 'badge')); logDetails(card, 'Result', [step.result.Message, step.result.Detail].filter(Boolean).join('\n')); }
      else if (!represented) card.append(text('div', step.result ? 'Finished' : 'Executing…', 'progress-status'));
    }
  }
  updateJumpButtons();
}
// ── History and session metadata ────────────────────────────
function sessionMetadata(meta = {}) {
  modelMetadata(meta.model);
  savedCwd = typeof meta.cwd === 'string' ? meta.cwd : '';
  if (!cwdDirty) $('session-working-dir').value = savedCwd;
  workingDirControls();
  savedRole = typeof meta.role === 'string' ? meta.role.trim() : '';
  const roleSelect = $('session-role-select');
  // Preserve an unsaved role choice across the frequent history/snapshot
  // refreshes, exactly as model drafts are kept.
  if (!roleDirty) roleSelect.value = [...roleSelect.children].some(option => option.value === savedRole) ? savedRole : '';
}
async function history(reset = false, forceBottom = false) {
  const version = generation, request = ++historyRequest;
  const scroller = $('conversation');
  let probe = await api(query('history', 0, 1));
  if (version !== generation || request !== historyRequest) return;
  const offset = reset ? (durable.length ? historyOffset : Math.max(0, probe.total - 30)) : Math.max(0, historyOffset - 30);
  const wanted = reset ? probe.total - offset : historyOffset - offset;
  if (!reset && !wanted) return;
  let collected = [], meta = probe, position = offset;
  do {
    meta = await api(query('history', position, Math.max(1, Math.min(200, wanted - collected.length))));
    if (version !== generation || request !== historyRequest) return;
    const turns = meta.turns || []; collected.push(...turns); position += turns.length;
    if (!turns.length || position >= meta.total) break;
  } while (collected.length < wanted);
  const next = reset ? collected : collected.concat(durable);
  const changed = historyOffset !== offset || JSON.stringify(next) !== JSON.stringify(durable);
  const initial = !$('history').children.length;
  const top = scroller.scrollTop, height = scroller.scrollHeight;
  const follow = height - scroller.clientHeight - top <= 80;
  // Bound the window when the reader is at the bottom; older turns reload from
  // the store on scroll-up, so a very long session does not grow without limit.
  let trimmed = 0;
  if (next.length > historyLimit && follow) { trimmed = next.length - historyLimit; next.splice(0, trimmed); }
  durable = next; historyOffset = offset + trimmed; historyTotal = meta.total;
  if (changed || initial) paintConversation();
  sessionMetadata(meta);
  if (!durable.length && (changed || initial)) { const welcome = text('div', '', 'empty'); welcome.append(text('h3', 'Best partner, PAI!'), text('p', 'Give PAI an instruction below.')); $('history').append(welcome); }
  if (forceBottom || initial || (reset && changed && follow)) scrollBottom();
  else if (changed) scroller.scrollTop = reset ? top : top + scroller.scrollHeight - height;
}
async function refreshHistory() {
  if (refreshing) { refreshAgain = true; return; }
  refreshing = true;
  try { do { refreshAgain = false; if (selected) await history(true); } while (refreshAgain); }
  catch (e) { error(e); } finally { refreshing = false; }
}
function scheduleHistory() { clearTimeout(refreshTimer); refreshTimer = setTimeout(refreshHistory, 150); }
// ── Snapshot, prompts, live updates ─────────────────────────
function tokenCount(n) {
  n = Number(n) || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
  return String(n);
}
function usageSnapshot(s) {
  const node = $('token-usage');
  node.hidden = !(s.usage_calls > 0);
  node.textContent = ''; node.title = '';
  if (node.hidden) return;
  const total = s.total_usage, latest = s.usage;
  // The latest request's prompt size is the current context; compared to the
  // compaction threshold it answers "am I about to compress?", not just "how much
  // have I spent". Only shown when compaction is configured (context_tokens > 0).
  const budget = s.context_tokens > 0
    ? `context ${tokenCount(latest.Prompt)} / ${tokenCount(s.context_tokens)} (${Math.round(100 * latest.Prompt / s.context_tokens)}%) · `
    : '';
  node.textContent = `Tokens · ${budget}sent ${tokenCount(total.Prompt)} · received ${tokenCount(total.Completion)} · total ${tokenCount(total.Total)} (this run)`;
  node.title = `Latest request: sent ${tokenCount(latest.Prompt)} · received ${tokenCount(latest.Completion)} · total ${tokenCount(latest.Total)}.`
    + (s.context_tokens > 0 ? ` Compaction summarizes older turns once a prompt exceeds ${s.context_tokens} tokens.` : '')
    + ' This run covers this worker runtime only; resuming a retired runtime starts fresh.';
}
function snapshot(s) {
  usageSnapshot(s);
  modelBlocked = ['busy','starting'].includes(s.state) || s.phase === 'starting' || s.queued > 0 || !!s.pending;
  sessionBusy = ['busy','starting'].includes(s.state);
  if (!trustAllApplying) $('trust-all').checked = !!s.trust_all;
  liveModel = typeof s.model === 'string' ? s.model.trim() : '';
  if (!modelDirty) $('session-model').value = liveModel || savedModel || defaultModel;
  modelControls();
  controls();
  runSnapshot(s);
  if (s.state !== 'busy' || s.pending) flushReasoning();
    else if (s.reasoning !== undefined) {
      thinking = String(s.reasoning).slice(-liveTextLimit);
      $('reasoning-text').textContent = thinking;
      if (thinking) showReasoning(); else $('reasoning-preview').hidden = true;
      $('reasoning-text').scrollTop = $('reasoning-text').scrollHeight;
    }
  const detail = s.active_tool ? statusText(s.active_tool.Detail) : '';
    const full = phaseLabel(s) + (detail ? ' · ' + detail : '');
    $('state').textContent = (full.length > 160 ? full.slice(0, 159) + '…' : full) + (s.queued > 0 ? ' · ' + s.queued + ' queued' : '') + (s.error ? ' · ' + s.error : '');
    $('state').title = full;
  if (!s.pending) { clearPending(); return; }
  const p = s.pending, name = selected;
  // Every live event includes a snapshot; leave the same form (and focus/draft)
  // intact, including the initial snapshot delivered after an SSE reconnect.
  if (pendingID === p.id && pendingSession === name) return;
  clearPending(); pendingID = p.id; pendingSession = name;
  const title = text('h3', p.kind === 'confirm' ? 'Confirmation required' : 'Question'); title.id = 'pending-title';
  flushReasoning();
  $('pending').append(title);
  if (p.kind === 'confirm' && p.tool) {
    const tool = p.tool;
    let heading = '🔧 ' + (tool.Name || 'Tool') + (tool.Target ? ' · ' + tool.Target : '');
    if (tool.Dir) heading += ' (in ' + tool.Dir + ')';
    $('pending').append(text('div', heading, 'confirmation-heading'));
    if (tool.Detail) {
      const shell = ['execute','remote'].includes(tool.Name);
      const segments = Array.isArray(tool.CommandSegments) && tool.CommandSegments.length ? tool.CommandSegments : null;
      if (segments) {
        // The server split the chain once (Go's tool.SplitSegments) and flagged
        // the untrusted indices, so mark exactly those.
        const pre = text('pre', '', 'command-detail');
        const bad = new Set(Array.isArray(tool.UntrustedSegments) ? tool.UntrustedSegments : []);
        segments.forEach((segment, i) => pre.append(text('span', segment + (i < segments.length - 1 ? '\n' : ''), bad.has(i) ? 'cmd-untrusted' : '')));
        $('pending').append(pre);
      } else {
        $('pending').append(text('pre', shell ? shellSegments(tool.Detail).join('\n') : tool.Detail, 'command-detail'));
      }
      if (shell) {
        const original = text('details', '');
        const command = document.createElement('textarea'); command.value = tool.Detail; command.readOnly = true;
        command.setAttribute('aria-label', 'Original command for copying');
        original.append(text('summary', 'Original command'), command); $('pending').append(original);
      }
    }
    if (tool.Reason) $('pending').append(text('p', tool.Reason, 'reason'));
    if (tool.Diff) $('pending').append(diff(tool.Diff));
  }
  if (p.title) $('pending').append(text('p', p.title, 'prompt-question'));
  const reply = async (answer, choice) => {
    try { await api('reply', {name, prompt_id:p.id, text:answer, choice}); if (name === selected) await loadSnapshot(); }
    catch (e) { error(e); if (name === selected) await loadSnapshot(); }
  };
  if (p.kind === 'confirm') {
    const untrusted = Array.isArray(p.untrusted) ? p.untrusted : [];
    if (untrusted.length) {
      // A chained command needs approval for specific programs (or a file change
      // for its directory): offer to trust for this run or for good.
      const label = p.trust_target === 'path' ? 'outside trusted paths: ' : 'not trusted: ';
      $('pending').append(text('p', '⚠ ' + label + untrusted.join(', '), 'untrusted-names'));
      const once = text('button', 'Once'), session = text('button', 'Trust session'), always = text('button', 'Always'), no = text('button', 'Decline');
      once.className = 'primary'; session.className = 'secondary'; always.className = 'secondary'; no.className = 'caution';
      once.onclick = () => reply('', 'once'); session.onclick = () => reply('', 'session');
      always.onclick = () => reply('', 'always'); no.onclick = () => reply('', 'deny');
      $('pending').append(actionRow([once, session, always, no]));
    } else {
      const yes = text('button', 'Approve'), no = text('button', 'Decline');
      yes.className = 'primary'; no.className = 'caution';
      yes.onclick = () => reply('', 'once'); no.onclick = () => reply('', 'deny'); $('pending').append(actionRow([yes, no]));
    }
  }
}
async function loadSnapshot() {
  const version = generation;
  try { const s = await api(query('snapshot', 0)); if (version === generation) snapshot(s); }
  catch (e) { if (version !== generation) return; if (e.status === 404) { stopRunProgress(); usageSnapshot({}); $('state').textContent = 'Stored session · no live runtime'; clearPending(); liveModel = ''; modelBlocked = false; sessionBusy = false; modelControls(); controls(); await history(true); } else error(e); }
}
function disconnect() { stopRunProgress(); clearReasoning(); if (stream) stream.close(); stream = null; clearTimeout(retry); clearTimeout(refreshTimer); }
function connect() {
  if (!selected || !$('login').hidden || stream) return;
  const version = generation;
  stream = new EventSource('/api/events?name=' + encodeURIComponent(selected));
  const connection = stream;
  stream.onmessage = e => {
    if (version !== generation || stream !== connection) return;
    const event = JSON.parse(e.data);

    if (!['reasoning','notice','snapshot'].includes(event.type)) flushReasoning();
    snapshot(event.snapshot);
    if (event.data !== undefined) {
      activity(event);
    }
    if (['snapshot','busy','awaiting','done','terminate','stopped','prompt','prompt_replied','user','session'].includes(event.type)) scheduleHistory();
  };
  stream.onerror = () => {
    if (version !== generation || stream !== connection) return;
    stopRunProgress();
    stream.close(); stream = null;
    // A missing/evicted runtime is normal. Polling also discovers later sends.
    retry = setTimeout(async () => { if (version !== generation) return; await loadSnapshot(); scheduleHistory(); if (version === generation) connect(); }, 2500);
  };
}
// ── Session actions ─────────────────────────────────────────
async function select(name, cwd) {
  disconnect(); generation++; const version = generation; selected = name; historyOffset = 0; sessionBusy = false;
  try { localStorage.setItem(SESSION_KEY, name); } catch {}
  clearReasoning(); usageSnapshot({}); liveModel = ''; modelDirty = false; roleDirty = false; cwdDirty = false; reasoningDirty = false; modelBlocked = true; sessionMetadata();
    $('title').textContent = name;
  durable = []; liveSteps = []; currentStep = null; thinking = ''; historyTotal = 0;
  liveCard = null; liveType = ''; liveText = '';
    $('history').replaceChildren(); clearPending(); $('live-progress').replaceChildren(); $('error').textContent = '';
  updateJumpButtons();
  $('conversation').scrollTop = 0; controls();
  loadTrustAll();
  loadReasoning();
  for (const row of $('sessions').children) { const active = row.dataset?.name === name; row.classList.toggle('selected', active); const open = row.firstElementChild; if (open) open.setAttribute('aria-current', active ? 'page' : 'false'); }
  $('app').classList.remove('sessions-open'); $('toggle-sessions').setAttribute('aria-expanded', 'false');
  try { await history(true, true); } catch (e) { if (e.status !== 404) error(e); }
  if (version !== generation) return;
  await loadSnapshot(); if (version === generation) connect();
}
// Reopen the remembered session on load, if it still exists in the listed set.
function restoreSession(list) {
  if (selected) return;
  let name = '';
  try { name = localStorage.getItem(SESSION_KEY) || ''; } catch { return; }
  if (!name || !list.some(meta => meta.name === name)) return;
  select(name);
}
// Ask whether to snapshot the dropped turns before rewinding. Resolves to true
// (backup), false (plain truncate), or null (cancelled).
function rewindChoice(dropped) {
  return new Promise(resolve => {
    const box = text('div', '', 'rewind-confirm');
    box.append(
      text('p', `Rewind to here? ${dropped} later turn${dropped === 1 ? '' : 's'} will be dropped.`),
      text('p', 'Keep a backup session of the dropped turns?', 'rewind-note'),
    );
    const backup = text('button', 'Rewind + backup', 'primary'), plain = text('button', 'Rewind', 'secondary'), cancel = text('button', 'Cancel', 'caution');
    backup.type = plain.type = cancel.type = 'button';
    const close = value => { box.remove(); resolve(value); };
    backup.onclick = () => close(true);
    plain.onclick = () => close(false);
    cancel.onclick = () => close(null);
    box.append(backup, plain, cancel);
    document.body.append(box);
  });
}
// Rewind to just before the user instruction at persisted index. With a backup
// the dropped turns stay restorable from a rolling session; without, they are
// simply removed.
async function rewind(index, content) {
  if (!selected || sessionBusy) return;
  const backup = await rewindChoice(historyTotal - index);
  if (backup === null) return;
  const name = selected;
  try {
    await api('rewind', {name, keep: index, backup});
    if (selected !== name) return;
    $('message').value = content || '';
    controls();
    await loadSnapshot();
    await refreshHistory();
    sessions(true).catch(error);
  } catch (e) { error(e); }
}
async function action(kind, override) {
  if (!selected) { error(new Error('Open a named session first')); return; }
  const name = selected, message = override === undefined ? $('message').value : override;
  if (kind !== 'cancel' && !message.trim()) return;
  try {
    await api(kind, {name, text:kind === 'cancel' ? '' : message});
    if (selected === name && kind !== 'cancel') scrollBottom();
    // A canned override (the auto-reply button) leaves the draft alone.
    if (override === undefined && kind !== 'cancel' && selected === name && $('message').value === message) $('message').value = '';
    controls();
    if (selected === name) { await loadSnapshot(); if (!stream) connect(); scheduleHistory(); }
    await sessions(true);
  } catch (e) { error(e); }
}
// Delete a stored session after confirmation. Deleting the open session also
// returns the workspace to its unselected state.
async function deleteSession(name) {
  if (!name) return;
  if (!confirm('Delete session "' + name + '"? Its saved history is removed and cannot be recovered.')) return;
  try {
    await api('delete', {name});
  } catch (e) { error(e); sessions(true).catch(() => {}); return; }
  if (selected === name) clearWorkspace();
  await sessions(true).catch(error);
}
// Return the workspace to its pre-selection state once the open session is gone.
function clearWorkspace() {
  disconnect(); generation++; selected = '';
  try { localStorage.removeItem(SESSION_KEY); } catch {}
  durable = []; liveSteps = []; currentStep = null; thinking = '';
  liveCard = null; liveType = ''; liveText = ''; historyOffset = 0; historyTotal = 0;
  sessionBusy = false; liveModel = ''; modelDirty = false; roleDirty = false; cwdDirty = false; reasoningDirty = false; modelBlocked = false;
  $('trust-all').checked = false;
  $('history').replaceChildren(); clearPending(); $('live-progress').replaceChildren(); $('error').textContent = '';
  clearReasoning(); usageSnapshot({});
  reasoningMetadata('');
  $('title').textContent = 'Select or create a session';
  sessionMetadata();
  const empty = text('div', '', 'empty');
  empty.append(text('h3', 'Best partner, PAI!'), text('p', 'Open a named session to start working with PAI.'));
  $('history').append(empty);
  updateJumpButtons();
  controls();
}
// ── Activity feed ───────────────────────────────────────────
// The right panel logs only context-lifecycle events — what changes the prompt the
// model sees (reasoning, compaction, context notices). Tools and run progress
// drive the conversation and the run indicator instead of this panel.
const contextEventTypes = new Set(['output']);
function activity(event) {
  const type = event.type, data = event.data;
  // Phased snapshots are authoritative even when event output is stale.
  if (!event.snapshot?.phase) {
  if (['done','terminate','stopped','ask','prompt','awaiting'].includes(type) && !pendingID) stopRunProgress();
  else if (runTimer !== null) {
    if (type === 'tool_call') {
      const name = String(data?.Name || 'tool').replace(/[\x00-\x1f\x7f]/g, '').slice(0, 48);
      runPhase('Running ' + name + '…', true);
    } else if (['reasoning','output','tool_result'].includes(type)) runPhase('Thinking…', type === 'tool_result');
  }
  }
  if (type === 'reasoning') {
    // Reasoning lives only in the dedicated preview while it streams; flushReasoning
    // hands the finished block to the activity log, so it is never shown in both
    // places at once.
    currentStep = null; thinking = (thinking + String(data)).slice(-liveTextLimit);
    $('reasoning-text').textContent = thinking; showReasoning();
    $('reasoning-text').scrollTop = $('reasoning-text').scrollHeight;
    return;
  }
  if (type !== 'notice' && type !== 'snapshot') flushReasoning();
  if (contextEventTypes.has(type)) {
    const streaming = ['tool_output','output'].includes(type);
    if (!streaming || liveType !== type || !liveCard) {
      liveCard = text('article', '', 'activity-card');
      liveCard.append(text('div', type.replaceAll('_', ' '), 'card-heading'), text('pre', ''));
      $('live-progress').append(liveCard); liveText = ''; liveType = type;
      while ($('live-progress').children.length > activityLimit) $('live-progress').firstElementChild.remove();
    }
    liveText = (liveText + (typeof data === 'string' ? data : JSON.stringify(data, null, 2))).slice(-liveTextLimit);
    liveCard.lastElementChild.textContent = liveText;
    liveCard.lastElementChild.scrollTop = liveCard.lastElementChild.scrollHeight;
    activityScroll();
  }
  if (type === 'tool_call') {
    currentStep = {call:data, floor:Math.max(0, historyTotal - 1), output:'', result:null};
    liveSteps.push(currentStep); if (liveSteps.length > 100) liveSteps.shift(); scheduleHistory();
  } else if (type === 'tool_output' && currentStep) currentStep.output = (currentStep.output + String(data)).slice(-liveTextLimit);
  else if (type === 'tool_result' && currentStep) { currentStep.result = data; scheduleHistory(); }
  else if (type === 'notice') toast(typeof data === 'string' ? data : String(data));
  else if (['done','terminate','ask','user'].includes(type)) scheduleHistory();
  else if (type !== 'output') return;
}
// Presentation only: never feed these segments back into execution or copying.
function shellSegments(command) {
  const segments = []; let start = 0, quote = '', escaped = false, depth = 0;
  for (let i = 0; i < command.length; i++) {
    const c = command[i];
    if (escaped) { escaped = false; continue; }
    if (c === '\\' && quote !== "'") { escaped = true; continue; }
    if (quote) { if (c === quote) quote = ''; continue; }
    if (c === "'" || c === '"' || c === '`') { quote = c; continue; }
    if (c === '(') { depth++; continue; }
    if (c === ')') { if (!depth) return [command]; depth--; continue; }
    if (depth || !['&', '|', ';'].includes(c)) continue;
    // Redirections and case terminators are not command-chain operators.
    if ((c === '|' || c === '&') && command[i - 1] === '>') continue;
    if (c === '&' && command[i + 1] !== '&') continue;
    if (c === ';' && (command[i + 1] === ';' || command[i - 1] === ';')) return [command];
    const end = i + ((c === '&' || c === '|') && command[i + 1] === c ? 2 : c === '|' && command[i + 1] === '&' ? 2 : 1);
    segments.push(command.slice(start, end).trim()); start = end; i = end - 1;
  }
  if (quote || escaped || depth || command.includes('<<')) return [command];
  segments.push(command.slice(start).trim());
  return segments.every(Boolean) ? segments : [command];
}
function flushReasoning() {
  // Append the finished reasoning block to the activity log once, in order, then
  // clear the live preview. Called when a step ends; a no-op when nothing streamed.
  if (thinking) {
    const card = text('article', '', 'activity-card');
    card.append(text('div', 'reasoning', 'card-heading'), text('pre', thinking));
    $('live-progress').append(card);
    while ($('live-progress').children.length > activityLimit) $('live-progress').firstElementChild.remove();
    activityScroll();
  }
  clearReasoning();
}
function clearReasoning() {
  thinking = ''; $('reasoning-text').textContent = ''; $('reasoning-preview').hidden = true;
}
// The new-session role is a radio grid, so every choice is visible at once
// instead of hidden behind a dropdown.
function roleChoice(role) {
  const option = text('label', '', 'role-option');
  const input = document.createElement('input');
  input.type = 'radio'; input.name = 'new-session-role'; input.value = role;
  option.append(input, text('span', role));
  return option;
}
function selectedRole() {
  const chosen = [...$('session-role').children].map(option => option.children[0]).find(input => input && input.checked);
  return chosen ? chosen.value : '';
}
function setSelectedRole(role) {
  for (const option of $('session-role').children) if (option.children[0]) option.children[0].checked = option.children[0].value === role;
}
async function roles() {
  const data = await api('roles');
  const option = role => { const node = text('option', role); node.value = role; return node; };
  const group = $('session-role'), previous = selectedRole();
  group.replaceChildren(...data.roles.map(roleChoice));
  setSelectedRole(data.roles.includes(previous) ? previous : data.default_role);
  // The header role control switches an existing session's role.
  const header = $('session-role-select');
  const draft = roleDirty ? header.value : '';
  header.replaceChildren(...data.roles.map(option));
  header.value = data.roles.includes(draft || savedRole) ? (draft || savedRole) : '';
  controls();
}
async function applyRole(e) {
  e.preventDefault();
  const role = $('session-role-select').value;
  if (!selected || !role || role === savedRole) return;
  const name = selected;
  try {
    	await api('role', {name, role});
    	if (selected !== name) return;
    	roleDirty = false;
    	await loadSnapshot();
    	await refreshHistory();
  } catch (err) { error(err); }
}
async function renameSession() {
  if (!selected || sessionBusy) return;
  const name = selected;
  const next = (prompt('Rename session to', name) || '').trim();
  if (!next || next === name) return;
  try {
    const meta = await api('rename', {name, new_name: next});
    await sessions(true);
    await select(meta.name || next);
  } catch (err) { error(err); }
}
let creating = false;
async function createSession(e) {
  e.preventDefault(); if (creating) return;
  const name = $('session-name').value.trim(), cwd = $('session-cwd').value.trim();
  if (!name) { error(new Error('Enter a session name')); return; }
  if (/[\x00-\x1f\x7f]/.test(cwd)) { error(new Error('Working directory must not contain control characters')); return; }
  const version = generation;
  creating = true; $('create-session').disabled = true; $('error').textContent = '';
  try {
    const meta = await api('create', {name, working_dir:cwd, role:selectedRole()});
    if (version === generation) await select(meta.name, meta.cwd);
    await sessions(true);
  } catch (err) { error(err); }
  finally { creating = false; $('create-session').disabled = false; }
}
function composerKey(e) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); if (!$('send').disabled) action('send'); }
}
// ── Event wiring ────────────────────────────────────────────
$('login-form').onsubmit = async e => { e.preventDefault(); try { await api('login', {token:$('token').value}); $('token').value = ''; showApp(); const list = await sessions(true); await Promise.all([roles(), models(), loadReasoning()]); restoreSession(list); if (selected) connect(); } catch (err) { error(err); } };
$('model-form').onsubmit = applyModel;
$('reasoning-form').onsubmit = applyReasoning;
$('reasoning-select').onchange = () => { reasoningDirty = true; reasoningControls(); };
$('role-form').onsubmit = applyRole;
$('working-dir-form').onsubmit = applyWorkingDir;
$('session-working-dir').oninput = () => { cwdDirty = true; workingDirControls(); };
$('rename-session').onclick = renameSession;
$('session-model').oninput = () => { modelDirty = true; renderModelOptions($('session-model').value); modelControls(); };
$('session-model').onfocus = () => renderModelOptions($('session-model').value);
$('session-model').onblur = () => setTimeout(closeModelOptions, 120);
$('session-model').onkeydown = event => { if (event.key === 'Escape') closeModelOptions(); };
$('session-role-select').onchange = () => { roleDirty = true; controls(); };
$('new-session').onsubmit = createSession;
$('composer').onsubmit = e => { e.preventDefault(); action('send'); };
$('steer').onclick = () => action('steer'); $('cancel').onclick = () => action('cancel');
$('apply-plan').onclick = () => action('send', AUTO_REPLY);
$('trust-all').onchange = () => setTrustAll($('trust-all').checked);
$('more-sessions').onclick = () => sessions().catch(error);
$('theme-toggle').onclick = toggleTheme;
$('prev-input').onclick = jumpToPrevInput;
$('to-bottom').onclick = jumpToBottom;
$('conversation').onscroll = () => {
  updateJumpButtons();
  // Reach the top and load a few more older turns (the initial window is small).
  const scroller = $('conversation');
  if (scroller.scrollTop <= 8 && historyOffset > 0 && !refreshing) history().catch(error);
};
$('message').oninput = controls;
$('message').onkeydown = composerKey;
$('toggle-sessions').onclick = () => {
  const open = $('app').classList.toggle('sessions-open');
  $('toggle-sessions').setAttribute('aria-expanded', String(open));
  if (open) { $('app').classList.remove('activity-open'); $('toggle-activity').setAttribute('aria-expanded', 'false'); }
};
$('toggle-activity').onclick = () => {
  const open = $('app').classList.toggle('activity-open');
  $('toggle-activity').setAttribute('aria-expanded', String(open));
  if (open) { $('app').classList.remove('sessions-open'); $('toggle-sessions').setAttribute('aria-expanded', 'false'); }
  activityScroll();
};

controls();
applyTheme(initialTheme());
initPendingDrag();
initReasoningDrag();
updateJumpButtons();
sessions(true).then(list => { showApp(); restoreSession(list); }).catch(bootFailed);
roles().catch(bootFailed);
models().catch(bootFailed);
loadReasoning();
