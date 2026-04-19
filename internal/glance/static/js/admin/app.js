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
})();
