// Draws the operator's cursor, name badge and a coloured frame over the page
// while a replay runs. It lives in a closed shadow root on <html> (never in
// <body>, so text steps do not see it), ignores the mouse (so hit tests and
// clicks pass through) and is styled through CSSOM properties, which a page's
// Content-Security-Policy does not block. All text goes in as textContent.
// Arguments: name, color, label, hint, x, y (x < 0 means "keep the position").
function (name, color, label, hint, x, y) {
  const key = Symbol.for('spartask.operator-overlay');
  const set = (el, styles) => { for (const k in styles) el.style.setProperty(k, styles[k], 'important'); return el; };
  let o = globalThis[key];
  if (!o || !o.host.isConnected) {
    const host = document.createElement('spartask-operator');
    set(host, { position: 'fixed', inset: '0', 'z-index': '2147483647', 'pointer-events': 'none', display: 'block', margin: '0', padding: '0', border: '0', background: 'transparent' });
    const root = host.attachShadow({ mode: 'closed' });
    const frame = set(document.createElement('div'), { position: 'fixed', inset: '0', 'box-sizing': 'border-box', 'border-width': '3px', 'border-style': 'solid', 'pointer-events': 'none' });
    const svgNS = 'http://www.w3.org/2000/svg';
    const pointer = document.createElementNS(svgNS, 'svg');
    pointer.setAttribute('width', '28');
    pointer.setAttribute('height', '28');
    pointer.setAttribute('viewBox', '0 0 24 24');
    const arrow = document.createElementNS(svgNS, 'path');
    arrow.setAttribute('d', 'M3 2l7 19 2.6-7.4L20 11z');
    arrow.setAttribute('stroke', '#ffffff');
    arrow.setAttribute('stroke-width', '1.5');
    arrow.setAttribute('stroke-linejoin', 'round');
    pointer.appendChild(arrow);
    const badge = set(document.createElement('div'), { 'margin-left': '18px', 'margin-top': '-4px', padding: '3px 8px', 'border-radius': '10px', color: '#ffffff', font: '600 12px/1.4 system-ui, sans-serif', 'white-space': 'nowrap', 'box-shadow': '0 1px 4px rgba(0,0,0,.3)' });
    const cursor = set(document.createElement('div'), { position: 'fixed', left: '0', top: '0', transition: 'transform 350ms ease-out', 'pointer-events': 'none', transform: 'translate(40px, 40px)' });
    cursor.append(pointer, badge);
    const banner = set(document.createElement('div'), { position: 'fixed', left: '12px', bottom: '12px', padding: '6px 12px', 'border-radius': '8px', color: '#ffffff', font: '500 12px/1.4 system-ui, sans-serif', 'max-width': '60vw', overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', 'box-shadow': '0 1px 6px rgba(0,0,0,.3)', 'pointer-events': 'none' });
    root.append(frame, cursor, banner);
    document.documentElement.appendChild(host);
    o = { host, frame, arrow, badge, cursor, banner };
    Object.defineProperty(globalThis, key, { value: o, configurable: true });
    // Shift+Esc stops the run. The bot never sends Shift, so only the person
    // at the computer can press it.
    window.addEventListener('keydown', (e) => {
      if (e.isTrusted && e.key === 'Escape' && e.shiftKey && typeof globalThis.__sptStop === 'function') {
        globalThis.__sptStop('user');
      }
    }, true);
  }
  set(o.frame, { 'border-color': color });
  o.arrow.setAttribute('fill', color);
  set(o.badge, { background: color });
  set(o.banner, { background: color });
  o.badge.textContent = name;
  o.banner.textContent = label ? name + ' · ' + label + ' · ' + hint : name + ' · ' + hint;
  if (x >= 0 && y >= 0) set(o.cursor, { transform: 'translate(' + Math.round(x) + 'px, ' + Math.round(y) + 'px)' });
  return true;
}
