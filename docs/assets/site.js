// Behaviour every page shares: the phone menu, copy buttons, and animation loops
// that run only while someone can see them.
(function () {
  'use strict';
  var root = document.documentElement;
  root.classList.add('js');
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  // Safari before 14 has only addListener on a MediaQueryList.
  function onChange(mq, fn) { if (mq.addEventListener) mq.addEventListener('change', fn); else mq.addListener(fn); }

  // Present from load: many screen readers ignore a live region added at the moment it speaks.
  var live;
  function announce(msg) {
    live.textContent = '';
    setTimeout(function () { live.textContent = msg; }, 50);
  }

  // Calls frame(t) every animation frame while el is on screen, the page is visible
  // and el has a size; under reduced motion it draws one still frame instead.
  // A frame that throws stops its own loop, so one fault is one error, not sixty a second.
  function loop(el, frame) {
    var seen = false, raf = 0, dead = false, drewStill = false;
    function ready() { return seen && !dead && !document.hidden && el.clientWidth > 0; }
    function tick(t) {
      raf = 0;
      if (!ready()) return;
      try { frame(t); } catch (e) { dead = true; throw e; }
      kick();
    }
    function kick() {
      if (!ready()) return;
      if (reduce.matches) {
        if (!drewStill) { drewStill = true; tick(0); }
        return;
      }
      if (!raf) raf = requestAnimationFrame(tick);
    }
    if ('IntersectionObserver' in window) {
      new IntersectionObserver(function (es) { seen = es[es.length - 1].isIntersecting; kick(); }).observe(el);
    } else {
      seen = true;
    }
    document.addEventListener('visibilitychange', kick);
    window.addEventListener('resize', function () { drewStill = false; kick(); });
    onChange(reduce, function () { drewStill = false; kick(); });
    kick();
  }

  // The async clipboard can exist and still refuse (permissions, iframes), so it falls back.
  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).catch(function () { return legacyCopy(text); });
    }
    return legacyCopy(text);
  }
  // Selecting the textarea moves focus, so focus goes back to the button afterwards.
  function legacyCopy(text) {
    var back = document.activeElement, ta = document.createElement('textarea'), ok = false;
    ta.value = text; ta.setAttribute('readonly', ''); ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    if (back && back.focus) back.focus();
    return ok ? Promise.resolve() : Promise.reject(new Error('copy refused'));
  }

  // Sections fade in as they scroll into view; the page never depends on a page script for it.
  function reveal() {
    var els = document.querySelectorAll('.reveal');
    if (!('IntersectionObserver' in window) || reduce.matches) {
      for (var i = 0; i < els.length; i++) els[i].classList.add('in');
      return;
    }
    var io = new IntersectionObserver(function (es) {
      es.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); } });
    }, { threshold: 0.12 });
    for (var j = 0; j < els.length; j++) io.observe(els[j]);
  }

  function wire() {
    live = document.createElement('div');
    live.className = 'visually-hidden';
    live.setAttribute('role', 'status');
    document.body.appendChild(live);
    var header = document.querySelector('.site-header');
    var btn = header && header.querySelector('.nav-toggle');
    if (btn) {
      var set = function (open) {
        header.classList.toggle('open', open);
        btn.setAttribute('aria-expanded', String(open));
      };
      btn.addEventListener('click', function () { set(btn.getAttribute('aria-expanded') !== 'true'); });
      document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && header.classList.contains('open')) { set(false); btn.focus(); }
      });
      document.addEventListener('click', function (e) {
        if (!header.contains(e.target) || e.target.closest('.site-nav a')) set(false);
      });
    }

    document.addEventListener('click', function (e) {
      var b = e.target.closest('[data-copy]');
      if (!b) return;
      var label = b.querySelector('.cp') || b;
      if (!('label' in b.dataset)) b.dataset.label = label.textContent;
      copyText(b.getAttribute('data-copy')).then(function () {
        b.classList.add('done', 'copied');
        label.textContent = 'copied';
        announce('Copied to the clipboard');
        clearTimeout(b._copyReset);
        b._copyReset = setTimeout(function () {
          b.classList.remove('done', 'copied');
          label.textContent = b.dataset.label;
        }, 1600);
      }, function () { announce('Copy failed; select the command and copy it by hand'); });
    });
  }

  // Canvas and SVG drawing read the same tokens CSS does, by role name ('accent-default').
  // Resolved once per role, since canvases ask inside their per-frame loops.
  var resolved = {};
  onChange(window.matchMedia('(prefers-color-scheme: dark)'), function () { resolved = {}; });
  function color(role) {
    if (!(role in resolved)) resolved[role] = getComputedStyle(root).getPropertyValue('--hy-color-' + role).trim();
    return resolved[role];
  }
  function rgb(role) {
    var h = color(role);
    return [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)];
  }
  function rgba(role, a) { return 'rgba(' + rgb(role).join(',') + ',' + a + ')'; }

  // A region that scrolls sideways must take focus, or a keyboard cannot scroll it
  // (WCAG 2.1.1). Only regions that overflow at this width get a tab stop.
  function focusScrollers() {
    var els = document.querySelectorAll('pre, .copyline code, .scroll-x, .term');
    for (var i = 0; i < els.length; i++) {
      var e = els[i], over = e.scrollWidth > e.clientWidth + 1;
      if (over && !e.hasAttribute('tabindex')) { e.setAttribute('tabindex', '0'); e.dataset.scrollFocus = ''; }
      else if (!over && 'scrollFocus' in e.dataset) { e.removeAttribute('tabindex'); delete e.dataset.scrollFocus; }
    }
  }
  // Tabs and <details> reveal hidden regions without a resize, so re-check after those too.
  var scrollTimer;
  function recheck() { clearTimeout(scrollTimer); scrollTimer = setTimeout(focusScrollers, 150); }
  window.addEventListener('resize', recheck);
  document.addEventListener('click', recheck);
  document.addEventListener('keyup', recheck);
  document.addEventListener('toggle', recheck, true);

  window.Hydra = { loop: loop, color: color, rgb: rgb, rgba: rgba };
  function init() { wire(); reveal(); focusScrollers(); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
