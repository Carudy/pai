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
// The browser remembers the open session in localStorage, never a cookie: it is
// only needed after the page loads, so it need not travel on every request, and
// it needs no server state. A cleared store just leaves the workspace empty.
const SESSION_KEY = 'pai.session';
let toastHost = null;
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
  $('model-options').replaceChildren(...data.models.map(model => { const option = text('option', model); option.value = model; return option; }));
  if (!modelDirty) $('session-model').value = liveModel || savedModel || defaultModel;
  modelControls();
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
// ── Run status, live transcript, response parsing ───────────
let durable = [], liveSteps = [], currentStep = null, thinking = '';
let liveCard = null, liveType = '', liveText = '';
const liveTextLimit = 16000;
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
function scrollBottom() { $('conversation').scrollTop = $('conversation').scrollHeight; }
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
function clearPending() { $('pending').replaceChildren(); pendingID = null; pendingSession = null; }
const text = (tag, value, className) => { const node = document.createElement(tag); node.textContent = value; if (className) node.className = className; return node; };
function error(e) { $('error').textContent = e.message || String(e); }
// A transient notice, layered over the workspace so a reason (e.g. why "Always"
// became session-only, or that a directory is now trusted) is not lost in the
// activity log. At most three are shown; each fades on its own.
function toast(message) {
  if (!document.body) return;
  if (!toastHost) { toastHost = text('div', '', 'toasts'); document.body.append(toastHost); }
  const node = text('div', message, 'toast');
  toastHost.append(node);
  while (toastHost.children.length > 3) toastHost.firstElementChild.remove();
  setTimeout(() => node.remove(), 6000);
}
function loginNeeded() { $('login').hidden = false; $('app').hidden = true; disconnect(); }
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
  // Prefill the working-directory field with the server's launch dir when empty.
  if (typeof page.default_cwd === 'string' && page.default_cwd && !$('session-cwd').value) {
    $('session-cwd').value = page.default_cwd;
  }
  for (const meta of page.sessions || []) {
    const button = text('button', meta.name); button.classList.toggle('selected', meta.name === selected);
    button.setAttribute('aria-current', meta.name === selected ? 'page' : 'false');
    button.onclick = () => select(meta.name, meta.cwd); $('sessions').append(button);
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
  $('composer-hint').textContent = !ready
    ? 'Open a session to send instructions'
    : sessionBusy
      ? 'Enter to send · Shift+Enter for newline · While busy: Sends queue, Steer redirects, Cancel stops'
      : 'Enter to send · Shift+Enter for newline';
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
const icons = {execute:'⌘', remote:'🌐', read:'📄', write:'✍', edit:'✎', websearch:'🔎'};
function payload(node, value, limit = Infinity) {
  if (typeof value === 'string') { node.append(text('pre', value.slice(0, limit))); return; }
  if (!value || typeof value !== 'object') { if (value !== undefined) node.append(prose(String(value))); return; }
  const known = ['path','host','cmd','content','old_string','new_string','offset','limit','replace_all'];
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
  node.append(text('div', (tool ? (icons[data.toolname] || '⚙') + ' ' + (data.toolname || 'Tool') : ({done:'✓ Complete',ask:'? Question',terminate:'■ Stopped'}[data.action] || data.action)), data.action === 'done' ? 'badge complete' : 'badge'));
  if (data.reason) node.append(text('div', 'Reason', 'field-label'), prose(data.reason));
  if (tool) { node.className += ' tool-card'; if (typeof data.payload === 'string') node.append(text('div', data.toolname === 'execute' ? 'CMD' : 'Query', 'field-label')); payload(node, data.payload); }
  else if (typeof data.payload === 'string') node.append(data.action === 'done' ? completeProse(data.payload) : prose(data.payload));
  else payload(node, data.payload);
  const extra = Object.fromEntries(Object.entries(data).filter(([key]) => !['action','toolname','reason','payload'].includes(key)));
  if (Object.keys(extra).length) rawDetails(node, extra);
}

function renderTurns(turns) {
  let toolCard = null;
  const cards = [];
  for (const turn of turns || []) {
    if (turn.Kind === 'tool_result') {
      if (!toolCard) { toolCard = text('article', '', 'turn assistant tool-card'); toolCard.append(text('div', '⚙ Tool result', 'badge')); $('history').append(toolCard); }
      cards.push(toolCard); logDetails(toolCard, 'Result', turn.Content); toolCard = null; continue;
    }
    const parsed = turn.Role === 'assistant' ? parsedResponse(turn.Content) : null;
    const article = text('article', '', 'turn ' + (turn.Role === 'user' ? 'user' : 'assistant'));
    if (!parsed || parsed.action !== 'tool') article.append(text('div', turn.Role === 'user' ? 'You' : '✦ PAI', 'turn-heading'));
    if (parsed) response(article, parsed); else article.append(prose(turn.Content));
    $('history').append(article); cards.push(article); toolCard = parsed && parsed.action === 'tool' ? article : null;
  }
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
      if (step.result && !represented) { card.append(text('div', step.result.Skipped ? 'Skipped' : step.result.OK ? '✓ Success' : '✕ Failed', 'badge')); logDetails(card, 'Result', [step.result.Message, step.result.Detail].filter(Boolean).join('\n')); }
      else if (!represented) card.append(text('div', step.result ? 'Finished' : 'Executing…', 'progress-status'));
    }
  }

}
// ── History and session metadata ────────────────────────────
function sessionMetadata(meta = {}) {
  modelMetadata(meta.model);
  for (const field of ['role', 'model']) {
    const node = $('session-' + field + '-info');
    const value = typeof meta[field] === 'string' ? meta[field].trim() : '';
    node.textContent = value ? field + ': ' + value : '';
    node.title = 'Saved ' + field + (value ? ': ' + value : '');
    node.hidden = !value;
  }
}
async function history(reset = false, forceBottom = false) {
  const version = generation, request = ++historyRequest;
  const scroller = $('conversation');
  let probe = await api(query('history', 0, 1));
  if (version !== generation || request !== historyRequest) return;
  const offset = reset ? (durable.length ? historyOffset : Math.max(0, probe.total - 100)) : Math.max(0, historyOffset - 100);
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
  durable = next; historyOffset = offset; historyTotal = meta.total;
  if (changed || initial) paintConversation();
  $('more-history').hidden = historyOffset === 0;
  $('workspace').textContent = 'Workspace: ' + (meta.cwd || 'server working directory');
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
function tokenCount(n) { return n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k' : String(n); }
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
  node.textContent = `Tokens · ${budget}sent ${total.Prompt} · received ${total.Completion} · total ${total.Total} (this run)`;
  node.title = `Latest request: sent ${latest.Prompt} · received ${latest.Completion} · total ${latest.Total}.`
    + (s.context_tokens > 0 ? ` Compaction summarizes older turns once a prompt exceeds ${s.context_tokens} tokens.` : '')
    + ' This run covers this worker runtime only; resuming a retired runtime starts fresh.';
}
function snapshot(s) {
  usageSnapshot(s);
  modelBlocked = ['busy','starting'].includes(s.state) || s.phase === 'starting' || !!s.pending;
  sessionBusy = ['busy','starting'].includes(s.state);
  liveModel = typeof s.model === 'string' ? s.model.trim() : '';
  if (!modelDirty) $('session-model').value = liveModel || savedModel || defaultModel;
  modelControls();
  controls();
  runSnapshot(s);
  if (s.state !== 'busy' || s.pending) flushReasoning();
    else if (s.reasoning !== undefined) {
      thinking = String(s.reasoning).slice(-liveTextLimit);
      $('reasoning-text').textContent = thinking; $('reasoning-preview').hidden = !thinking;
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
    $('pending').append(text('div', '🔧 ' + (tool.Name || 'Tool') + (tool.Target ? ' · ' + tool.Target : ''), 'confirmation-heading'));
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
      $('pending').append(once, session, always, no);
    } else {
      const yes = text('button', 'Approve'), no = text('button', 'Decline');
      yes.className = 'primary'; no.className = 'caution';
      yes.onclick = () => reply('', 'once'); no.onclick = () => reply('', 'deny'); $('pending').append(yes, no);
    }
  } else {
    const form = document.createElement('form'), input = document.createElement('textarea'), button = text('button', 'Reply');
    input.setAttribute('aria-label', 'Answer'); input.required = true;
    form.append(input, button); form.onsubmit = e => { e.preventDefault(); reply(input.value, 'once'); }; $('pending').append(form);
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
  clearReasoning(); usageSnapshot({}); liveModel = ''; modelDirty = false; modelBlocked = true; sessionMetadata();
    $('title').textContent = name; $('workspace').textContent = 'Workspace: ' + (cwd || 'server working directory');
  durable = []; liveSteps = []; currentStep = null; thinking = ''; historyTotal = 0;
  liveCard = null; liveType = ''; liveText = '';
    $('history').replaceChildren(); clearPending(); $('live-progress').replaceChildren(); $('error').textContent = '';
  $('conversation').scrollTop = 0; controls();
  for (const button of $('sessions').children) { const active = button.textContent === name; button.classList.toggle('selected', active); button.setAttribute('aria-current', active ? 'page' : 'false'); }
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
async function action(kind) {
  if (!selected) { error(new Error('Open a named session first')); return; }
  const name = selected, message = $('message').value;
  if (kind !== 'cancel' && !message.trim()) return;
  try {
    await api(kind, {name, text:kind === 'cancel' ? '' : message});
    if (selected === name && kind !== 'cancel') scrollBottom();
    if (kind !== 'cancel' && selected === name && $('message').value === message) $('message').value = '';
    controls();
    if (selected === name) { await loadSnapshot(); if (!stream) connect(); scheduleHistory(); }
    await sessions(true);
  } catch (e) { error(e); }
}
// ── Activity feed ───────────────────────────────────────────
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
    $('reasoning-text').textContent = thinking; $('reasoning-preview').hidden = false;
    $('reasoning-text').scrollTop = $('reasoning-text').scrollHeight;
    return;
  }
  if (type !== 'notice' && type !== 'snapshot') flushReasoning();
  const streaming = ['tool_output','output'].includes(type);
  if (!streaming || liveType !== type || !liveCard) {
    liveCard = text('article', '', 'activity-card');
    liveCard.append(text('div', type.replaceAll('_', ' '), 'card-heading'), text('pre', ''));
    $('live-progress').append(liveCard); liveText = ''; liveType = type;
    while ($('live-progress').children.length > 100) $('live-progress').firstElementChild.remove();
  }
  liveText = (liveText + (typeof data === 'string' ? data : JSON.stringify(data, null, 2))).slice(-liveTextLimit);
  liveCard.lastElementChild.textContent = liveText;
  liveCard.lastElementChild.scrollTop = liveCard.lastElementChild.scrollHeight;
  activityScroll();
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
    while ($('live-progress').children.length > 100) $('live-progress').firstElementChild.remove();
    activityScroll();
  }
  clearReasoning();
}
function clearReasoning() {
  thinking = ''; $('reasoning-text').textContent = ''; $('reasoning-preview').hidden = true;
}
async function roles() {
  const data = await api('roles');
  const dropdown = $('session-role'), previous = dropdown.value;
  const options = data.roles.map(role => { const option = text('option', role); option.value = role; return option; });
  dropdown.replaceChildren(...options);
  dropdown.value = data.roles.includes(previous) ? previous : data.default_role;
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
    const meta = await api('create', {name, working_dir:cwd, role:$('session-role').value});
    if (version === generation) await select(meta.name, meta.cwd);
    await sessions(true);
  } catch (err) { error(err); }
  finally { creating = false; $('create-session').disabled = false; }
}
function composerKey(e) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); if (!$('send').disabled) action('send'); }
}
// ── Event wiring ────────────────────────────────────────────
$('login-form').onsubmit = async e => { e.preventDefault(); try { await api('login', {token:$('token').value}); $('token').value = ''; $('login').hidden = true; $('app').hidden = false; const list = await sessions(true); await Promise.all([roles(), models()]); restoreSession(list); if (selected) connect(); } catch (err) { error(err); } };
$('model-form').onsubmit = applyModel;
$('session-model').oninput = () => { modelDirty = true; modelControls(); };
$('new-session').onsubmit = createSession;
$('composer').onsubmit = e => { e.preventDefault(); action('send'); };
$('steer').onclick = () => action('steer'); $('cancel').onclick = () => action('cancel');
$('more-sessions').onclick = () => sessions().catch(error);
$('more-history').onclick = () => history().catch(error);
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
sessions(true).then(restoreSession).catch(error);
roles().catch(error);
models().catch(error);
