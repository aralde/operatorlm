// ─────────────────────────────────────────────────────────────
//  OperatorLM settings · client logic
// ─────────────────────────────────────────────────────────────

const $  = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// ── Theme toggle ────────────────────────────────────────────
(function initTheme() {
  const KEY = 'operatorlm.theme';
  const apply = (t) => document.documentElement.setAttribute('data-theme', t);
  const current = () => document.documentElement.getAttribute('data-theme') || 'dark';
  const wire = () => {
    const btn = document.getElementById('theme-toggle');
    if (!btn) return;
    btn.addEventListener('click', () => {
      const next = current() === 'light' ? 'dark' : 'light';
      apply(next);
      try { localStorage.setItem(KEY, next); } catch (_) {}
    });
  };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wire, { once: true });
  } else {
    wire();
  }
})();

// ── HTTP helper ─────────────────────────────────────────────
async function api(method, url, body) {
  // X-OperatorLM-Admin is a CSRF marker: the server requires it on
  // state-changing admin endpoints. Sending it unconditionally is harmless
  // for other routes.
  const opts = { method, headers: { 'X-OperatorLM-Admin': '1' } };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = null; }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || (text && text.trim()) || res.statusText;
    throw new Error(msg);
  }
  return data;
}

// ── Toasts ──────────────────────────────────────────────────
function toast(message, type = 'info') {
  const host = $('#toast-host');
  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.textContent = message;
  host.appendChild(el);
  requestAnimationFrame(() => el.classList.add('show'));
  setTimeout(() => {
    el.classList.remove('show');
    setTimeout(() => el.remove(), 250);
  }, 3500);
}

// ── Tabs ────────────────────────────────────────────────────
const navItems = $$('.nav-item');
const panes    = $$('.tab-pane');
function activate(tab) {
  navItems.forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
  panes   .forEach(p => p.classList.toggle('active', p.dataset.pane === tab));
  if (location.hash !== '#' + tab) history.replaceState(null, '', '#' + tab);
}
navItems.forEach(b => b.addEventListener('click', () => {
  activate(b.dataset.tab);
  if (b.dataset.tab === 'tryit' && typeof loadLocalAuth === 'function') loadLocalAuth();
}));
const initial = location.hash.replace('#','') || 'providers';
if ($$(`.nav-item[data-tab="${initial}"]`).length) activate(initial);

// ── Defaults ────────────────────────────────────────────────
const DEFAULTS = {
  openai:          { prefix: 'openai/',     base_url: 'https://api.openai.com/v1' },
  openrouter:      { prefix: 'openrouter/', base_url: 'https://openrouter.ai/api/v1' },
  groq:            { prefix: 'groq/',       base_url: 'https://api.groq.com/openai/v1' },
  gemini:          { prefix: 'gemini/',     base_url: 'https://generativelanguage.googleapis.com/v1beta' },
  'chatgpt-codex': { prefix: 'chatgpt/',    base_url: '' },
  'opencode-zen':  { prefix: 'zen/',        base_url: 'https://opencode.ai/zen/v1' },
  'nvidia-nim':    { prefix: 'nvidia/',     base_url: 'https://integrate.api.nvidia.com/v1' },
  mistral:         { prefix: 'mistral/',    base_url: 'https://api.mistral.ai/v1' },
  bedrock:         { prefix: 'bedrock/',    base_url: 'https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1' },
  'azure-openai':  { prefix: 'azure/',      base_url: 'https://YOUR-RESOURCE.openai.azure.com', api_version: '2024-10-21' },
  custom:          { prefix: 'local/',      base_url: 'http://localhost:11434/v1' },
};

// in-memory snapshots
let providers = [];
let aliases   = [];

// ─────────────────────────────────────────────────────────────
//  Providers tab
// ─────────────────────────────────────────────────────────────

const provForm = $('#provider-form');
const provFields = {
  name:    $('[name=name]',        provForm),
  type:    $('[name=type]',        provForm),
  prefix:  $('[name=prefix]',      provForm),
  base:    $('[name=base_url]',    provForm),
  apiKey:  $('[name=api_key]',     provForm),
  apiVer:  $('[name=api_version]', provForm),
};
const probeBtn     = $('#probe-btn');
const probeStatus  = $('#probe-status');
const modelsBlock  = $('#models-block');
const modelsSelect = $('#models-select');
const saveBtn      = $('#save-btn');
const apikeyBlock      = $('#apikey-block');
const chatgptBlock     = $('#chatgpt-block');
const chatgptLoginBtn  = $('#chatgpt-login-btn');
const chatgptStatus    = $('#chatgpt-status');
const chatgptModelsSel = $('#chatgpt-models-select');

function autocompleteByType() {
  const t = provFields.type.value;
  const d = DEFAULTS[t];
  if (d) {
    if (!provFields.prefix.value) provFields.prefix.value = d.prefix;
    if (!provFields.base  .value) provFields.base  .value = d.base_url;
    if (provFields.apiVer && d.api_version && !provFields.apiVer.value) {
      provFields.apiVer.value = d.api_version;
    }
  }
  const isChatGPT = t === 'chatgpt-codex';
  const isAzure   = t === 'azure-openai';
  const isCustom  = t === 'custom';
  apikeyBlock.hidden = isChatGPT;
  chatgptBlock.hidden = !isChatGPT;
  provFields.apiKey.required = !isChatGPT && !isCustom;
  provFields.base.required = !isChatGPT;
  provFields.base.disabled = isChatGPT;
  if (isChatGPT) provFields.base.value = '';
  const apiVerField = document.getElementById('api-version-field');
  if (apiVerField) apiVerField.hidden = !isAzure;
  const baseField = document.getElementById('base-url-field');
  if (baseField) baseField.hidden = isChatGPT;
  if (isChatGPT) {
    chatgptStatus.textContent = '';
    chatgptStatus.className = 'status';
  } else {
    const cust = document.getElementById('chatgpt-models-custom');
    if (cust) cust.value = '';
  }
}
provFields.type.addEventListener('change', () => {
  for (const t in DEFAULTS) {
    if (provFields.prefix.value === DEFAULTS[t].prefix)   provFields.prefix.value = '';
    if (provFields.base  .value === DEFAULTS[t].base_url) provFields.base  .value = '';
    if (provFields.apiVer && DEFAULTS[t].api_version &&
        provFields.apiVer.value === DEFAULTS[t].api_version) provFields.apiVer.value = '';
  }
  autocompleteByType();
  resetProbe();
});
[provFields.apiKey, provFields.base, provFields.prefix].forEach(el =>
  el.addEventListener('input', resetProbe));
autocompleteByType();

function resetProbe() {
  modelsBlock.hidden = true;
  modelsSelect.innerHTML = '';
  saveBtn.disabled = true;
  probeStatus.textContent = '';
  probeStatus.className = 'status';
}

probeBtn.addEventListener('click', async () => {
  const payload = {
    type: provFields.type.value,
    base_url: provFields.base.value,
    api_key: provFields.apiKey.value,
    api_version: (provFields.apiVer && provFields.apiVer.value) || '',
  };
  const keyOptional = payload.type === 'custom';
  if (!payload.base_url || (!payload.api_key && !keyOptional)) {
    probeStatus.textContent = keyOptional
      ? 'Fill base URL first'
      : 'Fill base URL and API key first';
    probeStatus.className = 'status err';
    return;
  }
  probeBtn.disabled = true;
  probeStatus.textContent = 'Validating…';
  probeStatus.className = 'status pending';
  try {
    const body = await api('POST', '/admin/providers/probe', payload);
    const models = body.models || [];
    if (!models.length) {
      probeStatus.textContent = 'No models returned';
      probeStatus.className = 'status err';
      return;
    }
    modelsSelect.innerHTML = '';
    for (const m of models) {
      const o = document.createElement('option');
      o.value = m; o.textContent = m;
      modelsSelect.appendChild(o);
    }
    modelsBlock.hidden = false;
    saveBtn.disabled = false;
    probeStatus.textContent = `${models.length} models found ✓`;
    probeStatus.className = 'status ok';
  } catch (e) {
    probeStatus.textContent = 'Invalid: ' + e.message;
    probeStatus.className = 'status err';
  } finally {
    probeBtn.disabled = false;
  }
});

provForm.addEventListener('submit', async e => {
  e.preventDefault();
  if (saveBtn.disabled) return;
  const selected = Array.from(modelsSelect.selectedOptions).map(o => o.value);
  const payload = {
    name:        provFields.name.value,
    type:        provFields.type.value,
    prefix:      provFields.prefix.value,
    base_url:    provFields.base.value,
    api_key:     provFields.apiKey.value,
    api_version: (provFields.apiVer && provFields.apiVer.value) || '',
    models:      selected,
  };
  try {
    await api('POST', '/admin/providers', payload);
    provForm.reset();
    autocompleteByType();
    resetProbe();
    await loadAll();
    toast('Provider saved', 'success');
  } catch (e) {
    toast(e.message, 'error');
  }
});

// ── ChatGPT (OAuth) login ──────────────────────────────────
chatgptLoginBtn.addEventListener('click', async () => {
  const name = (provFields.name.value || '').trim();
  if (!name) {
    chatgptStatus.textContent = 'Provider name required';
    chatgptStatus.className = 'status err';
    return;
  }
  const picked = Array.from(chatgptModelsSel.selectedOptions).map(o => o.value);
  const customRaw = (document.getElementById('chatgpt-models-custom')?.value || '');
  const custom = customRaw.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
  const models = Array.from(new Set([...picked, ...custom]));
  if (!models.length) {
    chatgptStatus.textContent = 'Pick at least one model';
    chatgptStatus.className = 'status err';
    return;
  }

  chatgptLoginBtn.disabled = true;
  chatgptStatus.textContent = 'Saving provider…';
  chatgptStatus.className = 'status pending';

  try {
    await api('POST', '/admin/providers', {
      name,
      type: 'chatgpt-codex',
      prefix: provFields.prefix.value || 'chatgpt/',
      base_url: '',
      api_key_ref: `operatorlm:chatgpt-${name}`,
      models,
    });
  } catch (e) {
    chatgptStatus.textContent = 'Save failed: ' + e.message;
    chatgptStatus.className = 'status err';
    chatgptLoginBtn.disabled = false;
    return;
  }

  chatgptStatus.textContent = 'Opening browser…';
  try {
    await api('POST', '/admin/auth/chatgpt/start', { provider: name });
  } catch (e) {
    chatgptStatus.textContent = 'Login start failed: ' + e.message;
    chatgptStatus.className = 'status err';
    chatgptLoginBtn.disabled = false;
    return;
  }

  chatgptStatus.textContent = 'Waiting for browser login…';

  const deadline = Date.now() + 5 * 60 * 1000; // 5 min
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 1500));
    let st;
    try {
      st = await api('GET', '/admin/auth/chatgpt/status');
    } catch { continue; }
    if (st.status === 'success') {
      chatgptStatus.textContent = 'Logged in ✓';
      chatgptStatus.className = 'status ok';
      const cust = document.getElementById('chatgpt-models-custom');
      if (cust) cust.value = '';
      provForm.reset();
      autocompleteByType();
      await loadAll();
      toast('ChatGPT login successful', 'success');
      chatgptLoginBtn.disabled = false;
      return;
    }
    if (st.status === 'error') {
      chatgptStatus.textContent = 'Login failed: ' + (st.error || 'unknown');
      chatgptStatus.className = 'status err';
      chatgptLoginBtn.disabled = false;
      return;
    }
  }
  chatgptStatus.textContent = 'Timed out waiting for login';
  chatgptStatus.className = 'status err';
  chatgptLoginBtn.disabled = false;
});

// ── Edit provider dialog ───────────────────────────────────
const editDialog      = $('#provider-edit-dialog');
const editForm        = $('#provider-edit-form');
const editName        = $('#edit-name');
const editType        = $('#edit-type');
const editPrefix      = $('#edit-prefix');
const editBase        = $('#edit-base');
const editBaseFld     = $('#edit-base-field');
const editModelsSel   = $('#edit-models-select');
const editModelsCust  = $('#edit-models-custom');
const editFetchBlock  = $('#edit-fetch-block');
const editProbeBtn    = $('#edit-probe-btn');
const editProbeStatus = $('#edit-probe-status');
const editDisabled    = $('#edit-disabled');
const editCancel      = $('#edit-cancel');

let editOriginalDisabled = false;

function renderModelOptions(selectEl, models, preselected) {
  const sel = new Set(preselected || []);
  const seen = new Set();
  selectEl.innerHTML = '';
  for (const m of models) {
    if (seen.has(m)) continue;
    seen.add(m);
    const o = document.createElement('option');
    o.value = m;
    o.textContent = m;
    if (sel.has(m)) o.selected = true;
    selectEl.appendChild(o);
  }
  // Preserve any preselected entries that weren't in the upstream list
  for (const m of sel) {
    if (seen.has(m)) continue;
    const o = document.createElement('option');
    o.value = m;
    o.textContent = m;
    o.selected = true;
    selectEl.appendChild(o);
  }
}

function openEditDialog(p) {
  editName.value     = p.name;
  editType.value     = p.type;
  editPrefix.value   = p.prefix || '';
  editBase.value     = p.base_url || '';
  const current = p.models || [];
  renderModelOptions(editModelsSel, current, current);
  editModelsCust.value = '';
  editProbeStatus.textContent = '';
  editProbeStatus.className = 'status';
  editDisabled.checked = !!p.disabled;
  editOriginalDisabled = !!p.disabled;
  editBaseFld.hidden = (p.type === 'chatgpt-codex');
  editFetchBlock.hidden = (p.type === 'chatgpt-codex');
  if (typeof editDialog.showModal === 'function') {
    editDialog.showModal();
  } else {
    editDialog.setAttribute('open', '');
  }
}

editCancel.addEventListener('click', () => editDialog.close());

editProbeBtn.addEventListener('click', async () => {
  const name = editName.value;
  if (!name) return;
  const currentlySelected = Array.from(editModelsSel.selectedOptions).map(o => o.value);
  editProbeBtn.disabled = true;
  editProbeStatus.textContent = 'Validating…';
  editProbeStatus.className = 'status pending';
  try {
    const body = await api('POST', '/admin/providers/probe', { provider: name });
    const models = body.models || [];
    if (!models.length) {
      editProbeStatus.textContent = 'No models returned';
      editProbeStatus.className = 'status err';
      return;
    }
    renderModelOptions(editModelsSel, models, currentlySelected);
    editProbeStatus.textContent = `${models.length} models found ✓`;
    editProbeStatus.className = 'status ok';
  } catch (e) {
    editProbeStatus.textContent = 'Failed: ' + e.message;
    editProbeStatus.className = 'status err';
  } finally {
    editProbeBtn.disabled = false;
  }
});

editForm.addEventListener('submit', async e => {
  e.preventDefault();
  const name = editName.value;
  const type = editType.value;
  const picked = Array.from(editModelsSel.selectedOptions).map(o => o.value);
  const custom = editModelsCust.value.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
  const models = Array.from(new Set([...picked, ...custom]));

  const payload = {
    name,
    type,
    prefix:   editPrefix.value,
    base_url: type === 'chatgpt-codex' ? '' : editBase.value,
    models,
  };

  try {
    await api('POST', '/admin/providers', payload);
    if (editDisabled.checked !== editOriginalDisabled) {
      await api('PATCH', '/admin/providers/' + encodeURIComponent(name), {
        disabled: editDisabled.checked,
      });
    }
    editDialog.close();
    await loadAll();
    toast('Provider updated', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ─────────────────────────────────────────────────────────────
//  Keys tab
// ─────────────────────────────────────────────────────────────

const keyForm        = $('#key-form');
const keyProviderSel = $('#key-provider');

keyForm.addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(keyForm);
  try {
    await api('POST', `/admin/providers/${encodeURIComponent(fd.get('provider'))}/keys`, {
      name: fd.get('name'),
      api_key: fd.get('api_key'),
    });
    keyForm.reset();
    await loadAll();
    toast('Key added', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ─────────────────────────────────────────────────────────────
//  Aliases tab
// ─────────────────────────────────────────────────────────────

const aliasForm    = $('#alias-form');
const targetsTbody = $('#targets-table tbody');
const addTargetBtn = $('#add-target-btn');

function addTargetRow(t = {}) {
  const tr = document.createElement('tr');
  tr.innerHTML = `
    <td class="idx"></td>
    <td><select class="t-provider"></select></td>
    <td><select class="t-key"></select></td>
    <td><select class="t-model"></select></td>
    <td><input class="t-order" type="number" min="0" value="${t.order ?? 1}" style="width:5rem" /></td>
    <td><input class="t-rpm"   type="number" min="0" value="${t.rpm   ?? 0}" style="width:5rem" /></td>
    <td><button type="button" class="danger t-del">✕</button></td>`;
  targetsTbody.appendChild(tr);

  const provSel  = $('.t-provider', tr);
  const keySel   = $('.t-key',      tr);
  const modelSel = $('.t-model',    tr);

  for (const p of providers) {
    const o = document.createElement('option');
    o.value = p.name; o.textContent = p.name;
    provSel.appendChild(o);
  }
  if (t.provider) provSel.value = t.provider;

  function refresh() {
    const p = providers.find(x => x.name === provSel.value);
    keySel.innerHTML = '';
    const def = document.createElement('option');
    def.value = ''; def.textContent = 'default';
    keySel.appendChild(def);
    for (const k of (p?.keys || [])) {
      const o = document.createElement('option');
      o.value = k.name; o.textContent = k.name;
      keySel.appendChild(o);
    }
    if (t.key) keySel.value = t.key;

    modelSel.innerHTML = '';
    const models = p?.models || [];
    if (!models.length) {
      const o = document.createElement('option');
      o.value = ''; o.textContent = '(no models — set them on the provider)';
      modelSel.appendChild(o);
    } else {
      for (const m of models) {
        const o = document.createElement('option');
        o.value = m; o.textContent = m;
        modelSel.appendChild(o);
      }
    }
    if (t.upstream_model) modelSel.value = t.upstream_model;
  }
  refresh();
  provSel.addEventListener('change', refresh);

  $('.t-del', tr).addEventListener('click', () => { tr.remove(); renumber(); });
  renumber();
}

function renumber() {
  $$('#targets-table tbody tr').forEach((tr, i) => $('.idx', tr).textContent = i + 1);
}

addTargetBtn.addEventListener('click', () => addTargetRow());

aliasForm.addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(aliasForm);
  const targets = $$('#targets-table tbody tr').map(tr => ({
    provider:       $('.t-provider', tr).value,
    key:            $('.t-key',      tr).value,
    upstream_model: $('.t-model',    tr).value,
    order:          Number($('.t-order', tr).value) || 0,
    rpm:            Number($('.t-rpm',   tr).value) || 0,
  })).filter(t => t.upstream_model);
  if (!targets.length) {
    toast('Add at least one target with a model selected.', 'warning');
    return;
  }
  try {
    await api('POST', '/admin/aliases', {
      name: fd.get('name'),
      strategy: fd.get('strategy') || 'order',
      targets,
    });
    aliasForm.reset();
    targetsTbody.innerHTML = '';
    await loadAll();
    toast('Alias saved', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

function editAlias(name) {
  const a = aliases.find(x => x.name === name);
  if (!a) return;
  $('[name=name]',     aliasForm).value = a.name;
  $('[name=strategy]', aliasForm).value = a.strategy || 'order';
  targetsTbody.innerHTML = '';
  for (const t of (a.targets || [])) addTargetRow(t);
  activate('aliases');
  aliasForm.scrollIntoView({ behavior: 'smooth', block: 'center' });
}

// ─────────────────────────────────────────────────────────────
//  Try it tab (playground)
// ─────────────────────────────────────────────────────────────

const tryStream    = $('#tryit-stream');
const tryPromptIn  = $('#tryit-prompt');
const slugsHost    = $('#tryit-slugs');
const aliasesHost  = $('#tryit-aliases');
const slugsCount   = $('#tryit-slugs-count');
const aliasesCount = $('#tryit-aliases-count');
const slugsEmpty   = $('#tryit-slugs-empty');
const aliasesEmpty = $('#tryit-aliases-empty');

const resultMeta = $('#result-meta');
const resultBody = $('#result-body');
const resultTabs = $('.result-tabs');
$('#tryit-clear').addEventListener('click', clearResult);

// Last-result store, shown via the tabs (Content / Raw / Request)
let lastResult = null;

function clearResult() {
  lastResult = null;
  resultMeta.innerHTML = '<span class="muted small">No request yet. Click <strong>Run</strong> on any example.</span>';
  resultBody.textContent = '';
  resultTabs.hidden = true;
}

resultTabs.addEventListener('click', e => {
  const btn = e.target.closest('.result-tab');
  if (!btn) return;
  $$('.result-tab').forEach(b => b.classList.toggle('active', b === btn));
  renderResultBody(btn.dataset.rt);
});

function renderResultBody(view) {
  if (!lastResult) { resultBody.textContent = ''; return; }
  const { content, raw, curl } = lastResult;
  if (view === 'content') resultBody.textContent = content || '(no text content)';
  else if (view === 'raw') resultBody.textContent = raw || '(empty)';
  else if (view === 'curl') resultBody.textContent = curl || '';
}

function buildPayload(model, stream) {
  return {
    model,
    messages: [{ role: 'user', content: tryPromptIn.value || 'hi' }],
    stream: !!stream,
  };
}

function buildCurl(model, stream) {
  const payload = buildPayload(model, stream);
  // Pretty-print the body for readability in the curl preview.
  const body = JSON.stringify(payload);
  const escaped = body.replace(/'/g, "'\\''");
  const url = `http://${location.host}/v1/chat/completions`;
  return `curl ${url} \\
  -H "Content-Type: application/json" \\
  -d '${escaped}'`;
}

async function copyCurl(model, stream, btn) {
  const cmd = buildCurl(model, stream);
  try {
    await navigator.clipboard.writeText(cmd);
    const original = btn.textContent;
    btn.textContent = 'Copied ✓';
    setTimeout(() => { btn.textContent = original; }, 1400);
  } catch (e) {
    toast('Clipboard failed: ' + e.message, 'error');
  }
}

// ─────────────────────────────────────────────────────────────
//  Help → Example requests (cURL / Python / TypeScript tabs)
// ─────────────────────────────────────────────────────────────
function buildHelpSnippets(baseURL) {
  const v1 = `${baseURL}/v1`;
  const curl = (model, stream) => {
    const body = JSON.stringify({
      model,
      messages: [{ role: 'user', content: 'hi' }],
      ...(stream ? { stream: true } : {}),
    });
    return `curl ${v1}/chat/completions \\
  -H "Content-Type: application/json" \\
  -d '${body}'`;
  };
  const py = (model, stream) => stream
    ? `from openai import OpenAI

client = OpenAI(base_url="${v1}", api_key="not-needed")

stream = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "hi"}],
    stream=True,
)
for chunk in stream:
    delta = chunk.choices[0].delta.content or ""
    print(delta, end="", flush=True)`
    : `from openai import OpenAI

client = OpenAI(base_url="${v1}", api_key="not-needed")

resp = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "hi"}],
)
print(resp.choices[0].message.content)`;
  const ts = (model, stream) => stream
    ? `import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${v1}",
  apiKey: "not-needed",
});

const stream = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "hi" }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
}`
    : `import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${v1}",
  apiKey: "not-needed",
});

const resp = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "hi" }],
});
console.log(resp.choices[0].message.content);`;

  return {
    slug:   { curl: curl('openai/gpt-4o-mini', false), python: py('openai/gpt-4o-mini', false), ts: ts('openai/gpt-4o-mini', false) },
    alias:  { curl: curl('model-hub', false),          python: py('model-hub', false),          ts: ts('model-hub', false) },
    stream: { curl: curl('model-hub', true),           python: py('model-hub', true),           ts: ts('model-hub', true) },
  };
}

function renderHelpExamples(baseURL) {
  const snippets = buildHelpSnippets(baseURL);
  $$('.example-block').forEach(block => {
    const key = block.dataset.example;
    const set = snippets[key];
    if (!set) return;
    const body = $('.snippet-body', block);
    const tabs = $$('.snippet-tab', block);
    const copyBtn = $('.snippet-copy', block);

    const langOf = t => t.dataset.lang === 'ts' ? 'ts' : t.dataset.lang === 'python' ? 'python' : 'curl';
    const show = lang => {
      body.textContent = set[lang];
      tabs.forEach(t => t.classList.toggle('active', langOf(t) === lang));
    };
    show('curl');

    tabs.forEach(t => {
      t.onclick = () => show(langOf(t));
    });

    copyBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(body.textContent);
        const original = copyBtn.textContent;
        copyBtn.textContent = 'Copied ✓';
        setTimeout(() => { copyBtn.textContent = original; }, 1400);
      } catch (e) {
        toast('Clipboard failed: ' + e.message, 'error');
      }
    };
  });
}

function setRunningMeta(model, stream) {
  resultTabs.hidden = false;
  resultMeta.innerHTML = `
    <span class="pill run">running…</span>
    <code class="small">${model}</code>
    ${stream ? '<span class="muted small">stream</span>' : ''}
  `;
  resultBody.textContent = '';
}

function setDoneMeta(model, stream, status, ms) {
  const ok = status >= 200 && status < 300;
  resultMeta.innerHTML = `
    <span class="pill ${ok ? 'ok' : 'err'}">${status}</span>
    <code class="small">${model}</code>
    ${stream ? '<span class="muted small">stream</span>' : ''}
    <span class="muted small">${ms} ms</span>
  `;
}

// Extract plain text content from the response (handles non-stream and SSE).
function extractContent(rawText, stream) {
  if (!stream) {
    try {
      const obj = JSON.parse(rawText);
      const c = obj?.choices?.[0]?.message?.content;
      if (typeof c === 'string') return c;
      // Errors come as { error: {...} } or string body
      if (obj?.error) return JSON.stringify(obj.error, null, 2);
      return JSON.stringify(obj, null, 2);
    } catch (_) { return rawText; }
  }
  // Stream: parse SSE lines
  let out = '';
  for (const line of rawText.split('\n')) {
    if (!line.startsWith('data:')) continue;
    const payload = line.slice(5).trim();
    if (!payload || payload === '[DONE]') continue;
    try {
      const obj = JSON.parse(payload);
      const delta = obj?.choices?.[0]?.delta?.content;
      if (typeof delta === 'string') out += delta;
    } catch (_) { /* ignore */ }
  }
  return out || rawText;
}

async function runExample(model, stream) {
  const curl = buildCurl(model, stream);
  setRunningMeta(model, stream);
  // Switch to Content view by default for new runs
  $$('.result-tab').forEach(b => b.classList.toggle('active', b.dataset.rt === 'content'));

  const t0 = performance.now();
  let raw = '';
  try {
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(buildPayload(model, stream)),
    });

    if (stream && res.body && res.headers.get('content-type')?.includes('event-stream')) {
      // Stream chunks into the body as they arrive.
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let liveText = '';
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const chunk = dec.decode(value, { stream: true });
        raw += chunk;
        // Live update: parse what we have and re-render Content view
        liveText = extractContent(raw, true);
        const view = $$('.result-tab').find(b => b.classList.contains('active'))?.dataset.rt || 'content';
        if (view === 'content') resultBody.textContent = liveText;
        else if (view === 'raw') resultBody.textContent = raw;
      }
    } else {
      raw = await res.text();
    }

    const ms = Math.round(performance.now() - t0);
    setDoneMeta(model, stream, res.status, ms);
    const content = extractContent(raw, stream);
    lastResult = { content, raw, curl };
    const view = $$('.result-tab').find(b => b.classList.contains('active'))?.dataset.rt || 'content';
    renderResultBody(view);
  } catch (e) {
    const ms = Math.round(performance.now() - t0);
    resultMeta.innerHTML = `
      <span class="pill err">network</span>
      <code class="small">${model}</code>
      <span class="muted small">${ms} ms</span>
    `;
    lastResult = { content: e.message, raw: e.message, curl };
    renderResultBody('content');
  }
}

function exampleCard(model, badgeText, badgeClass) {
  const card = document.createElement('div');
  card.className = 'example';
  const stream = tryStream.checked;
  card.innerHTML = `
    <div class="example-head">
      <div class="example-name">${model}</div>
      <div class="example-meta">
        <span class="tag ${badgeClass || ''}">${badgeText}</span>
      </div>
    </div>
    <pre class="curl-pre"></pre>
    <div class="example-actions">
      <button type="button" class="btn-secondary copy-btn">Copy</button>
      <button type="button" class="btn-primary run-btn">Run</button>
    </div>`;
  const updateCurl = () => $('.curl-pre', card).textContent = buildCurl(model, tryStream.checked);
  updateCurl();
  tryPromptIn.addEventListener('input', updateCurl);
  tryStream.addEventListener('change', updateCurl);

  $('.copy-btn', card).addEventListener('click', e => copyCurl(model, tryStream.checked, e.currentTarget));
  $('.run-btn',  card).addEventListener('click', () => runExample(model, tryStream.checked));
  return card;
}

function renderTryIt() {
  // Slugs: every (provider.prefix + model) pair from configured providers
  slugsHost.innerHTML = '';
  let slugCount = 0;
  for (const p of providers) {
    for (const m of (p.models || [])) {
      slugCount++;
      slugsHost.appendChild(exampleCard((p.prefix || '') + m, p.type));
    }
  }
  slugsCount.textContent = slugCount;
  slugsEmpty.hidden = slugCount > 0;

  // Aliases
  aliasesHost.innerHTML = '';
  for (const a of aliases) {
    aliasesHost.appendChild(exampleCard(a.name, `${(a.targets || []).length} targets`));
  }
  aliasesCount.textContent = aliases.length;
  aliasesEmpty.hidden = aliases.length > 0;
}

// ─────────────────────────────────────────────────────────────
//  Reliability tab (config + live health)
// ─────────────────────────────────────────────────────────────

const reliabilityForm = $('#reliability-form');
const fallbackSel     = $('#fallback-alias-sel');
const healthTbody     = $('#health-table tbody');
const healthEmpty     = $('#health-empty');
$('#health-refresh-btn').addEventListener('click', loadHealth);

const RELIABILITY_FIELDS = [
  'max_retries', 'backoff_base_ms', 'backoff_cap_ms',
  'open_after_failures', 'cooldown_rate_limit_ms', 'cooldown_server_ms', 'cooldown_network_ms',
  'connect_timeout_ms', 'per_attempt_timeout_ms', 'stream_idle_timeout_ms', 'total_timeout_ms',
];

async function loadReliability() {
  try {
    const r = await api('GET', '/admin/reliability');
    for (const f of RELIABILITY_FIELDS) {
      const el = $(`[name=${f}]`, reliabilityForm);
      if (el) el.value = r[f] ?? '';
    }
    // Refresh fallback alias dropdown options from current aliases
    fallbackSel.innerHTML = '<option value="">(none)</option>';
    for (const a of aliases) {
      const o = document.createElement('option');
      o.value = a.name; o.textContent = a.name;
      fallbackSel.appendChild(o);
    }
    fallbackSel.value = r.default_fallback_alias || '';
  } catch (e) {
    console.error('reliability load failed', e);
  }
}

reliabilityForm.addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(reliabilityForm);
  const payload = { default_fallback_alias: fd.get('default_fallback_alias') || '' };
  for (const f of RELIABILITY_FIELDS) {
    payload[f] = Number(fd.get(f)) || 0;
  }
  try {
    await api('POST', '/admin/reliability', payload);
    await loadReliability();
    toast('Reliability settings applied', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

function fmtRelTime(iso) {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  const dt = t - Date.now();
  if (Math.abs(dt) < 1000) return 'now';
  const s = Math.round(dt / 1000);
  if (s > 0) return `in ${s}s`;
  return `${-s}s ago`;
}

async function loadHealth() {
  try {
    const r = await api('GET', '/admin/health');
    const targets = r.targets || [];
    healthTbody.innerHTML = '';
    healthEmpty.hidden = targets.length > 0;
    // Sort: open > half-open > closed, then by id
    targets.sort((a, b) => {
      const order = { 'open': 0, 'half-open': 1, 'closed': 2 };
      return (order[a.state] - order[b.state]) || a.id.localeCompare(b.id);
    });
    for (const t of targets) {
      const tr = document.createElement('tr');
      const fails = t.consecutive_failures || 0;
      const cooldown = t.cooldown_ends_at ? fmtRelTime(t.cooldown_ends_at) : '—';
      const errPreview = (t.last_error || '').slice(0, 80) + ((t.last_error || '').length > 80 ? '…' : '');
      const cls = t.last_class ? `<span class="tag">${t.last_class}</span> ` : '';
      tr.innerHTML = `
        <td><code>${t.id}</code></td>
        <td><span class="state-pill ${t.state}">${t.state}</span></td>
        <td>${fails}</td>
        <td class="muted small">${cooldown}</td>
        <td class="muted small">${cls}${errPreview || '—'}</td>
        <td>${t.state !== 'closed' ? `<button class="btn-ghost" data-clear="${t.id}">Reset</button>` : ''}</td>`;
      healthTbody.appendChild(tr);
    }
    healthTbody.querySelectorAll('button[data-clear]').forEach(btn => {
      btn.onclick = async () => {
        try {
          await api('POST', '/admin/health/clear', { id: btn.dataset.clear });
          loadHealth();
          toast('Breaker reset', 'success');
        } catch (e) { toast(e.message, 'error'); }
      };
    });
  } catch (e) {
    console.error('health load failed', e);
  }
}
setInterval(loadHealth, 3000);

// ─────────────────────────────────────────────────────────────
//  Audit tab
// ─────────────────────────────────────────────────────────────

const auditForm    = $('#audit-form');
const auditActive  = $('#audit-active');
const auditWritten = $('#audit-written');
const auditDropped = $('#audit-dropped');
const auditPath    = $('#audit-path');

async function loadAudit() {
  try {
    const a = await api('GET', '/admin/audit');
    $('[name=enabled]',                 auditForm).checked = !!a.enabled;
    $('[name=path]',                    auditForm).value   = a.path || '';
    $('[name=buffer_size]',             auditForm).value   = a.buffer_size || '';
    $('[name=max_request_body_bytes]',  auditForm).value   = a.max_request_body_bytes || '';
    $('[name=max_response_body_bytes]', auditForm).value   = a.max_response_body_bytes || '';
    $('[name=redact]',                  auditForm).checked = !!a.redact;

    auditActive.textContent  = a.active ? 'Active' : 'Disabled';
    auditActive.style.color  = a.active ? 'var(--success)' : 'var(--text-mute)';
    auditWritten.textContent = (a.written || 0).toLocaleString();
    auditDropped.textContent = (a.dropped || 0).toLocaleString();
    auditDropped.style.color = (a.dropped || 0) > 0 ? 'var(--warning)' : 'var(--text)';
    auditPath.textContent    = a.effective_path || a.path || '—';
    $('#help-audit-path').textContent = a.effective_path || a.path || '~/.operatorlm/audit.log';
  } catch (e) {
    auditActive.textContent = 'Error';
    auditActive.style.color = 'var(--danger)';
  }
}

auditForm.addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(auditForm);
  const payload = {
    enabled:                 $('[name=enabled]', auditForm).checked,
    path:                    fd.get('path') || '',
    buffer_size:             Number(fd.get('buffer_size'))             || 0,
    max_request_body_bytes:  Number(fd.get('max_request_body_bytes'))  || 0,
    max_response_body_bytes: Number(fd.get('max_response_body_bytes')) || 0,
    redact:                  $('[name=redact]', auditForm).checked,
  };
  try {
    await api('POST', '/admin/audit', payload);
    await loadAudit();
    toast('Audit settings applied', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});
setInterval(loadAudit, 5000);

// ─────────────────────────────────────────────────────────────
//  Local auth tab
// ─────────────────────────────────────────────────────────────

const localAuthForm   = $('#localauth-form');
const localAuthStatus = $('#localauth-status');
const localAuthClear  = $('#localauth-clear');
const tryitLock       = $('#tryit-locked');
const tryitPane       = document.querySelector('[data-pane="tryit"]');

async function loadLocalAuth() {
  try {
    const a = await api('GET', '/admin/localauth');
    $('[name=enabled]', localAuthForm).checked = !!a.enabled;
    $('[name=api_key]', localAuthForm).value   = '';
    if (a.enabled && a.key_set) {
      localAuthStatus.textContent   = 'enforced';
      localAuthStatus.style.color   = 'var(--success)';
    } else if (a.key_set) {
      localAuthStatus.textContent   = 'key set, disabled';
      localAuthStatus.style.color   = 'var(--text-mute)';
    } else {
      localAuthStatus.textContent   = 'no key set';
      localAuthStatus.style.color   = 'var(--text-mute)';
    }
    const locked = !!a.enabled && !!a.key_set;
    if (tryitLock) tryitLock.hidden = !locked;
    if (tryitPane) tryitPane.classList.toggle('tryit-locked-on', locked);
  } catch (e) {
    localAuthStatus.textContent = 'error';
    localAuthStatus.style.color = 'var(--danger)';
  }
}

localAuthForm.addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(localAuthForm);
  const payload = {
    enabled: $('[name=enabled]', localAuthForm).checked,
    api_key: (fd.get('api_key') || '').toString(),
  };
  try {
    await api('POST', '/admin/localauth', payload);
    $('[name=api_key]', localAuthForm).value = '';
    await loadLocalAuth();
    toast('Local auth settings applied', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

localAuthClear.addEventListener('click', async () => {
  if (!confirm('Clear the stored local API key? This also disables enforcement.')) return;
  try {
    await api('POST', '/admin/localauth', { enabled: false, clear_key: true });
    $('[name=api_key]', localAuthForm).value = '';
    await loadLocalAuth();
    toast('Local API key cleared', 'success');
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ─────────────────────────────────────────────────────────────
//  Render
// ─────────────────────────────────────────────────────────────

function render() {
  // Providers table ----------------------------------------
  const ptb = $('#providers tbody');
  ptb.innerHTML = '';
  for (const p of providers) {
    const tags = (p.models || []).map(m => `<span class="tag">${m}</span>`).join(' ')
              || '<span class="muted small">none</span>';
    const keyNames = ['default', ...(p.keys || []).map(k => k.name)];
    const keys = keyNames.map(n => `<span class="tag">${n}</span>`).join(' ');
    const tr = document.createElement('tr');
    if (p.disabled) tr.classList.add('row-disabled');
    const disabledTag = p.disabled ? ' <span class="tag tag-disabled">Disabled</span>' : '';
    const reloginBtn = p.type === 'chatgpt-codex'
      ? `<button class="btn-ghost" data-relogin="${p.name}">Re-login</button>`
      : '';
    tr.innerHTML = `
      <td><strong>${p.name}</strong>${disabledTag}</td>
      <td><span class="tag">${p.type}</span></td>
      <td><code>${p.prefix || ''}</code></td>
      <td class="small muted">${p.base_url}</td>
      <td>${keys}</td>
      <td>${tags}</td>
      <td>
        <button class="btn-ghost" data-edit="${p.name}">Edit</button>
        ${reloginBtn}
        <button class="btn-ghost" data-toggle-prov="${p.name}" data-disabled="${p.disabled ? '1' : '0'}">${p.disabled ? 'Enable' : 'Disable'}</button>
        <button class="danger" data-name="${p.name}">Delete</button>
      </td>`;
    ptb.appendChild(tr);
  }
  $('#providers-empty').hidden = providers.length > 0;
  $('#providers-count').textContent = providers.length;
  ptb.querySelectorAll('button[data-toggle-prov]').forEach(btn => {
    btn.onclick = async () => {
      const name = btn.dataset.toggleProv;
      const next = btn.dataset.disabled !== '1';
      try {
        await api('PATCH', '/admin/providers/' + encodeURIComponent(name), { disabled: next });
        await loadAll();
        toast(next ? 'Provider disabled' : 'Provider enabled', 'success');
      } catch (e) { toast(e.message, 'error'); }
    };
  });
  ptb.querySelectorAll('button[data-edit]').forEach(btn => {
    btn.onclick = () => {
      const p = providers.find(x => x.name === btn.dataset.edit);
      if (p) openEditDialog(p);
    };
  });
  ptb.querySelectorAll('button[data-relogin]').forEach(btn => {
    btn.onclick = async () => {
      const name = btn.dataset.relogin;
      btn.disabled = true;
      try {
        await api('POST', '/admin/auth/chatgpt/start', { provider: name });
        toast('Browser opened — complete login in the new tab', 'info');
        const deadline = Date.now() + 5 * 60 * 1000;
        while (Date.now() < deadline) {
          await new Promise(r => setTimeout(r, 1500));
          let st;
          try { st = await api('GET', '/admin/auth/chatgpt/status'); } catch { continue; }
          if (st.status === 'success') { toast('ChatGPT re-login OK', 'success'); break; }
          if (st.status === 'error')   { toast('Re-login failed: ' + (st.error || ''), 'error'); break; }
        }
      } catch (e) { toast(e.message, 'error'); }
      btn.disabled = false;
    };
  });
  ptb.querySelectorAll('button.danger').forEach(btn => {
    btn.onclick = async () => {
      if (!confirm('Delete provider "' + btn.dataset.name + '" and all its keys?')) return;
      try {
        await api('DELETE', '/admin/providers/' + encodeURIComponent(btn.dataset.name));
        await loadAll();
        toast('Provider deleted', 'success');
      } catch (e) { toast(e.message, 'error'); }
    };
  });

  // Key form provider dropdown ------------------------------
  keyProviderSel.innerHTML = '';
  for (const p of providers) {
    const o = document.createElement('option');
    o.value = p.name; o.textContent = p.name;
    keyProviderSel.appendChild(o);
  }

  // All keys table -----------------------------------------
  const ktb = $('#all-keys tbody');
  ktb.innerHTML = '';
  let keyCount = 0;
  for (const p of providers) {
    for (const k of (p.keys || [])) {
      keyCount++;
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${p.name}</td>
        <td><strong>${k.name}</strong></td>
        <td><code>${k.api_key_ref}</code></td>
        <td><button class="danger" data-prov="${p.name}" data-key="${k.name}">Delete</button></td>`;
      ktb.appendChild(tr);
    }
  }
  $('#keys-empty').hidden = keyCount > 0;
  ktb.querySelectorAll('button.danger').forEach(btn => {
    btn.onclick = async () => {
      if (!confirm('Delete key "' + btn.dataset.key + '" of provider "' + btn.dataset.prov + '"?')) return;
      try {
        await api('DELETE', `/admin/providers/${encodeURIComponent(btn.dataset.prov)}/keys/${encodeURIComponent(btn.dataset.key)}`);
        await loadAll();
        toast('Key deleted', 'success');
      } catch (e) { toast(e.message, 'error'); }
    };
  });

  // Aliases table ------------------------------------------
  const atb = $('#aliases tbody');
  atb.innerHTML = '';
  for (const a of aliases) {
    const tr = document.createElement('tr');
    if (a.disabled) tr.classList.add('row-disabled');
    const disabledTag = a.disabled ? ' <span class="tag tag-disabled">Disabled</span>' : '';
    tr.innerHTML = `
      <td><strong>${a.name}</strong>${disabledTag}</td>
      <td><span class="tag">${a.strategy || 'order'}</span></td>
      <td>${(a.targets || []).length} target(s)</td>
      <td>
        <button class="btn-ghost" data-toggle-alias="${a.name}" data-disabled="${a.disabled ? '1' : '0'}">${a.disabled ? 'Enable' : 'Disable'}</button>
        <button class="btn-ghost" data-edit="${a.name}">Edit</button>
        <button class="danger"    data-del="${a.name}">Delete</button>
      </td>`;
    atb.appendChild(tr);
  }
  $('#aliases-empty').hidden = aliases.length > 0;
  $('#aliases-count').textContent = aliases.length;
  atb.querySelectorAll('button[data-toggle-alias]').forEach(btn => {
    btn.onclick = async () => {
      const name = btn.dataset.toggleAlias;
      const next = btn.dataset.disabled !== '1';
      try {
        await api('PATCH', '/admin/aliases/' + encodeURIComponent(name), { disabled: next });
        await loadAll();
        toast(next ? 'Alias disabled' : 'Alias enabled', 'success');
      } catch (e) { toast(e.message, 'error'); }
    };
  });
  atb.querySelectorAll('button[data-edit]').forEach(b =>
    b.onclick = () => editAlias(b.dataset.edit));
  atb.querySelectorAll('button[data-del]').forEach(b =>
    b.onclick = async () => {
      if (!confirm('Delete alias "' + b.dataset.del + '"?')) return;
      try {
        await api('DELETE', '/admin/aliases/' + encodeURIComponent(b.dataset.del));
        await loadAll();
        toast('Alias deleted', 'success');
      } catch (e) { toast(e.message, 'error'); }
    });

  // Help section examples ----------------------------------
  const baseURL = `http://${location.host}`;
  $('#help-base').textContent = `${baseURL}/v1`;
  $('#endpoint-addr').textContent = location.host;
  renderHelpExamples(baseURL);

  // Refresh the Try-it gallery
  renderTryIt();
}

// ─────────────────────────────────────────────────────────────
//  Loaders
// ─────────────────────────────────────────────────────────────

async function loadAll() {
  const [pl, al] = await Promise.all([
    fetch('/admin/providers').then(r => r.json()),
    fetch('/admin/aliases').then(r => r.json()).catch(() => []),
  ]);
  providers = (pl || []).map(p => ({
    name:        p.name,
    type:        p.type,
    prefix:      p.prefix,
    base_url:    p.base_url,
    api_key_ref: p.api_key_ref,
    disabled:    !!p.disabled,
    models:      p.models || [],
    keys:        (p.keys || []).map(k => ({ name: k.name, api_key_ref: k.api_key_ref })),
  }));
  aliases = (al || []).map(a => ({
    name:     a.name,
    strategy: a.strategy,
    targets:  (a.targets || []).map(t => ({
      provider:       t.provider,
      key:            t.key,
      upstream_model: t.upstream_model,
      order:          t.order,
      rpm:            t.rpm,
    })),
  }));
  render();
}

loadAll().then(() => loadReliability());
loadAudit();
loadLocalAuth();
loadHealth();
