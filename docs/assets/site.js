// Behaviour every page shares: the phone menu, copy and print buttons, the blog's
// reading aids, and animation loops that run only while someone can see them.
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
    function redraw() { drewStill = false; kick(); }
    window.addEventListener('resize', redraw);
    onChange(reduce, redraw);
    onTheme(redraw);
    kick();
  }

  // The theme follows the OS unless the viewer picks one; the choice is saved per
  // browser. The inline script in each page's <head> applies it before CSS paints.
  var KEY = 'hydra-theme', themeFns = [];
  var NAME = { system: 'Theme: match system', light: 'Theme: light', dark: 'Theme: dark' };
  function onTheme(fn) { themeFns.push(fn); }
  // The theme the OS is not showing comes first, so the first press always changes
  // the screen: system, the other theme, the OS's own, system.
  function nextTheme(mode) {
    var order = osDark.matches ? ['system', 'light', 'dark'] : ['system', 'dark', 'light'];
    return order[(order.indexOf(mode) + 1) % order.length];
  }
  function themeChanged() {
    resolved = {};
    for (var i = 0; i < themeFns.length; i++) themeFns[i]();
  }
  var osDark = window.matchMedia('(prefers-color-scheme: dark)');
  onChange(osDark, themeChanged);
  function setTheme(mode) {
    if (mode === 'system') delete root.dataset.theme; else root.dataset.theme = mode;
    try { if (mode === 'system') localStorage.removeItem(KEY); else localStorage.setItem(KEY, mode); } catch (e) { /* this page still follows it */ }
    themeChanged();
  }
  var ICONS = '<svg class="i-system" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8"/><path d="M12 4a8 8 0 0 1 0 16z" class="fill"/></svg>' +
    '<svg class="i-light" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M4.9 19.1L7 17M17 7l2.1-2.1"/></svg>' +
    '<svg class="i-dark" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/></svg>';
  // Built by script because without script it could not work; the page then follows the OS.
  function themeToggle(header) {
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'theme-toggle';
    b.innerHTML = ICONS + '<span class="visually-hidden"></span>';
    var label = b.lastChild;
    function show() {
      var mode = root.dataset.theme || 'system';
      b.dataset.mode = mode;
      label.textContent = NAME[mode];
      b.title = NAME[mode] + '. Click to change.';
    }
    b.addEventListener('click', function () {
      setTheme(nextTheme(b.dataset.mode));
      show();
      announce(NAME[b.dataset.mode]);
    });
    show();
    header.appendChild(b);
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

  // Blog posts: a bar for how far the reader is, and a rail that marks the section on screen.
  function readingAids() {
    var bar = document.querySelector('.progress-bar'), queued = false;
    function draw() {
      queued = false;
      var h = document.documentElement, max = h.scrollHeight - h.clientHeight;
      bar.style.transform = 'scaleX(' + (max > 0 ? Math.min(1, h.scrollTop / max) : 0) + ')';
    }
    function schedule() { if (!queued) { queued = true; requestAnimationFrame(draw); } }
    if (bar) {
      window.addEventListener('scroll', schedule, { passive: true });
      window.addEventListener('resize', schedule);
      draw();
    }
    var links = document.querySelectorAll('.rail a[href^="#"]');
    if (!links.length || !('IntersectionObserver' in window)) return;
    var byId = {};
    var io = new IntersectionObserver(function (es) {
      es.forEach(function (e) {
        if (!e.isIntersecting) return;
        for (var i = 0; i < links.length; i++) links[i].classList.remove('active');
        byId[e.target.id].classList.add('active');
      });
    }, { rootMargin: '-40% 0px -50% 0px' });
    for (var i = 0; i < links.length; i++) {
      var sec = document.getElementById(links[i].getAttribute('href').slice(1));
      if (sec) { byId[sec.id] = links[i]; io.observe(sec); }
    }
  }

  function wire() {
    live = document.createElement('div');
    live.className = 'visually-hidden';
    live.setAttribute('role', 'status');
    document.body.appendChild(live);
    var header = document.querySelector('.site-header');
    if (header) themeToggle(header);
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
      if (e.target.closest('[data-print]')) window.print();
    });

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
  // Resolved once per role per theme, since canvases ask inside their per-frame loops;
  // a page that keeps colours of its own refreshes them in Hydra.onTheme.
  var resolved = {};
  function color(role) {
    if (!(role in resolved)) resolved[role] = getComputedStyle(root).getPropertyValue('--hy-color-' + role).trim();
    return resolved[role];
  }
  function rgb(role) {
    var h = color(role);
    return [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)];
  }
  function rgba(role, a) { return 'rgba(' + rgb(role).join(',') + ',' + a + ')'; }
  // Whether the dark theme is in force, for drawing that runs the other way on each
  // (additive light, white highlights). Read on a theme change, not per frame.
  function dark() { return getComputedStyle(root).getPropertyValue('--hy-theme-dark').trim() === '1'; }

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

  window.Hydra = { loop: loop, color: color, rgb: rgb, rgba: rgba, dark: dark, onTheme: onTheme };
  function init() { wire(); reveal(); readingAids(); focusScrollers(); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
