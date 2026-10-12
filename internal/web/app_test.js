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
    this.style = {};
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
    const storage = new Map();
    const context = vm.createContext({URL, Date: {now: () => now}, EventSource: MockEventSource,
      localStorage: {getItem: key => (storage.has(key) ? storage.get(key) : null), setItem: (key, value) => storage.set(key, String(value)), removeItem: key => storage.delete(key)},
      confirm: () => true,
      prompt: () => 'renamed',
      setInterval: (callback, delay) => { assert.equal(delay, 1000); intervals.set(++timerID, callback); return timerID; },
      clearInterval: id => intervals.delete(id), setTimeout: callback => { timeouts.set(++timerID, callback); return timerID; }, clearTimeout: id => timeouts.delete(id), document: {
    body: new Element('body'),
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
page.run("selected = 'one'; snapshot({state:'busy',queued:0,pending:{id:'1',kind:'confirm',title:'Proceed?',tool:{Name:'edit',Target:'/f',Detail:'d',Reason:'r'}}})");
const promptTitle = page.pending().children[0];
for (let i = 0; i < 5; i++) {
  page.run("snapshot({state:'busy',queued:2,pending:{id:'1',kind:'confirm',title:'Proceed?',tool:{Name:'edit',Target:'/f',Detail:'d',Reason:'r'}}})");
  assert.equal(page.pending().children[0], promptTitle, 'same prompt keeps its card (focus/draft preserved)');
}
page.run("snapshot({state:'busy',queued:0,pending:{id:'2',kind:'confirm',title:'Next?',tool:{Name:'edit',Target:'/f',Detail:'d',Reason:'r'}}})");
assert.notEqual(page.pending().children[0], promptTitle, 'a new prompt rebuilds the card');
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
assert.ok(!page.get('live-progress').textContent.includes('first second'), 'live reasoning stays out of the activity log');
assert.equal(page.get('reasoning-preview').hidden, false);
assert.equal(page.get('reasoning-text').textContent, 'first second');
assert.equal(page.get('reasoning-text').scrollTop, 1000);
page.run("historyTotal=0; activity({type:'tool_call',data:{Name:'execute',Detail:'echo ok'}}); activity({type:'tool_output',data:'ok\\n'}); activity({type:'tool_result',data:{OK:true,Message:'finished'}})");
assert.equal(page.get('reasoning-preview').hidden, true);
assert.equal(page.get('reasoning-text').textContent, '');
assert.equal(page.get('live-progress').children.length, 4, 'finished reasoning block plus three tool cards');
assert.match(page.get('live-progress').textContent, /first second/, 'finished reasoning is appended to the activity log');
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
page.run("selected='one'; snapshot(" + JSON.stringify({state:'busy',queued:0,pending:{id:'pth',kind:'confirm',title:'Create /etc/x?',tool:{Name:'write',Target:'/etc/x'},untrusted:['/etc'],trust_target:'path'}}) + ')');
assert.match(page.pending().textContent, /outside trusted paths: \/etc/, 'a path confirmation is worded as a path, not a command');
page.run("selected='one'; snapshot(" + JSON.stringify({state:'busy',queued:0,pending:{id:'dir',kind:'confirm',title:'Execute this command?',tool:{Name:'execute',Target:'bash',Dir:'/var/log',Detail:'df -h'}}}) + ')');
assert.match(page.pending().textContent, /\(in \/var\/log\)/, 'the confirmation shows the working directory');
page.run("for(let i=0;i<120;i++) activity({type:'notice',data:'event '+i}); activity({type:'output',data:'x'.repeat(20000)}); activity({type:'output',data:'tail'})");
assert.equal(page.get('live-progress').children.length, 60, 'activity is bounded');
assert.equal(page.get('live-progress').lastElementChild.lastElementChild.textContent.length, 16000);
assert.ok(page.get('live-progress').lastElementChild.textContent.endsWith('tail'), 'stream chunks coalesce');
page.run("activity({type:'notice',data:'trusted from now on: /srv'}); toast('b'); toast('c')");
assert.equal(page.run('toastHost.children.length'), 3, 'toasts are bounded');
assert.equal(page.run("toastHost.firstElementChild.className"), 'toast');
assert.equal(page.run("toastHost.firstElementChild.textContent"), 'trusted from now on: /srv', 'a notice is toasted, not only logged');
page.run("selected=''; localStorage.setItem('pai.session','two'); restoreSession([{name:'one',cwd:'/a'},{name:'two',cwd:'/b'}])");
assert.equal(page.run('selected'), 'two', 'a reload reopens the remembered session');
page.run("selected=''; localStorage.setItem('pai.session','ghost'); restoreSession([{name:'one',cwd:'/a'}])");
assert.equal(page.run('selected'), '', 'a forgotten session is not reopened');
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
// The model's reason is Markdown too: it must go through the same renderer as
// the payload, keep its muted styling, and never be left as a plain-text span.
wiring.run("renderTurns(" + JSON.stringify([{Role:'assistant', Content:JSON.stringify({action:'done', payload:'**answer**', reason:'checked **disk**'})}]) + ")");
const reasonNodes = descendants(wiring.get('history')).filter(n => /\breason-text\b/.test(n.className));
assert.equal(reasonNodes.length, 1);
assert.match(reasonNodes[0].className, /\bmarkdown\b/, 'reason is rendered as Markdown');
assert.equal(reasonNodes[0].textContent, 'sanitized');
assert.ok(!descendants(reasonNodes[0]).some(n => n.tag === 'span'), 'no plain-text span fallback for a rendered reason');
wiring.run("DOMPurify.sanitize = () => { throw Error('failure'); }");
assert.equal(wiring.run("completeProse('**bold**').textContent"), '**bold**');

// A payload whose fields are all known renders labelled fields rather than a raw
// JSON dump, which is what the search tools' object payloads depend on.
const search = browser();
search.run('renderTurns(' + JSON.stringify([
  {Role:'assistant', Content:JSON.stringify({action:'tool', toolname:'papersearch', payload:{source:'arxiv', query:'private decision tree', limit:5, sort:'relevance', year_from:2023}})}
]) + ')');
const searchHistory = search.get('history');
assert.match(searchHistory.textContent, /papersearch/, 'the tool name is shown');
assert.match(searchHistory.textContent, /private decision tree/, 'the query value is shown');
for (const label of ['source','query','sort','year from']) assert.match(searchHistory.textContent, new RegExp(label), 'papersearch field label ' + label);
assert.ok(!descendants(searchHistory).some(n => n.tag === 'summary' && n.textContent === 'Raw details'), 'known payload fields are not dumped as raw JSON');

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
  await p.run('history(true)'); assert.equal(p.get('history').children.length, 30);
  assert.equal(p.get('history').children[0].textContent, 'You220', 'initial load shows latest tail');
  assert.equal(p.get('conversation').scrollTop, 1000, 'initial history follows latest');
  p.get('conversation').scrollTop = 123;
  p.run("globalThis.originalPaint=paintConversation; paintConversation=()=>{originalPaint(); $('conversation').scrollHeight+=500;}");
  await p.run('history()'); assert.equal(p.get('history').children.length, 60);
  assert.equal(p.get('conversation').scrollTop, 623, 'earlier history offsets the added height to preserve the viewport');
  p.run('paintConversation=originalPaint');
  await p.run('history(true)'); assert.equal(p.get('history').children.length, 60, 'refresh retains earlier loaded pages');
  await p.run('history()'); assert.equal(p.get('history').children.length, 90);
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
  assert.equal(p.run('savedRole'), 'custom');
  assert.equal(p.get('session-model').value, 'provider:model');
  p.run("api=()=>new Promise(resolve=>globalThis.finishHistory=resolve)");
  const stale = p.run("select('two')");
  // Selection clears saved metadata while the new history loads.
  assert.equal(p.run('savedRole'), '');
  assert.equal(p.get('session-model').value, '');
  p.run("api=async()=>({turns:[],total:0})");
  await p.run("select('three')");
  p.run("finishHistory({turns:[],total:0,role:'stale',model:'stale'})");
  await stale;
  // A stale response cannot populate the newer selection.
  assert.equal(p.run('savedRole'), '');
  assert.equal(p.get('session-model').value, '');
  p.run("sessionMetadata({role:'<script>literal</script>',model:42})");
  assert.equal(p.run('savedRole'), '<script>literal</script>');
  assert.equal(p.get('session-model').value, '', 'a non-string model is ignored');
  p.run("sessionMetadata({role:'  ',model:' model '})");
  assert.equal(p.run('savedRole'), '', 'a blank role clears the saved role');
  assert.equal(p.get('session-model').value, 'model');
}
async function verifyCreate() {
  const p = browser();
  p.run("api=async()=>({roles:['custom','configured'],default_role:'configured'})");
  await p.run('roles()');
  assert.equal(p.run('selectedRole()'), 'configured', 'the default role is preselected');
  assert.equal(p.run('JSON.stringify([...$("session-role").children].map(label => label.children[0].value))'), '["custom","configured"]');
  assert.equal(p.run('$("session-role").children.every(label => label.children[0].type === "radio")'), true, 'choices are radios');
  p.run('setSelectedRole("custom")');
  await p.run('roles()');
  assert.equal(p.run('selectedRole()'), 'custom', 'a rerender keeps the unsent pick');
  p.get('session-name').value = ' new '; p.get('session-cwd').value = ' /workspace ';
  p.run("globalThis.calls=[]; api=async (path,body)=>{calls.push([path,body]);return {name:'new',cwd:'/resolved'};}; select=async(name,cwd)=>calls.push(['select',name,cwd]); sessions=async reset=>calls.push(['sessions',reset]);");
  await p.run('createSession({preventDefault(){}})');
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(calls)')), [['create',{name:'new',working_dir:'/workspace',role:'custom'}],['select','new','/resolved'],['sessions',true]]);
  assert.equal(p.get('create-session').disabled, false);
  p.run("api=async()=>{throw new Error('not a directory')}");
  await p.run('createSession({preventDefault(){}})');
  assert.match(p.get('error').textContent, /not a directory/);
  assert.equal(p.run('selectedRole()'), 'custom');
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
assert.match(markup, /<input id="session-model"[^>]*placeholder="provider:model"/);
assert.match(markup, /<ul id="model-options"[^>]*role="listbox"/);
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
const budgetState = {state:'awaiting',usage_calls:3,usage:{Prompt:78000,Completion:100,Total:78100},total_usage:{Prompt:200000,Completion:5000,Total:205000},context_tokens:128000};
usagePage.run(`snapshot(${JSON.stringify(budgetState)})`);
assert.match(usagePage.get('token-usage').textContent, /context 78k \/ 128k \(61%\)/, 'context fill appears when compaction is configured');
assert.match(usagePage.get('token-usage').title, /exceeds 128000 tokens/);

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
// The rewind control lives in the card's async click handler, so this check is
// async (never await at the top level: this file is CommonJS).
async function verifyRewind() {
  const p = browser();
  p.run("selected='one'; historyTotal=9; historyOffset=2; sessionBusy=false; durable=[{Role:'user',Kind:'input',Content:'do x'},{Role:'assistant',Kind:'output',Content:JSON.stringify({action:'done',payload:'ok'})}]; api=async(path,body)=>{globalThis.rewound=[path,body];return {ok:true};}; loadSnapshot=async()=>{}; refreshHistory=async()=>{}; sessions=async()=>{}; renderTurns(durable)");
  const button = p.get('history').children[0].children.find(node => node.className === 'rewind');
  assert.ok(button, 'a user input card offers a rewind button');
  const pending = button.onclick();
  assert.ok(p.run("document.body.children.find(node => node.className === 'rewind-confirm')"), 'rewind asks whether to keep a backup');
  p.run("document.body.children.find(node => node.className === 'rewind-confirm').children.filter(node => node.tag === 'button')[0].onclick()");
  await pending;
  assert.equal(p.run('rewound[0]'), 'rewind');
  assert.equal(p.run('rewound[1].name'), 'one');
  assert.equal(p.run('rewound[1].keep'), 2, 'rewind posts the persisted turn index');
  assert.equal(p.run('rewound[1].backup'), true, 'the backup choice is sent');
  assert.equal(p.get('message').value, 'do x', 'the instruction returns to the composer');
}
// Role change and rename are manager operations gated on an idle worker.
async function verifyRole() {
  const p = browser();
  p.run("selected='one'; modelBlocked=false; savedRole='devops'; $('session-role-select').replaceChildren(...['devops','coder'].map(r=>{const o=document.createElement('option');o.value=r;o.textContent=r;return o;})); $('session-role-select').value='devops'; api=async(path,body)=>{globalThis.roleCall=[path,body];return {ok:true};}; loadSnapshot=async()=>{}; refreshHistory=async()=>{}; controls()");
  assert.equal(p.get('apply-role').disabled, true, 'the unchanged role cannot be applied');
  p.run("$('session-role-select').value='coder'; roleDirty=true; controls()");
  assert.equal(p.get('apply-role').disabled, false, 'a new role can be applied');
  // A background refresh must not roll the unsent choice back before Apply.
  p.run("sessionMetadata({role:'devops'})");
  assert.equal(p.get('session-role-select').value, 'coder', 'a refresh keeps the unsent role');
  await p.run("applyRole({preventDefault(){}})");
  assert.equal(p.run('roleCall[0]'), 'role');
  assert.equal(p.run('roleCall[1].role'), 'coder');
  assert.equal(p.run('roleCall[1].name'), 'one');
  assert.equal(p.run('roleDirty'), false, 'applying the role clears the draft flag');
  assert.equal(p.run("selected='one'; controls(); $('rename-session').hidden"), false, 'rename is offered for a selected session');
  assert.equal(p.run("selected=''; controls(); $('rename-session').hidden"), true, 'rename is hidden without a session');
}
// The sidebar lists each session with a delete affordance, guarded by a prompt.
async function verifySessionList() {
  const p = browser();
  p.run("selected='one'; globalThis.calls=[]; api=async()=>({sessions:[{name:'one',cwd:'/a'},{name:'two',cwd:'/b'}],total:2,default_cwd:'/a'})");
  await p.run('sessions(true)');
  const rows = p.get('sessions').children;
  assert.equal(rows.length, 2, 'each saved session renders a row');
  assert.ok(rows[0].classes.has('selected'), 'the open session row is marked selected');
  assert.ok(!rows[1].classes.has('selected'));
  assert.deepEqual(rows[0].children.map(node => node.textContent), ['one', '🗑']);
  assert.equal(rows[0].children[1].title, 'Delete this session');
  p.run("globalThis.del=[]; api=async(path,body)=>{del.push([path,body]);return {ok:true};}; sessions=async()=>{}; confirm=()=>false");
  await rows[1].children[1].onclick({stopPropagation(){}});
  assert.equal(p.run('del.length'), 0, 'declining the prompt deletes nothing');
  p.run('confirm=()=>true');
  await rows[1].children[1].onclick({stopPropagation(){}});
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(del)')), [['delete', {name:'two'}]], 'a confirmed delete removes the right session');
  assert.equal(p.run('selected'), 'one', 'deleting another session keeps the open one');
}
// The model field is a combobox: focus shows every suggestion, typing filters,
// and choosing one fills the field (free text is still allowed).
function verifyModelOptions() {
  const p = browser();
  p.run("modelList=['deepseek:deepseek-v4-flash','openai:gpt-4o','llama:spark']; selected='one'; $('session-model').disabled=false; renderModelOptions('')");
  let list = p.get('model-options');
  assert.equal(list.hidden, false, 'focus opens the suggestion list');
  assert.deepEqual(list.children.map(node => node.textContent), ['deepseek:deepseek-v4-flash', 'openai:gpt-4o', 'llama:spark']);
  p.run("renderModelOptions('openai')");
  assert.deepEqual(p.get('model-options').children.map(node => node.textContent), ['openai:gpt-4o'], 'typing filters the list');
  p.run("renderModelOptions('nope')");
  assert.equal(p.get('model-options').hidden, true, 'no matches closes the list');
  p.run("renderModelOptions(''); chooseModel('llama:spark')");
  assert.equal(p.get('session-model').value, 'llama:spark', 'choosing a suggestion fills the field');
  assert.equal(p.get('model-options').hidden, true, 'choosing closes the list');
  assert.equal(p.run('modelDirty'), true, 'a chosen suggestion is an unsaved edit');
}
// Deleting the open session returns the workspace to its unselected state.
async function verifyDeleteOpen() {
  const p = browser();
  p.run("selected='one'; globalThis.calls=[]; api=async(path,body)=>{calls.push([path,body]);return {ok:true};}; sessions=async reset=>calls.push(['sessions',reset]); disconnect=()=>{}; confirm=()=>true; localStorage.setItem('pai.session','one')");
  await p.run("deleteSession('one')");
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(calls)')), [['delete', {name:'one'}], ['sessions', true]], 'delete then reload the list');
  assert.equal(p.run('selected'), '', 'the workspace is cleared');
  assert.equal(p.get('title').textContent, 'Select or create a session');
  assert.equal(p.run("localStorage.getItem('pai.session')"), null, 'the remembered session is forgotten');
}

async function verifyWorkingDir() {
  const p = browser();
  p.run("selected='one'; modelBlocked=false; sessionMetadata({cwd:'/old',role:'devops',model:'test:model'}); controls()");
  assert.equal(p.get('session-working-dir').value, '/old');
  assert.equal(p.get('apply-working-dir').disabled, true, 'unchanged workspace needs no Apply');
  p.run("$('session-working-dir').value='/new'; cwdDirty=true; workingDirControls(); sessionMetadata({cwd:'/old'})");
  assert.equal(p.get('session-working-dir').value, '/new', 'history refresh preserves a workspace draft');
  assert.equal(p.get('apply-working-dir').disabled, false);
  for (const s of [{state:'busy'}, {state:'starting'}, {state:'awaiting',queued:1}, {state:'awaiting',pending:{id:'1',kind:'confirm'}}]) {
    p.run('snapshot(' + JSON.stringify(s) + ')');
    assert.equal(p.get('session-working-dir').disabled, true, 'workspace is idle-only');
    assert.equal(p.get('apply-working-dir').disabled, true);
  }
  p.run("snapshot({state:'awaiting',queued:0}); globalThis.calls=[]; sessions=async()=>{}; connect=()=>{}; history=async()=>sessionMetadata({cwd:'/normalized/new'}); loadSnapshot=async()=>{}; api=async(path,body)=>{calls.push([path,body]);return {ok:true}};");
  await p.run("applyWorkingDir({preventDefault(){}})");
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(calls)')), [['working-dir', {name:'one', working_dir:'/new'}]]);
  assert.equal(p.get('session-working-dir').value, '/normalized/new', 'Apply shows the server-normalized path');
  assert.equal(p.run('cwdDirty'), false);
  assert.equal(p.get('apply-working-dir').disabled, true);

  p.run("$('session-working-dir').value='/missing'; cwdDirty=true; workingDirControls(); api=async()=>{throw Error('working directory does not exist')}; loadSnapshot=async()=>sessionMetadata({cwd:'/normalized/new'})");
  await p.run("applyWorkingDir({preventDefault(){}})");
  assert.match(p.get('error').textContent, /does not exist/);
  assert.equal(p.get('session-working-dir').value, '/missing', 'failed validation preserves the draft');
  assert.equal(p.run('savedCwd'), '/normalized/new', 'failed Apply leaves persisted metadata unchanged');
  p.run("$('session-working-dir').value='  '; workingDirControls()");
  assert.equal(p.get('apply-working-dir').disabled, true, 'blank paths are disabled');

  p.run("api=async()=>({turns:[],total:0,cwd:'/two'}); history=async()=>sessionMetadata({cwd:'/two'}); loadSnapshot=async()=>{}");
  await p.run("select('two')");
  assert.equal(p.get('session-working-dir').value, '/two');
  p.run('clearWorkspace()');
  assert.equal(p.get('session-working-dir').value, '');
  assert.equal(p.get('session-working-dir').disabled, true);

  const stale = browser();
  stale.run("selected='one'; modelBlocked=false; sessionMetadata({cwd:'/old'}); $('session-working-dir').value='/new'; cwdDirty=true; workingDirControls(); api=()=>new Promise(resolve=>globalThis.finishApply=resolve); history=async()=>sessionMetadata({cwd:'/two'}); loadSnapshot=async()=>{}; connect=()=>{};");
  const applying = stale.run("applyWorkingDir({preventDefault(){}})");
  assert.equal(stale.get('apply-working-dir').disabled, true, 'Apply is disabled while the request is pending');
  await stale.run("select('two')");
  stale.run('finishApply({ok:true})');
  await applying;
  assert.equal(stale.get('session-working-dir').value, '/two', 'a stale Apply response cannot replace another session workspace');
}

async function verifySendFailure() {
  const p = browser();
  p.run(`selected='one'; $('login').hidden=true; $('message').value='hello';
    sessions=async()=>{}; history=async()=>{};
    api=async path => path === 'send' ? {ok:true} :
      {name:'one',state:'stopped',queued:0,error:'session working directory: directory does not exist'};`);
  await p.run("action('send')");
  assert.match(p.get('state').textContent, /directory does not exist/, 'startup failure appears even when Send returned 202');
  assert.equal(p.get('run-progress').hidden, true);
  assert.equal(p.run('sessionBusy'), false, 'failed startup does not leave the composer busy');
  p.connections[0].onerror();
  await p.flush();
  assert.match(p.get('state').textContent, /directory does not exist/, 'SSE reconnect retains the stopped failure snapshot');
}

async function verifyComposer() {
  const p = browser();
  p.run("selected=''; controls()");
  assert.equal(p.get('trust-all').disabled, true, 'trust switch needs a session');
  assert.equal(p.get('apply-plan').disabled, true, 'auto-reply needs a session');
  p.run("selected='one'; controls()");
  assert.equal(p.get('trust-all').disabled, false);
  assert.equal(p.get('apply-plan').disabled, false, 'auto-reply needs no draft');
  assert.equal(p.get('send').disabled, true, 'send still needs a draft');

  // The server's snapshot is the source of truth for the switch.
  p.run("snapshot({state:'awaiting',queued:0,trust_all:true})");
  assert.equal(p.get('trust-all').checked, true, 'snapshot mirrors trust_all');
  p.run("snapshot({state:'awaiting',queued:0,trust_all:false})");
  assert.equal(p.get('trust-all').checked, false, 'snapshot clears trust_all');

  // Apply plan sends a canned instruction and leaves the draft alone.
  const auto = browser();
  auto.run(`selected='one'; $('message').value='keep me'; globalThis.calls=[];
    api=async(path,body)=>{calls.push([path,body]); return {};};
    sessions=async()=>{}; loadSnapshot=async()=>{}; connect=()=>{};`);
  await auto.run('action("send", AUTO_REPLY)');
  assert.equal(auto.run('calls.length'), 1, 'one request');
  assert.equal(auto.run('JSON.stringify(calls[0])'), '[\"send\",{\"name\":\"one\",\"text\":\"Apply your plan.\"}]');
  assert.equal(auto.get('message').value, 'keep me', 'canned reply preserves the draft');

  // Toggling the switch posts the new state.
  const toggle = browser();
  toggle.run(`selected='one'; globalThis.calls=[];
    api=async(path,body)=>{calls.push([path,body]); return {};};
    loadSnapshot=async()=>{};`);
  await toggle.run('setTrustAll(true)');
  assert.equal(toggle.run('JSON.stringify(calls[0])'), '[\"trust-all\",{\"name\":\"one\",\"trust_all\":true}]');
  assert.equal(toggle.get('trust-all').checked, true, 'switch reflects the accepted state');
}

async function verifyJumpButtons() {
  const p = browser();
  p.run(`$('conversation').scrollHeight=1000; $('conversation').clientHeight=400; $('conversation').scrollTop=600;
    $('history').replaceChildren(...[1,2].map(()=>{const d=document.createElement('div');d.className='turn user';return d;}));
    updateJumpButtons();`);
  assert.equal(p.get('prev-input').hidden, false, 'Up appears with instructions');
  assert.equal(p.get('to-bottom').hidden, true, 'Down hides at the newest message');
  p.run("$('conversation').scrollTop=0; updateJumpButtons()");
  assert.equal(p.get('to-bottom').hidden, false, 'Down appears when scrolled up');
  p.run('jumpToBottom()');
  assert.equal(p.get('to-bottom').hidden, true, 'Down hides after jumping to the end');
  assert.equal(p.get('conversation').scrollTop, 1000, 'Down scrolls to the end');
  p.run("$('history').replaceChildren(); updateJumpButtons()");
  assert.equal(p.get('prev-input').hidden, true, 'Up hides without instructions');
}

Promise.all([verifyWorkingDir(), verifySendFailure(), verifyHistory(), verifyMetadata(), verifyCreate(), verifyModels(), verifyRewind(), verifyRole(), verifySessionList(), verifyDeleteOpen(), verifyModelOptions(), verifyComposer(), verifyJumpButtons()]).then(() => console.log('web UI tests passed')).catch(e => { console.error(e); process.exitCode = 1; });
