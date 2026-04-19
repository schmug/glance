(function () {
  'use strict';
  const root = document.getElementById('admin-root');
  const baseURL = root.dataset.baseUrl || '';

  const state = {
    files: [],
    currentPath: null,
    pendingContents: '',
    onDiskContents: '',
    dirty: false,
  };

  function api(path, opts) {
    return fetch(baseURL + '/admin/api' + path, Object.assign({ credentials: 'same-origin' }, opts || {}));
  }

  function clearChildren(el) { while (el.firstChild) el.removeChild(el.firstChild); }

  function el(tag, attrs, text) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const k in attrs) {
        if (k === 'class') node.className = attrs[k];
        else if (k === 'dataset') { for (const d in attrs.dataset) node.dataset[d] = attrs.dataset[d]; }
        else node.setAttribute(k, attrs[k]);
      }
    }
    if (text != null) node.textContent = String(text);
    return node;
  }

  function renderSkeleton(yamlText) {
    const host = document.getElementById('preview-skeleton');
    clearChildren(host);
    let parsed;
    try {
      parsed = jsyaml.load(yamlText);
    } catch (e) {
      host.appendChild(el('div', { class: 'box' }, 'YAML parse error: ' + e.message));
      return;
    }
    if (!parsed || !Array.isArray(parsed.pages)) {
      host.appendChild(el('div', { class: 'box' }, '(no pages)'));
      return;
    }
    parsed.pages.forEach(function (page) {
      const pg = el('div');
      pg.appendChild(el('h3', null, page.name || '(untitled)'));
      (page.columns || []).forEach(function (col) {
        const c = el('div', { class: 'column' });
        (col.widgets || []).forEach(function (w) {
          const title = w.title || w.type || '(widget)';
          c.appendChild(el('div', { class: 'box' }, '[' + (col.size || 'full') + '] ' + title));
        });
        pg.appendChild(c);
      });
      host.appendChild(pg);
    });
  }

  api('/files').then(r => r.json()).then(files => {
    state.files = files;
    const picker = document.getElementById('file-picker');
    const list = document.getElementById('file-list');
    files.forEach(f => {
      const opt = el('option', { value: f.path }, f.path.split('/').pop());
      picker.appendChild(opt);

      const item = el('div', { class: 'item', dataset: { path: f.path } }, f.path.split('/').pop());
      list.appendChild(item);
    });
  }).catch(e => {
    const status = document.getElementById('status');
    status.textContent = 'failed to load files';
    console.error(e);
  });

  root.querySelectorAll('.admin-mobile-tabs button').forEach(function (btn) {
    btn.addEventListener('click', function () {
      root.querySelectorAll('.admin-mobile-tabs button').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      root.querySelectorAll('.admin-mobile-panel').forEach(p => p.classList.remove('active'));
      const target = root.querySelector('.admin-mobile-panel[data-tab="' + btn.dataset.tab + '"]');
      if (target) target.classList.add('active');
    });
  });

  window.__admin = { api, state, renderSkeleton, el, clearChildren, baseURL, root };

  // ---- Task 17: editor behaviors + save roundtrip -------------------------

  const editor = document.getElementById('editor');
  const editorError = document.getElementById('editor-error');
  const saveBtn = document.getElementById('save-btn');
  const statusEl = document.getElementById('status');
  const filePicker = document.getElementById('file-picker');

  function setDirty(d) {
    state.dirty = d;
    saveBtn.disabled = !d;
  }
  function setError(msg) {
    editorError.textContent = msg || '';
    if (msg) editorError.classList.add('visible');
    else editorError.classList.remove('visible');
  }
  function setStatus(msg) { statusEl.textContent = msg || ''; }

  async function loadFile(path) {
    const r = await api('/files' + path);
    if (!r.ok) { setStatus('load failed'); return; }
    const text = await r.text();
    state.currentPath = path;
    state.onDiskContents = text;
    state.pendingContents = text;
    editor.value = text;
    setDirty(false);
    setError('');
    renderSkeleton(text);
  }

  filePicker.addEventListener('change', () => loadFile(filePicker.value));

  editor.addEventListener('input', () => {
    state.pendingContents = editor.value;
    setDirty(editor.value !== state.onDiskContents);
    renderSkeleton(editor.value);
    // Only strict-validate the main file (includes are YAML fragments).
    if (state.files.length > 0 && state.currentPath === state.files[0].path) {
      clearTimeout(editor.__validateTimer);
      editor.__validateTimer = setTimeout(validatePending, 350);
    } else {
      setError('');
    }
  });

  editor.addEventListener('keydown', (e) => {
    if (e.key === 'Tab') {
      e.preventDefault();
      const start = editor.selectionStart;
      const end = editor.selectionEnd;
      const before = editor.value.substring(0, start);
      const sel = editor.value.substring(start, end);
      const after = editor.value.substring(end);
      if (e.shiftKey) {
        const lineStart = before.lastIndexOf('\n') + 1;
        const block = editor.value.substring(lineStart, end);
        const dedented = block.replace(/^ {1,2}/gm, '');
        editor.value = editor.value.substring(0, lineStart) + dedented + after;
        editor.selectionStart = editor.selectionEnd = start;
      } else if (sel.indexOf('\n') >= 0) {
        const lineStart = before.lastIndexOf('\n') + 1;
        const block = editor.value.substring(lineStart, end);
        const indented = block.replace(/^/gm, '  ');
        editor.value = editor.value.substring(0, lineStart) + indented + after;
        editor.selectionStart = lineStart;
        editor.selectionEnd = lineStart + indented.length;
      } else {
        editor.value = before + '  ' + after;
        editor.selectionStart = editor.selectionEnd = start + 2;
      }
      editor.dispatchEvent(new Event('input'));
    } else if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      save();
    }
  });

  async function validatePending() {
    const r = await api('/validate', { method: 'POST', body: state.pendingContents, headers: { 'Content-Type': 'text/plain' } });
    if (r.ok) { setError(''); return true; }
    const body = await r.json().catch(() => ({}));
    setError(body.error || 'invalid config');
    return false;
  }

  async function currentGen() {
    const r = await api('/config-generation');
    if (!r.ok) return -1;
    const b = await r.json();
    return b.generation;
  }
  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  async function save() {
    if (!state.currentPath || !state.dirty) return;
    setStatus('saving…');
    const prevGen = await currentGen();
    const r = await api('/files' + state.currentPath, {
      method: 'PUT',
      body: state.pendingContents,
      headers: { 'Content-Type': 'text/plain' },
    });
    if (!r.ok) {
      const body = await r.json().catch(() => ({}));
      setError(body.error || 'save failed');
      setStatus('save failed');
      return;
    }
    state.onDiskContents = state.pendingContents;
    setDirty(false);
    setStatus('saved; waiting for reload…');
    const deadline = Date.now() + 3000;
    while (Date.now() < deadline) {
      const g = await currentGen();
      if (g !== prevGen) { setStatus('reloaded ✓'); return; }
      await sleep(150);
    }
    setStatus('saved (no reload detected)');
  }
  saveBtn.addEventListener('click', save);

  const initialLoad = setInterval(() => {
    if (state.files.length > 0) {
      loadFile(state.files[0].path);
      clearInterval(initialLoad);
    }
  }, 50);

  // ---- Task 18: insert-widget menu ---------------------------------------

  const insertBtn = document.getElementById('insert-widget-btn');
  let stubs = null;
  insertBtn.addEventListener('click', async () => {
    if (!stubs) {
      const r = await api('/widget-stubs');
      stubs = await r.json();
    }
    const names = Object.keys(stubs).sort();
    const name = window.prompt('Widget type to insert:\n' + names.join(', '));
    if (!name) return;
    const stub = stubs[name];
    if (!stub) { setStatus('no stub for "' + name + '"'); return; }

    const pos = editor.selectionStart;
    const before = editor.value.substring(0, pos);
    const lineStart = before.lastIndexOf('\n') + 1;
    const indent = (before.substring(lineStart).match(/^ */) || [''])[0];
    const indented = stub.split('\n').map((line, i) => i === 0 ? line : (line ? indent + line : line)).join('\n');
    editor.value = before + indented + editor.value.substring(pos);
    editor.selectionStart = editor.selectionEnd = pos + indented.length;
    editor.dispatchEvent(new Event('input'));
  });

  // ---- Task 19: history tab + full-render preview -------------------------

  const historyList = document.getElementById('history-list');

  async function loadHistory() {
    clearChildren(historyList);
    const r = await api('/history');
    if (!r.ok) {
      historyList.appendChild(el('div', null, 'failed to load history'));
      return;
    }
    const entries = await r.json();
    entries.forEach(entry => {
      const row = el('div', { class: 'admin-history-entry' });
      row.appendChild(el('strong', null, entry.Message));
      const meta = el('div', { class: 'meta' });
      meta.textContent = entry.Time + ' · ' + entry.Email + ' · ' + (entry.SHA || '').substring(0, 8);
      row.appendChild(meta);
      row.addEventListener('click', async () => {
        if (!confirm('Restore to commit ' + entry.SHA.substring(0, 8) + '?\n' + entry.Message)) return;
        const rr = await api('/history/' + entry.SHA + '/restore', { method: 'POST' });
        if (!rr.ok) { setStatus('restore failed'); return; }
        setStatus('restored ✓ reloading…');
        setTimeout(() => window.location.reload(), 800);
      });
      historyList.appendChild(row);
    });
  }

  root.querySelectorAll('.admin-mobile-tabs button[data-tab="history"]').forEach(btn => {
    btn.addEventListener('click', loadHistory);
  });
  loadHistory();

  const pvtabs = root.querySelectorAll('.admin-preview-tabs button');
  const previewIframe = document.getElementById('preview-iframe');
  const previewSkel = document.getElementById('preview-skeleton');
  pvtabs.forEach(b => {
    b.addEventListener('click', async () => {
      pvtabs.forEach(x => x.classList.remove('active'));
      b.classList.add('active');
      if (b.dataset.pvtab === 'skeleton') {
        previewIframe.classList.remove('visible');
        previewSkel.style.display = '';
      } else {
        previewSkel.style.display = 'none';
        setStatus('rendering full preview…');
        const r = await api('/preview', { method: 'POST', body: state.pendingContents, headers: { 'Content-Type': 'text/plain' } });
        if (!r.ok) {
          const body = await r.json().catch(() => ({}));
          setStatus('preview failed');
          setError(body.error || 'preview failed');
          return;
        }
        const { preview_id } = await r.json();
        previewIframe.src = baseURL + '/admin/preview/' + preview_id + '/';
        previewIframe.classList.add('visible');
        setStatus('preview ready');
      }
    });
  });
})();
