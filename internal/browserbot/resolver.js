// Spartask player helper. Called with Runtime.callFunctionOn; selectors and
// values always arrive as arguments, never as script text.
function (selectors, action, value) {
  const norm = (t) => (t || '').replace(/\s+/g, ' ').trim();
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    const s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none';
  };
  const byLabel = (text) => {
    const out = [];
    for (const l of document.querySelectorAll('label')) {
      if (norm(l.textContent) === text && l.control) out.push(l.control);
    }
    return out;
  };
  const byText = (text, tag) => {
    const out = [];
    let nodes;
    try { nodes = document.querySelectorAll(tag || 'button,a,[role=button],[role=link],[role=menuitem],[role=tab],[role=option],summary,label'); } catch (e) { return out; }
    for (const el of nodes) if (norm(el.innerText) === text) out.push(el);
    return out;
  };
  const pageURL = location.origin + location.pathname;
  const passwordVisible = () => Array.from(document.querySelectorAll('input[type=password]')).some(visible);

  for (let i = 0; i < selectors.length; i++) {
    const s = selectors[i];
    let els = [];
    try {
      if (s.kind === 'css' || s.kind === 'path') els = Array.from(document.querySelectorAll(s.value));
      else if (s.kind === 'label') els = byLabel(s.value);
      else if (s.kind === 'text') els = byText(s.value, s.tag);
    } catch (e) { continue; }
    const el = els.find(visible);
    if (!el) continue;

    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2;
    const y = r.top + r.height / 2;
    const hitEl = document.elementFromPoint(x, y);
    const res = { found: true, index: i, x: x, y: y, hit: !!hitEl && (el === hitEl || el.contains(hitEl) || hitEl.contains(el)), disabled: !!el.disabled, ok: true, text: '', page_url: pageURL };

    if (action === 'focus') {
      el.focus();
      if (typeof el.select === 'function') el.select();
    } else if (action === 'blur') {
      el.blur();
    } else if (action === 'select') {
      if (el.tagName !== 'SELECT') { res.ok = false; return res; }
      let opt = Array.from(el.options).find((o) => o.value === value);
      if (!opt) opt = Array.from(el.options).find((o) => norm(o.textContent) === value);
      if (!opt) { res.ok = false; return res; }
      el.value = opt.value;
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    } else if (action === 'text') {
      res.text = norm(el.innerText !== undefined && el.innerText !== '' ? el.innerText : el.value);
    }
    return res;
  }
  return { found: false, index: -1, password_visible: passwordVisible(), page_url: pageURL };
}
