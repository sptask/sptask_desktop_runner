// Spartask recorder. Injected into every document of the runner's own browser
// while the user records. It only reports what the user did; it never acts.
(() => {
  if (window.top !== window || window.__sptRecorderInstalled) return;
  window.__sptRecorderInstalled = true;

  const BINDING = '__sptRecord';
  const MAX_TEXT = 80;
  const SENSITIVE_AUTOCOMPLETE = /^(current-password|new-password|one-time-code|cc-number|cc-csc|cc-exp|cc-exp-month|cc-exp-year)$/;
  const TEXT_INPUT_TYPES = new Set(['', 'text', 'email', 'search', 'tel', 'url', 'number', 'password', 'date', 'datetime-local', 'month', 'time', 'week']);
  const CLICKABLE = 'button,a,input,select,textarea,label,summary,[role=button],[role=link],[role=menuitem],[role=tab],[role=option],[role=checkbox],[role=radio],[onclick]';
  const lastValue = new WeakMap();

  const send = (step) => {
    const fn = window[BINDING];
    if (typeof fn === 'function') fn(JSON.stringify(step));
  };
  const norm = (t) => (t || '').replace(/\s+/g, ' ').trim();
  const quote = (v) => v.replace(/["\\]/g, '\\$&');
  const pageURL = () => location.origin + location.pathname;
  const unique = (sel) => {
    try { return document.querySelectorAll(sel).length === 1; } catch (e) { return false; }
  };

  const isTextField = (el) => {
    const tag = el.tagName;
    if (tag === 'TEXTAREA') return true;
    if (tag !== 'INPUT') return false;
    return TEXT_INPUT_TYPES.has((el.getAttribute('type') || '').toLowerCase());
  };
  const isSensitive = (el) => {
    if ((el.getAttribute('type') || '').toLowerCase() === 'password') return true;
    return SENSITIVE_AUTOCOMPLETE.test((el.getAttribute('autocomplete') || '').toLowerCase().trim());
  };

  const labelText = (el) => {
    if (el.labels && el.labels.length > 0) {
      const t = norm(el.labels[0].textContent);
      if (t && t.length <= MAX_TEXT) return t;
    }
    return '';
  };

  const cssPath = (el) => {
    const parts = [];
    let node = el;
    while (node && node.nodeType === 1 && node !== document.documentElement && parts.length < 8) {
      const tag = node.tagName.toLowerCase();
      if (node.id && !/\d{3,}/.test(node.id)) {
        parts.unshift('#' + CSS.escape(node.id));
        break;
      }
      let idx = 1;
      let sib = node.previousElementSibling;
      while (sib) { if (sib.tagName === node.tagName) idx++; sib = sib.previousElementSibling; }
      parts.unshift(tag + ':nth-of-type(' + idx + ')');
      node = node.parentElement;
    }
    return parts.join(' > ');
  };

  const selectorsFor = (el) => {
    const out = [];
    const tag = el.tagName.toLowerCase();
    for (const attr of ['data-testid', 'data-test', 'data-qa', 'data-cy']) {
      const v = el.getAttribute(attr);
      if (v) { const s = '[' + attr + '="' + quote(v) + '"]'; if (unique(s)) out.push({ kind: 'css', value: s }); }
    }
    if (el.id && !/\d{3,}/.test(el.id)) {
      const s = '#' + CSS.escape(el.id);
      if (unique(s)) out.push({ kind: 'css', value: s });
    }
    const name = el.getAttribute('name');
    if (name) { const s = tag + '[name="' + quote(name) + '"]'; if (unique(s)) out.push({ kind: 'css', value: s }); }
    const aria = el.getAttribute('aria-label');
    if (aria) { const s = tag + '[aria-label="' + quote(aria) + '"]'; if (unique(s)) out.push({ kind: 'css', value: s }); }
    const label = labelText(el);
    if (label) out.push({ kind: 'label', value: label });
    if (!isTextField(el) && tag !== 'select') {
      const text = norm(el.innerText);
      if (text && text.length <= MAX_TEXT) out.push({ kind: 'text', value: text, tag: tag });
    }
    const path = cssPath(el);
    if (path) out.push({ kind: 'css', value: path });
    return out.slice(0, 6);
  };

  const describe = (el) => norm(labelText(el) || el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.getAttribute('name') || el.innerText || el.tagName).slice(0, MAX_TEXT);

  const recordFill = (el) => {
    const sensitive = isSensitive(el);
    const value = sensitive ? '' : el.value;
    const key = sensitive ? '\u0000sensitive' : value;
    if (lastValue.get(el) === key) return;
    lastValue.set(el, key);
    send({ type: 'fill', selectors: selectorsFor(el), value: value, sensitive: sensitive, label: describe(el), page_url: pageURL() });
  };

  document.addEventListener('click', (e) => {
    if (!e.isTrusted) return;
    const target = e.target instanceof Element ? (e.target.closest(CLICKABLE) || e.target) : null;
    if (!target || isTextField(target) || target.tagName === 'SELECT' || target.tagName === 'OPTION') return;
    send({ type: 'click', selectors: selectorsFor(target), label: describe(target), page_url: pageURL() });
  }, true);

  document.addEventListener('change', (e) => {
    if (!e.isTrusted) return;
    const el = e.target;
    if (!(el instanceof Element)) return;
    if (el.tagName === 'SELECT') {
      const opt = el.selectedOptions && el.selectedOptions[0];
      send({ type: 'select', selectors: selectorsFor(el), value: el.value, label: opt ? norm(opt.textContent).slice(0, MAX_TEXT) : describe(el), page_url: pageURL() });
      return;
    }
    if (isTextField(el)) recordFill(el);
  }, true);

  document.addEventListener('keydown', (e) => {
    if (!e.isTrusted || e.key !== 'Enter' || e.isComposing) return;
    const el = e.target;
    if (!(el instanceof Element) || !isTextField(el) || el.tagName === 'TEXTAREA') return;
    recordFill(el);
    send({ type: 'press', key: 'Enter', selectors: selectorsFor(el), label: describe(el), page_url: pageURL() });
  }, true);
})();
