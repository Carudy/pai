'use strict';
// Run with node internal/web/app_test.js; no DOM library or dependencies needed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(__dirname + '/app.js', 'utf8').split("$('login-form').onsubmit")[0];
class Element {
  constructor(tag) {
    this.tag = tag; this.tagName = tag.toUpperCase(); this.children = []; this._text = ''; this.value = ''; this.dataset = {}; this.attributes = {};
    this.scrollTop = 0; this.scrollHeight = 1000; this.clientHeight = 400; this.offsetHeight = 20;
    this.classes = new Set();
    this.classList = {toggle: (name, force) => { const on = force === undefined ? !this.classes.has(name) : force; if (on) this.classes.add(name); else this.classes.delete(name); return on; }, remove: name => this.classes.delete(name)};
  }
  set innerHTML(value) { throw new Error('innerHTML is forbidden'); }
    get textContent() { return this._text + this.children.map(node => node.textContent).join(''); }
  set textContent(value) { this._text = String(value); this.children = []; }
  get firstElementChild() { return this.children[0]; }
  get lastElementChild() { return this.children[this.children.length - 1]; }
  append(...nodes) { for (const node of nodes) { node.parent = this; this.children.push(node); } }
  replaceChildren(...nodes) { this._text = ''; this.children = []; this.append(...nodes); }
  setAttribute(key, value) { this.attributes[key] = value; }
  remove() { this.parent.children.splice(this.parent.children.indexOf(this), 1); }
}
function browser() {
  const html = fs.readFileSync(__dirname + '/index.html', 'utf8');
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], new Element(match[1])]));
  let now = 0, timerID = 0;
    const intervals = new Map(), timeouts = new Map(), connections = [];
    elements.get('run-progress').append(new Element('span'), elements.get('run-elapsed'));
    class MockEventSource {
      constructor() { connections.push(this); }
      close() { this.closed = true; }
    }
    const context = vm.createContext({URL, Date: {now: () => now}, EventSource: MockEventSource,
      setInterval: (callback, delay) => { assert.equal(delay, 1000); intervals.set(++timerID, callback); return timerID; },
      clearInterval: id => intervals.delete(id), setTimeout: callback => { timeouts.set(++timerID, callback); return timerID; }, clearTimeout: id => timeouts.delete(id), document: {
    getElementById(id) { return elements.get(id) || null; },
    createElement(tag) { return new Element(tag); }
  }});
  vm.runInContext(source, context);
  return {tick: milliseconds => { now += milliseconds; for (const callback of intervals.values()) callback(); }, timers: () => intervals.size, connections,
        flush: async () => { const callbacks = [...timeouts.values()]; timeouts.clear(); for (const callback of callbacks) await callback(); },
      run: code => vm.runInContext(code, context), get: id => { vm.runInContext(`$('${id}')`, context); return elements.get(id); }, pending: () => elements.get('pending')};
}
const recovery = browser();
recovery.run("snapshot({state:'busy',phase:'reasoning',reasoning:'first second'});");
assert.equal(recovery.get('reasoning-text').textContent, 'first second');
assert.equal(recovery.get('reasoning-preview').hidden, false);
recovery.run("activity({type:'reasoning',data:' third'});");
assert.equal(recovery.get('reasoning-text').textContent, 'first second third');
recovery.run("snapshot({state:'awaiting',phase:'awaiting'});");
assert.equal(recovery.get('reasoning-preview').hidden, true);
const progress = browser();
progress.run("selected='one'; $('login').hidden=true; snapshot({state:'starting',queued:0})");
assert.equal(progress.get('run-progress').hidden, false);
assert.match(progress.get('run-elapsed').textContent, /Starting… · 0s \(this browser\)/);
assert.equal(progress.timers(), 1);
progress.tick(65000);
assert.match(progress.get('run-elapsed').textContent, /1m 5s/);
progress.run("snapshot({state:'starting',queued:1})");
assert.match(progress.get('run-elapsed').textContent, /1m 5s/, 'repeated snapshots preserve clock');
progress.run("snapshot({state:'busy',queued:0}); activity({type:'tool_call',data:{Name:'execute',Detail:'secret command'}})");
assert.match(progress.get('run-elapsed').textContent, /Running execute… · 0s/);
assert.ok(!progress.get('run-elapsed').textContent.includes('secret'));
progress.tick(3000);
progress.run("snapshot({state:'busy',queued:0})");
assert.match(progress.get('run-elapsed').textContent, /Running execute… · 3s/);
progress.run("activity({type:'tool_call',data:{Name:'execute'}})");
assert.match(progress.get('run-elapsed').textContent, /0s/, 'each tool call resets even for same tool');
assert.equal(progress.timers(), 1);
progress.tick(2000);
progress.run("activity({type:'tool_result',data:{OK:true}})");
assert.match(progress.get('run-elapsed').textContent, /Thinking… · 0s/);
progress.run("snapshot({state:'busy',queued:0,pending:{id:'p',kind:'confirm'}}); activity({type:'tool_call',data:{Name:'execute'}})");
assert.equal(progress.get('run-progress').hidden, false);
assert.equal(progress.get('run-progress').firstElementChild.hidden, true, 'approval must not show spinner');
assert.equal(progress.get('run-elapsed').textContent, 'Waiting for approval');
assert.equal(progress.timers(), 0);
for (const state of ['awaiting','idle','stopped']) {
  progress.run("snapshot({state:'busy',queued:0}); snapshot({state:" + JSON.stringify(state) + ",queued:0})");
  assert.equal(progress.timers(), 0);
  assert.equal(progress.get('run-progress').hidden, true);
}
progress.run("snapshot({state:'busy',queued:0}); connect()");
const oldConnection = progress.connections[0];
oldConnection.onerror();
assert.equal(progress.timers(), 0, 'connection loss stops interval');
progress.tick(10000);
progress.run('connect()');
oldConnection.onmessage({data:JSON.stringify({type:'snapshot',snapshot:{state:'busy',queued:0}})});
assert.equal(progress.timers(), 0, 'obsolete connection cannot restart clock');
progress.connections[1].onmessage({data:JSON.stringify({type:'snapshot',snapshot:{state:'busy',queued:0}})});
assert.match(progress.get('run-elapsed').textContent, /0s \(this browser\)/, 'reconnect starts a fresh browser clock');
for (const cleanup of ['disconnect()', 'loginNeeded()']) {
  progress.run("snapshot({state:'busy',queued:0}); " + cleanup);
  assert.equal(progress.timers(), 0);
}
progress.run("snapshot({state:'busy',queued:0}); history=async()=>{}; loadSnapshot=async()=>{}; select('two','/fixed')");
assert.equal(progress.timers(), 0, 'selection clears old timer immediately');

for (const name of ['execute','remote','read','write','edit','websearch','custom']) {
  const tool = browser();
  tool.run("snapshot({state:'busy',queued:0}); activity({type:'tool_call',data:{Name:" + JSON.stringify(name) + "}})");
  assert.match(tool.get('run-elapsed').textContent, new RegExp('Running ' + name + '… · 0s'));
  assert.equal(tool.get('run-progress').firstElementChild.hidden, false);
  tool.tick(2000);
  tool.run("snapshot({state:'busy',queued:0}); activity({type:'tool_output',data:'log'})");
  assert.match(tool.get('run-elapsed').textContent, /2s/);
  tool.run("activity({type:'tool_result',data:{OK:true}})");
  assert.match(tool.get('run-elapsed').textContent, /Thinking… · 0s/);
}
const waiting = browser();
waiting.run("snapshot({state:'busy',queued:0,pending:{id:'answer',kind:'ask'}})");
waiting.tick(5000);
waiting.run("snapshot({state:'busy',queued:0,pending:{id:'answer',kind:'ask'}})");
assert.equal(waiting.get('run-elapsed').textContent, 'Waiting for answer');
assert.equal(waiting.get('run-progress').firstElementChild.hidden, true);
assert.equal(waiting.timers(), 0);

const phases = browser();
for (const [phase, label] of Object.entries({starting:'Starting…', waiting_model:'Waiting for model response', reasoning:'Receiving reasoning'})) {
  phases.run(`snapshot({state:'busy',phase:${JSON.stringify(phase)},queued:0})`);
  assert.ok(phases.get('run-elapsed').textContent.startsWith(label));
  assert.equal(phases.get('state').textContent, label);
  phases.tick(3000);
  phases.get('conversation').scrollTop = 17;
  phases.run(`snapshot({state:'busy',phase:${JSON.stringify(phase)},queued:0})`);
  assert.match(phases.get('run-elapsed').textContent, /3s/);
  assert.equal(phases.get('conversation').scrollTop, 17);
}
for (const name of ['execute','remote','read','write','edit','websearch','custom']) {
  const s = {state:'busy',phase:'tool',queued:0,active_tool:{Name:name,Target:'target',Detail:'<script>command</script> ' + 'x'.repeat(200)}};
  phases.run(`snapshot(${JSON.stringify(s)})`);
  assert.ok(phases.get('run-elapsed').textContent.startsWith('Running ' + name + ' · target'));
  assert.equal(phases.get('state').textContent.length, 160);
  assert.ok(phases.get('state').title.includes(s.active_tool.Detail));
  phases.tick(2000);
  phases.run(`snapshot(${JSON.stringify(s)}); activity({type:'output',data:'stale',snapshot:${JSON.stringify(s)}})`);
  assert.match(phases.get('run-elapsed').textContent, /2s/);
  assert.ok(!phases.get('state').textContent.includes('0 queued'));
}
for (const [kind, label] of [['confirm','Waiting for approval'],['ask','Waiting for answer']]) {
  phases.run(`snapshot({state:'busy',phase:'tool',queued:2,pending:{id:${JSON.stringify(kind)},kind:${JSON.stringify(kind)}}})`);
  assert.equal(phases.get('run-elapsed').textContent, label);
  assert.equal(phases.get('state').textContent, label + ' · 2 queued');
  assert.equal(phases.timers(), 0);
}
for (const phase of ['awaiting','stopped']) {
  phases.run(`snapshot({state:'busy',phase:${JSON.stringify(phase)},queued:0})`);
  assert.equal(phases.timers(), 0);
}

const page = browser();
page.run("selected = 'one'; snapshot({state:'busy',queued:0,pending:{id:'1',kind:'ask',title:'Question'}})");
const form = descendants(page.pending()).find(node => node.tag === 'form'), input = descendants(form).find(node => node.tag === 'textarea');
input.value = 'unfinished answer';
for (let i = 0; i < 5; i++) {
  page.run("snapshot({state:'busy',queued:2,pending:{id:'1',kind:'ask',title:'Question'}})");
  assert.equal(descendants(page.pending()).find(node => node.tag === 'form'), form, 'same prompt must preserve form identity/focus');
  assert.equal(input.value, 'unfinished answer', 'same prompt must preserve draft');
}
page.run("snapshot({state:'busy',queued:0,pending:{id:'2',kind:'ask',title:'Next question'}})");
assert.notEqual(descendants(page.pending()).find(node => node.tag === 'form'), form);
assert.equal(descendants(page.pending()).find(node => node.tag === 'form').children[0].value, '');
page.run("snapshot({state:'awaiting',queued:0})");
assert.equal(page.pending().children.length, 0);
const confirmation = {state:'busy',queued:0,pending:{id:'3',kind:'confirm',title:'Review edit',tool:{Name:'edit',Target:'/fixed/file',Detail:'replace text',Reason:'fix',Diff:'-old\n+new'}}};
function show(target) { target.run("selected = 'one'; snapshot(" + JSON.stringify(confirmation) + ')'); }
show(page);
assert.match(page.pending().textContent, /🔧 edit · \/fixed\/file/);
assert.match(page.pending().textContent, /-old\n\+new/);
const preview = page.pending().children.find(node => node.className === 'diff');
assert.match(preview.children[0].className, /diff-remove/);
assert.match(preview.children[1].className, /diff-add/);
page.run('snapshot(' + JSON.stringify(confirmation) + ')');
assert.equal(page.pending().children.find(node => node.className === 'diff'), preview);
page.run("clearPending(); selected = 'two'; snapshot({state:'awaiting',queued:0})");
show(page);
assert.match(page.pending().textContent, /-old\n\+new/, 'session switch recovers snapshot diff');
const reloaded = browser(); show(reloaded);
assert.match(reloaded.pending().textContent, /-old\n\+new/, 'fresh page recovers snapshot diff');
const turns = [
  {Role:'assistant',Content:JSON.stringify({action:'done',payload:'All set.\n```sh\necho ok\n```'})},
  {Role:'assistant',Content:JSON.stringify({action:'tool',toolname:'edit',reason:'Fix typo',payload:{path:'/file',old_string:'old',new_string:'<script>alert(1)</script>'}})},
  {Role:'assistant',Content:'<img src=x onerror=alert(1)>'}
];
page.run('renderTurns(' + JSON.stringify(turns) + ')');
assert.match(page.get('history').textContent, /Complete/);
assert.match(page.get('history').textContent, /Fix typo/);
assert.match(page.get('history').textContent, /\+<script>alert\(1\)<\/script>/);
function descendants(node) { return [node, ...node.children.flatMap(descendants)]; }
assert.match(page.get('history').textContent, /echo ok/);
assert.ok(!descendants(page.get('history')).some(node => ['script','img'].includes(node.tag)), 'hostile text never becomes markup');
page.run("activity({type:'reasoning',data:'first '}); activity({type:'reasoning',data:'second'})");
assert.match(page.get('live-progress').textContent, /first second/);
assert.equal(page.get('reasoning-preview').hidden, false);
assert.equal(page.get('reasoning-text').textContent, 'first second');
assert.equal(page.get('reasoning-text').scrollTop, 1000);
page.run("historyTotal=0; activity({type:'tool_call',data:{Name:'execute',Detail:'echo ok'}}); activity({type:'tool_output',data:'ok\\n'}); activity({type:'tool_result',data:{OK:true,Message:'finished'}})");
assert.equal(page.get('reasoning-preview').hidden, true);
assert.equal(page.get('reasoning-text').textContent, '');
assert.equal(page.get('live-progress').children.length, 4);
assert.match(page.get('live-progress').textContent, /echo ok/);
page.run('durable=' + JSON.stringify([{Role:'assistant',Kind:'output',Content:JSON.stringify({action:'tool',toolname:'execute',payload:'echo ok'})},{Role:'user',Kind:'tool_result',Content:'[execute result]\nCMD: echo ok\nok'}]) + '; paintConversation()');
assert.equal(page.get('history').children.length, 1, 'persisted tool result nests under its call');
assert.equal(page.get('live-progress').children.length, 4, 'durable refresh preserves the independent activity log');
assert.match(page.get('history').textContent, /Execution log/, 'streamed output survives durable refresh');
page.run('paintConversation()');
assert.equal(page.get('history').children.length, 1, 'repeat refresh remains idempotent');
assert.equal(descendants(page.get('history')).filter(n => n.tag === 'summary' && n.textContent === 'Result').length, 1);
page.run("historyTotal=2; activity({type:'tool_call',data:{Name:'execute',Detail:'echo ok'}})");
assert.equal(page.get('live-progress').children.length, 5, 'repeated command is a new activity event');
assert.equal(page.get('events').scrollTop, 1000, 'activity always follows latest');
assert.equal(page.get('conversation').scrollTop, 0, 'tool activity does not scroll chat');
page.get('conversation').scrollTop = 123;
page.run("activity({type:'output',data:'new response'})");
assert.equal(page.get('conversation').scrollTop, 123, 'received activity preserves reading position');
assert.equal(page.pending().children[0].id, 'pending-title', 'floating prompt has an accessible heading');
assert.deepEqual(descendants(page.pending()).filter(n => n.tag === 'button').map(n => n.textContent), ['Approve', 'Decline'], 'approval has no accidental dismiss action');
page.run("selected='one'; snapshot(" + JSON.stringify({state:'busy',queued:0,pending:{id:'cmd',kind:'confirm',title:'Execute this command?',tool:{Name:'execute',Target:'bash',Detail:'ls && sudo rm x',CommandSegments:['ls &&','sudo rm x'],UntrustedSegments:[1]},untrusted:['sudo']}}) + ')');
assert.deepEqual(descendants(page.pending()).filter(n => n.tag === 'button').map(n => n.textContent), ['Once', 'Trust session', 'Always', 'Decline'], 'command approval offers trust choices');
assert.match(page.pending().textContent, /not trusted: sudo/);
const marked = descendants(page.pending()).filter(n => n.tag === 'span' && n.className === 'cmd-untrusted');
assert.equal(marked.length, 1, 'only the untrusted segment is marked');
assert.match(marked[0].textContent, /sudo rm x/);
page.run("for(let i=0;i<120;i++) activity({type:'notice',data:'event '+i}); activity({type:'output',data:'x'.repeat(20000)}); activity({type:'output',data:'tail'})");
assert.equal(page.get('live-progress').children.length, 100, 'activity is bounded');
assert.equal(page.get('live-progress').lastElementChild.lastElementChild.textContent.length, 16000);
assert.ok(page.get('live-progress').lastElementChild.textContent.endsWith('tail'), 'stream chunks coalesce');
page.run("selected=''; controls()"); assert.equal(page.get('send').disabled, true);
page.run("selected='one'; $('message').value='  '; controls()"); assert.equal(page.get('send').disabled, true);
page.run("$('message').value='next'; snapshot({state:'busy',queued:1}); controls()");
assert.equal(page.get('send').disabled, false, 'busy runtime accepts queued sends');
page.run("action = kind => { globalThis.sent = kind; }; globalThis.sent = null; globalThis.prevented = false; composerKey({key:'Enter',isComposing:true,preventDefault(){globalThis.prevented=true}})");
assert.equal(page.run('sent'), null); assert.equal(page.run('prevented'), false);
page.run("composerKey({key:'Enter',shiftKey:true,preventDefault(){globalThis.prevented=true}})"); assert.equal(page.run('sent'), null);
page.run("composerKey({key:'Enter',preventDefault(){globalThis.prevented=true}})"); assert.equal(page.run('sent'), 'send');

function completion(content, action = 'done') {
  const p = browser();
  p.run('renderTurns(' + JSON.stringify([{Role:'assistant', Content:JSON.stringify({action, payload:content})}]) + ')');
  return p.get('history');
}
// The minimal DOM does not pretend to implement DOMPurify. Test fail-closed
// behavior here; an optional real DOM runner below exercises the actual bundles.
for (const content of ['# Heading\n**bold**', '<img src=x onerror=alert(1)>', 'x'.repeat(100001)]) {
  assert.equal(descendants(completion(content)).find(n => n.className === 'prose').textContent, content);
}
for (const url of ['javascript:alert(1)','data:text/html,evil','/relative','//example.com','mailto:a@example.com','https://','https://exa\tmple.com']) assert.equal(page.run('safeMarkdownURL(' + JSON.stringify(url) + ')'), false);
for (const url of ['https://example.com','HTTP://example.com/path']) assert.equal(page.run('safeMarkdownURL(' + JSON.stringify(url) + ')'), true);
const wiring = browser();
wiring.run(`globalThis.marked = {Renderer: class {}, parse(value, options) { globalThis.options = options; return 'untrusted parser output'; }};
  globalThis.DOMPurify = {isSupported:true, sanitize(value, config) {
    if (value !== 'untrusted parser output' || !config.RETURN_DOM_FRAGMENT || config.ALLOWED_TAGS.includes('img')) throw Error('unsafe configuration');
    const fragment = document.createElement('fragment'); fragment.textContent = 'sanitized'; fragment.querySelectorAll = () => []; return fragment;
  }};`);
assert.equal(wiring.run("completeProse('**bold**').textContent"), 'sanitized');
assert.equal(wiring.run("options.renderer.html({text:'<img src=x>'})"), '&lt;img src=x&gt;');
wiring.run("DOMPurify.sanitize = () => { throw Error('failure'); }");
assert.equal(wiring.run("completeProse('**bold**').textContent"), '**bold**');

// Optional test-only dependency, installed outside the repository. No build or
// production dependency; do not count the minimal DOM above as sanitizer tests.
if (process.env.PAI_WEB_JSDOM) {
  const {JSDOM} = require(process.env.PAI_WEB_JSDOM);
  const dom = new JSDOM(fs.readFileSync(__dirname + '/index.html', 'utf8'), {runScripts:'outside-only', url:'https://pai.example'});
  const window = dom.window;
  for (const file of ['marked.min.js','purify.min.js']) window.eval(fs.readFileSync(__dirname + '/vendor/' + file, 'utf8'));
  window.eval(source);
  const render = value => window.eval('completeProse(' + JSON.stringify(value) + ')');
  const rendered = render('# Heading\n\n**bold** and `code`\n\n- one\n  - nested\n\n| A | B |\n| - | - |\n| c | d |\n\n[site](https://example.com)');
  for (const tag of ['h1','strong','code','ul ul','table','a']) assert.ok(rendered.querySelector(tag), tag);
  assert.equal(rendered.querySelector('a').rel, 'noopener noreferrer');
  assert.equal(rendered.querySelector('a').target, '_blank');
  const hostile = render('<script>alert(1)</script><img src=https://remote.example/x><svg onload=alert(1)><math><style>x</style><iframe src=x><form><input>\n\n![alt](https://remote.example/image)\n[bad](javascript:evil)');
  assert.equal(hostile.querySelector('script,img,svg,math,style,iframe,form,input,[style],[onload],[onerror]'), null);
  assert.equal(hostile.querySelector('a[href]'), null);
  assert.match(hostile.textContent, /<script>/);
  assert.match(render('![alt](https://example.com/image)').textContent, /alt/);
  dom.window.close();
  console.log('real DOM markdown/sanitizer tests passed');
} else console.log('SKIP real DOM sanitizer tests (set PAI_WEB_JSDOM to an external jsdom module path)');

async function verifyHistory() {
  const p = browser();
  p.run("selected='one'; globalThis.calls=[]; api=async path => { calls.push(path); const offset=Number(/offset=(\\d+)/.exec(path)[1]),limit=Number(/limit=(\\d+)/.exec(path)[1]); return {turns:Array.from({length:Math.min(limit,250-offset)},(_,i)=>({Role:'user',Content:String(offset+i)})),total:250,cwd:'/fixed'}; }");
  p.get('conversation').scrollTop = 123;
  await p.run('history(true)'); assert.equal(p.get('history').children.length, 100);
  assert.equal(p.get('history').children[0].textContent, 'You150', 'initial load shows latest tail');
  assert.equal(p.get('conversation').scrollTop, 1000, 'initial history follows latest');
  p.get('conversation').scrollTop = 123;
  p.run("globalThis.originalPaint=paintConversation; paintConversation=()=>{originalPaint(); $('conversation').scrollHeight+=500;}");
  await p.run('history()'); assert.equal(p.get('history').children.length, 200);
  assert.equal(p.get('conversation').scrollTop, 623, 'earlier history offsets the added height to preserve the viewport');
  p.run('paintConversation=originalPaint');
  await p.run('history(true)'); assert.equal(p.get('history').children.length, 200, 'refresh retains earlier loaded pages');
  await p.run('history()'); assert.equal(p.get('history').children.length, 250);
  assert.equal(p.get('more-history').hidden, true);
  p.get('conversation').scrollTop = 50;
  await p.run('history(true)');
  assert.equal(p.get('conversation').scrollTop, 50, 'unchanged refresh preserves reading position');
  const card = p.get('history').firstElementChild;
  card.open = true;
  p.run("snapshot({state:'busy',queued:0}); scheduleHistory()");
  p.tick(3000); await p.flush();
  assert.equal(p.get('conversation').scrollTop, 50, 'timer reload does not drag reader');
  assert.equal(p.get('history').firstElementChild, card, 'unchanged reload preserves DOM identity');
  assert.equal(card.open, true);
  await p.run('refreshHistory()');
  assert.equal(p.get('history').firstElementChild, card, 'poll reload preserves DOM');
  p.run("globalThis.total=251; api=async path=>{const offset=Number(/offset=(\\d+)/.exec(path)[1]),limit=Number(/limit=(\\d+)/.exec(path)[1]); return {turns:Array.from({length:Math.min(limit,total-offset)},(_,i)=>({Role:'user',Content:String(offset+i)})),total,cwd:'/fixed'};}");
  await p.run('history(true)');
  assert.equal(p.get('conversation').scrollTop, 50, 'new durable content preserves reader position');
  p.get('conversation').scrollTop = 1050;
  p.run('total++');
  await p.run('history(true)');
  assert.equal(p.get('conversation').scrollTop, 1500, 'new durable content follows near bottom');
  p.run("connect=()=>{}; loadSnapshot=async()=>{}; globalThis.originalSessions=sessions; sessions=async()=>{}; $('message').value='hello'; api=async()=>({})");
  p.get('conversation').scrollTop = 50;
  await p.run("action('send')");
  assert.equal(p.get('conversation').scrollTop, 1500, 'sending follows latest immediately');
  p.run("api=async()=>({turns:[{Role:'user',Content:'selected history'}],total:1,cwd:'/fixed'})");
  await p.run("select('two')");
  assert.equal(p.get('conversation').scrollTop, 1500, 'selecting a session follows latest');
  p.run('sessions = originalSessions');
  p.run("api=async()=>({sessions:[],total:0})"); await p.run('sessions(true)');
  assert.match(p.get('sessions-status').textContent, /No saved sessions/);
  p.run("api=async()=>{throw new Error('offline')}");
  await assert.rejects(p.run('sessions(true)'));
  assert.match(p.get('sessions-status').textContent, /Could not load/);
}
async function verifyMetadata() {
  const p = browser();
  p.run("connect=()=>{}; loadSnapshot=async()=>{}; api=async()=>({turns:[],total:0,cwd:'/fixed',role:'custom',model:'provider:model',api_key:'secret'})");
  await p.run("select('one', '/initial')");
  assert.equal(p.get('title').textContent, 'one');
  assert.equal(p.get('workspace').textContent, 'Workspace: /fixed');
  assert.equal(p.get('session-role-info').textContent, 'role: custom');
  assert.equal(p.get('session-role-info').title, 'Saved role: custom');
  assert.equal(p.get('session-model-info').textContent, 'model: provider:model');
  assert.equal(p.get('session-model-info').title, 'Saved model: provider:model');
  assert.equal(p.get('session-model-info').hidden, false);
  p.run("api=()=>new Promise(resolve=>globalThis.finishHistory=resolve)");
  const stale = p.run("select('two')");
  for (const field of ['role','model']) {
    assert.equal(p.get('session-' + field + '-info').hidden, true, 'selection clears saved metadata while loading');
    assert.equal(p.get('session-' + field + '-info').textContent, '');
  }
  p.run("api=async()=>({turns:[],total:0})");
  await p.run("select('three')");
  p.run("finishHistory({turns:[],total:0,role:'stale',model:'stale'})");
  await stale;
  for (const field of ['role','model']) {
    assert.equal(p.get('session-' + field + '-info').hidden, true, 'missing fields stay hidden; stale response cannot populate them');
    assert.equal(p.get('session-' + field + '-info').textContent, '');
  }
  p.run("sessionMetadata({role:'<script>literal</script>',model:42})");
  assert.equal(p.get('session-role-info').textContent, 'role: <script>literal</script>');
  assert.equal(p.get('session-model-info').hidden, true);
  p.run("sessionMetadata({role:'  ',model:' model '})");
  assert.equal(p.get('session-role-info').hidden, true);
  assert.equal(p.get('session-model-info').textContent, 'model: model');
}
async function verifyCreate() {
  const p = browser();
  p.run("api=async()=>({roles:['custom','configured'],default_role:'configured'})");
  await p.run('roles()');
  assert.equal(p.get('session-role').value, 'configured');
  assert.deepEqual(p.get('session-role').children.map(option => option.value), ['custom','configured']);
  p.get('session-role').value = 'custom';
  await p.run('roles()');
  assert.equal(p.get('session-role').value, 'custom');
  p.get('session-name').value = ' new '; p.get('session-cwd').value = ' /workspace ';
  p.run("globalThis.calls=[]; api=async (path,body)=>{calls.push([path,body]);return {name:'new',cwd:'/resolved'};}; select=async(name,cwd)=>calls.push(['select',name,cwd]); sessions=async reset=>calls.push(['sessions',reset]);");
  await p.run('createSession({preventDefault(){}})');
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(calls)')), [['create',{name:'new',working_dir:'/workspace',role:'custom'}],['select','new','/resolved'],['sessions',true]]);
  assert.equal(p.get('create-session').disabled, false);
  p.run("api=async()=>{throw new Error('not a directory')}");
  await p.run('createSession({preventDefault(){}})');
  assert.match(p.get('error').textContent, /not a directory/);
  assert.equal(p.get('session-role').value, 'custom');
  assert.equal(p.get('session-name').value, ' new '); assert.equal(p.get('session-cwd').value, ' /workspace ');
  p.get('session-cwd').value = 'bad\npath'; p.run('calls=[]');
  await p.run('createSession({preventDefault(){}})');
  assert.match(p.get('error').textContent, /control characters/);
  p.get('session-cwd').value = '';
  p.run("api=async(path,body)=>{calls.push([path,body]);return {name:'new',cwd:'/launch'}}");
  await p.run('createSession({preventDefault(){}})');
  assert.equal(p.run('calls[0][1].working_dir'), '');
  p.run("calls=[]; api=()=>new Promise(resolve=>globalThis.finish=resolve)");
  const request = p.run('createSession({preventDefault(){}})');
  await p.run('createSession({preventDefault(){}})');
  p.run("generation++; finish({name:'new',cwd:'/launch'})"); await request;
  assert.equal(p.run("calls.some(c=>c[0]==='select')"), false, 'stale create must not change selection');
}
for (const [command, expected] of [
  ['echo a && echo b || echo c; cat f | sort', ['echo a &&','echo b ||','echo c;','cat f |','sort']],
  ["grep 'a|b' f && echo \"x;y\"", ["grep 'a|b' f &&",'echo "x;y"']],
  ['echo a\\|b; (echo x && echo y) | cat', ['echo a\\|b;','(echo x && echo y) |','cat']],
  ['echo x 2>&1 && cat', ['echo x 2>&1 &&','cat']],
  ['echo "unfinished | cat', ['echo "unfinished | cat']],
  ['cat <<EOF; text', ['cat <<EOF; text']]
]) assert.deepEqual(JSON.parse(page.run('JSON.stringify(shellSegments(' + JSON.stringify(command) + '))')), expected);
for (const type of ['tool_call','done','ask','terminate','output','prompt','stopped']) {
  page.run("activity({type:'reasoning',data:'<script>current</script>'}); activity({type:" + JSON.stringify(type) + ",data:{Name:'execute'}})");
  assert.equal(page.get('reasoning-preview').hidden, true);
  assert.equal(page.get('reasoning-text').textContent, '');
}
// Validate source nesting: browser parsers silently repair malformed closing tags.
const markup = fs.readFileSync(__dirname + '/index.html', 'utf8');
const stack = [], voidTags = new Set(['meta','link','input','br','hr','img']);
for (const match of markup.matchAll(/<\/?([a-z][a-z0-9-]*)\b[^>]*>/gi)) {
  const tag = match[1].toLowerCase();
  assert.ok(!match[0].slice(1).includes('<'), 'malformed tag: ' + match[0]);
  if (match[0].startsWith('</')) assert.equal(stack.pop(), tag, 'misnested ' + tag);
  else if (!voidTags.has(tag)) stack.push(tag);
}
assert.deepEqual(stack, []);
assert.match(markup, /Apply<\/button>\s*<\/form>/);
assert.match(markup, /<input id="session-model"[^>]*list="model-options"[^>]*placeholder="provider:model"/);
assert.match(markup, /Suggestions are the configured default and saved session models/);
assert.match(markup, /Type any configured provider:model/);
const usagePage = browser();
usagePage.run("snapshot({state:'awaiting',usage_calls:0})");
assert.equal(usagePage.get('token-usage').hidden, true);
const usageState = {state:'awaiting',usage_calls:2,usage:{Prompt:20,Completion:3,Total:23},total_usage:{Prompt:30,Completion:5,Total:35}};
for (let i = 0; i < 2; i++) {
  usagePage.run(`snapshot(${JSON.stringify(usageState)})`);
  assert.equal(usagePage.get('token-usage').textContent, 'Tokens · sent 30 · received 5 · total 35 (this run)');
  assert.match(usagePage.get('token-usage').title, /Latest request: sent 20 · received 3 · total 23/);
}
usagePage.run("snapshot({state:'starting',usage_calls:0})");
assert.equal(usagePage.get('token-usage').hidden, true);
assert.equal(usagePage.get('token-usage').textContent, '');

async function verifyModels() {
  const p = browser();
  p.run("selected='one'; modelBlocked=false; sessionMetadata({model:'test:saved'}); models=async()=>{};");
  assert.equal(p.get('session-model').value, 'test:saved');
  p.run("$('session-model').value='test:custom'; modelDirty=true; sessionMetadata({model:'test:changed'}); snapshot({state:'busy',model:'test:live'});");
  assert.equal(p.get('session-model').value, 'test:custom', 'refresh preserves draft');
  assert.equal(p.get('session-model').disabled, true);
  assert.equal(p.get('apply-model').disabled, true);
  for (const s of [{state:'starting'}, {state:'awaiting',pending:{id:'p',kind:'ask'}}]) {
    p.run(`snapshot(${JSON.stringify(s)})`);
    assert.equal(p.get('session-model').disabled, true);
  }
  p.run("snapshot({state:'awaiting',model:'test:live'});");
  assert.equal(p.get('apply-model').disabled, false);
  p.run("api=async()=>{const e=new Error('configured provider required');e.status=400;throw e}; loadSnapshot=async()=>{};");
  await p.run("applyModel({preventDefault(){}})");
  assert.match(p.get('error').textContent, /configured provider/);
  assert.equal(p.get('session-model').value, 'test:custom');
  p.run("$('session-model').value=' test : custom '; history=async()=>sessionMetadata({model:'test:custom'}); connect=()=>{}; api=async(path,body)=>{if(body.model!=='test:custom') throw Error('not normalized'); return {ok:true};};");
  await p.run("applyModel({preventDefault(){}})");
  assert.equal(p.get('session-model').value, 'test:custom');
  assert.equal(p.run('modelDirty'), false);
  p.run("$('session-model').value='test:unsaved'; modelDirty=true; api=()=>new Promise(resolve=>globalThis.finishModel=resolve); modelControls();");
  const stale = p.run("applyModel({preventDefault(){}})");
  p.run("history=async()=>sessionMetadata({model:'test:other'});");
  await p.run("select('two')");
  p.run("finishModel({ok:true})"); await stale;
  assert.equal(p.get('session-model').value, 'test:other', 'stale switch cannot overwrite selection');
  assert.equal(p.run('modelDirty'), false);
}
Promise.all([verifyHistory(), verifyMetadata(), verifyCreate(), verifyModels()]).then(() => console.log('web UI tests passed')).catch(e => { console.error(e); process.exitCode = 1; });
