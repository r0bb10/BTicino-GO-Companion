'use strict';

var _session = null;

function escapeHtml(str) {
  if (typeof str !== 'string') str = String(str);
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function apiRequest(url, opts) {
  opts = opts || {};
  var fetchOpts = {
    method: opts.method || 'GET',
    credentials: 'same-origin',
    headers: opts.body ? { 'Content-Type': 'application/json' } : undefined,
    body: opts.body ? JSON.stringify(opts.body) : undefined
  };
  return fetch(url, fetchOpts).then(function(r) {
    return r.text().then(function(text) {
      var data = {};
      if (text) {
        try { data = JSON.parse(text); } catch (e) { data = { error: text }; }
      }
      if (!r.ok) {
        var err = new Error(data.error || r.statusText || 'Request failed');
        err.status = r.status;
        err.data = data;
        throw err;
      }
      if (opts.cache) {
        try { sessionStorage.setItem('cache:' + url, JSON.stringify(data)); } catch (e) {}
      }
      return data;
    });
  });
}

function apiGet(url, opts) { return apiRequest(url, opts); }
function apiPost(url, data) { return apiRequest(url, { method: 'POST', body: data }); }
function apiPut(url, data) { return apiRequest(url, { method: 'PUT', body: data }); }

function handleApiError(err, fallback) {
  if (err && (err.status === 401 || err.status === 403)) {
    _session = null;
    stopPolling();
    show('loginView');
    showToast(err.message || 'Authentication required', 'error');
    return;
  }
  if (fallback) showToast(fallback, 'error');
}

function getCached(url) {
  try {
    var raw = sessionStorage.getItem('cache:' + url);
    return raw ? JSON.parse(raw) : null;
  } catch (e) { return null; }
}

function showToast(msg, type) {
  var t = document.getElementById('_toast');
  if (!t) {
    t = document.createElement('div');
    t.id = '_toast';
    document.body.appendChild(t);
  }
  t.textContent = msg;
  t.className = 'toast toast-' + (type || 'info') + ' show';
  clearTimeout(t._tid);
  t._tid = setTimeout(function() { t.classList.remove('show'); }, 3500);
}

function switchPage(pageId) {
  document.querySelectorAll('.page').forEach(function(p) { p.classList.add('hidden'); });
  document.getElementById(pageId + 'Page').classList.remove('hidden');
  document.querySelectorAll('.nav-links a, .nav-links .nav-dropdown-toggle').forEach(function(a) { a.classList.remove('active'); });
  var link = document.querySelector('.nav-links a[data-page="' + pageId + '"]');
  if (link) link.classList.add('active');
  /* highlight parent dropdown toggle if any */
  var parentLi = link ? link.closest('.nav-dropdown') : null;
  if (parentLi) {
    var toggle = parentLi.querySelector('.nav-dropdown-toggle');
    if (toggle) toggle.classList.add('active');
  }
  if (pageId === 'about') renderAbout();
}

/* ──── Nav bar ──── */
function buildNav() {
  var nav = document.getElementById('nav');
  if (!nav) return;
  var pages = [
    { id: 'config', label: 'Config' },
    {
      label: 'Logger',
      children: [
        { id: 'logs', label: 'Logs' },
        { id: 'busframes', label: 'BUS Frames' }
      ]
    },
    { id: 'about', label: 'About' }
  ];
  var html = '<span class="nav-brand"><img src="/bticino-logo.svg" height="24" alt=""> BTicino Companion</span><ul class="nav-links">';
  for (var i = 0; i < pages.length; i++) {
    var pg = pages[i];
    if (pg.children) {
      var childActive = false;
      for (var c = 0; c < pg.children.length; c++) {
        if (document.getElementById(pg.children[c].id + 'Page') && !document.getElementById(pg.children[c].id + 'Page').classList.contains('hidden')) {
          childActive = true;
          break;
        }
      }
      html += '<li class="nav-dropdown">';
      html += '<a class="nav-dropdown-toggle' + (childActive ? ' active' : '') + '">' + escapeHtml(pg.label) + '</a>';
      html += '<ul class="nav-dropdown-menu">';
      for (var j = 0; j < pg.children.length; j++) {
        var ch = pg.children[j];
        html += '<li><a data-page="' + ch.id + '" onclick="switchPage(\'' + ch.id + '\')">' + escapeHtml(ch.label) + '</a></li>';
      }
      html += '</ul></li>';
    } else {
      html += '<li><a data-page="' + pg.id + '" onclick="switchPage(\'' + pg.id + '\')">' + escapeHtml(pg.label) + '</a></li>';
    }
  }
  html += '<li><button class="logout-link" onclick="logout()">Log Out</button></li>';
  html += '</ul>';
  nav.innerHTML = html;
  /* activate first page */
  var first = document.querySelector('.nav-links a[data-page]');
  if (first) first.classList.add('active');
}

/* ──── Session ──── */
function checkSession() {
  return apiGet('/api/session', { cache: true }).then(function(session) {
    _session = session;
    if (!session.authenticated) {
      var help = document.getElementById('loginHelp');
      if (session.bootstrap_required) {
        help.innerHTML = 'First login uses <strong>companion / companion</strong>, then you must replace the default credentials.';
        help.style.display = 'block';
      } else {
        help.style.display = 'none';
      }
      show('loginView');
      return;
    }
    if (session.bootstrap) {
      show('setupView');
      return;
    }
    show('appView');
    buildNav();
    switchPage('about');
  }).catch(function(err) {
    show('loginView');
    showToast(err.message || 'Failed to connect', 'error');
  });
}

function show(viewId) {
  document.getElementById('loginView').classList.add('hidden');
  document.getElementById('setupView').classList.add('hidden');
  document.getElementById('appView').classList.add('hidden');
  document.getElementById(viewId).classList.remove('hidden');
}

/* ──── Login ──── */
function submitLogin(event) {
  event.preventDefault();
  var form = new FormData(event.target);
  var btn = event.target.querySelector('.btn');
  btn.disabled = true;
  btn.textContent = 'Signing In…';
  var errEl = document.getElementById('loginError');
  errEl.classList.remove('visible');

  apiPost('/api/login', {
    username: form.get('username'),
    password: form.get('password')
  }).then(function(data) {
    if (data.error) {
      errEl.textContent = data.error;
      errEl.classList.add('visible');
      btn.disabled = false;
      btn.textContent = 'Sign In';
      return;
    }
    btn.textContent = 'Sign In';
    checkSession();
  }).catch(function(err) {
    errEl.textContent = err.message || 'Connection error';
    errEl.classList.add('visible');
    btn.disabled = false;
    btn.textContent = 'Sign In';
  });
}

function submitSetup(event) {
  event.preventDefault();
  var form = new FormData(event.target);
  var btn = event.target.querySelector('.btn');
  btn.disabled = true;
  btn.textContent = 'Saving…';
  var errEl = document.getElementById('setupError');
  errEl.classList.remove('visible');

  apiPost('/api/credentials', {
    username: form.get('username'),
    password: form.get('password'),
    current_password: ''
  }).then(function(data) {
    if (data.error) {
      errEl.textContent = data.error;
      errEl.classList.add('visible');
      btn.disabled = false;
      btn.textContent = 'Save Credentials';
      return;
    }
    showToast('Credentials saved. Sign in with the new account.', 'success');
    btn.textContent = 'Save Credentials';
    show('loginView');
    document.getElementById('loginForm').reset();
  }).catch(function(err) {
    errEl.textContent = err.message || 'Connection error';
    errEl.classList.add('visible');
    btn.disabled = false;
    btn.textContent = 'Save Credentials';
  });
}

function logout() {
  apiPost('/api/logout', {}).then(function() {
    showToast('Signed out.', 'info');
    _session = null;
    show('loginView');
    document.getElementById('loginForm').reset();
  }).catch(function(err) {
    showToast(err.message || 'Logout failed', 'error');
  });
}

/* ──── About page ──── */
function renderAbout() {
  var cached = getCached('/api/session');
  if (cached) renderAboutData(cached);
  apiGet('/api/session', { cache: true }).then(renderAboutData).catch(function(err) { handleApiError(err); });
  var cachedStatus = getCached('/api/status');
  if (cachedStatus) renderStatusData(cachedStatus);
  apiGet('/api/status', { cache: true }).then(renderStatusData).catch(function(err) { handleApiError(err); });
}

function renderAboutData(session) {
  if (!session) return;
  document.getElementById('aboutVersion').textContent = session.version || '-';
  document.getElementById('aboutGitSHA').textContent = session.git_sha && session.git_sha !== '-' ? session.git_sha.substring(0, 10) + '…' : '-';
}

function renderStatusData(status) {
  if (!status) return;
  document.getElementById('statusModel').textContent = status.model || '-';
  document.getElementById('statusFirmware').textContent = status.firmware || '-';
  document.getElementById('statusHardware').textContent = status.hardware || '-';

  var update = status.update_status || {stage: 'checking'};
  var badge = document.getElementById('aboutUpdateBadge');
  var label = update.stage;
  if (update.stage === 'available') {
    label = update.available || 'Available';
    badge.className = 'badge badge-info';
  } else if (update.stage === 'healthy' || update.stage === 'idle') {
    label = 'Up-to-date';
    badge.className = 'badge badge-success';
  } else if (update.stage === 'checking') {
    label = 'Checking…';
    badge.className = 'badge badge-info';
  } else if (update.stage === 'failed') {
    label = 'Update failed';
    badge.className = 'badge badge-danger';
  } else if (update.stage === 'applying' || update.stage === 'restarting') {
    label = 'Applying…';
    badge.className = 'badge badge-info';
  } else {
    label = update.stage;
    badge.className = 'badge badge-info';
  }
  badge.textContent = label;

  var haBadge = document.getElementById('statusHABadge');
  haBadge.textContent = status.ha_paired ? 'Claimed' : 'Claimable';
  haBadge.className = 'badge ' + (status.ha_paired ? 'badge-success' : 'badge-info');
}

/* ──── Log / Frame page state ──── */
var _logPaused = false;
var _framePaused = false;
var _logTimer = null;
var _frameTimer = null;
var _logPrevContent = '';

function startPolling(page) {
  stopPolling();
  if (page === 'logs') { loadLoggingState(); fetchLogs(); _logTimer = setInterval(fetchLogs, 3000); }
  if (page === 'busframes') { fetchFrames(); _frameTimer = setInterval(fetchFrames, 2000); }
}

function stopPolling() {
  if (_logTimer) { clearInterval(_logTimer); _logTimer = null; }
  if (_frameTimer) { clearInterval(_frameTimer); _frameTimer = null; }
}

/* Patch switchPage to manage polling */
var _origSwitch = switchPage;
switchPage = function(pageId) {
  _origSwitch(pageId);
  if (pageId === 'logs' || pageId === 'busframes') {
    startPolling(pageId);
  } else {
    stopPolling();
  }
};

/* ──── Log viewer ──── */
function fetchLogs() {
  if (_logPaused) return;
  apiGet('/api/logs').then(renderLogs).catch(function(err) { handleApiError(err, 'Failed to load logs'); });
}

function loadLoggingState() {
  apiGet('/api/logging').then(function(data) {
    var runtimeLevel = document.getElementById('logRuntimeLevel');
    if (runtimeLevel && data.level) runtimeLevel.value = data.level;
  }).catch(function(err) { handleApiError(err, 'Failed to load logger state'); });
}

function setLoggingLevel(level) {
  apiPut('/api/logging', { level: level }).then(function(data) {
    var runtimeLevel = document.getElementById('logRuntimeLevel');
    if (runtimeLevel && data.level) runtimeLevel.value = data.level;
    showToast('Log level set to ' + data.level.toUpperCase(), 'success');
  }).catch(function(err) {
    handleApiError(err, 'Failed to update logger level');
    loadLoggingState();
  });
}

function highlightText(text, query) {
  if (!query) return escapeHtml(text);
  var lower = text.toLowerCase();
  var needle = query.toLowerCase();
  var pos = 0;
  var out = '';
  while (true) {
    var idx = lower.indexOf(needle, pos);
    if (idx === -1) break;
    out += escapeHtml(text.substring(pos, idx));
    out += '<mark class="log-highlight">' + escapeHtml(text.substring(idx, idx + query.length)) + '</mark>';
    pos = idx + query.length;
  }
  return out + escapeHtml(text.substring(pos));
}

function renderLogs(data) {
  if (!data || !data.log) return;
  var pre = document.querySelector('#logOutput pre');
  var raw = data.log;
  if (raw === _logPrevContent) return;
  _logPrevContent = raw;
  var query = document.getElementById('logSearch').value.trim();
  var lines = raw.split('\n');
  var html = '';
  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    if (!line) continue;
    var cls = 'log-line-info';
    if (/\[E\]/.test(line)) cls = 'log-line-error';
    else if (/\[W\]/.test(line)) cls = 'log-line-warn';
    else if (/\[D\]/.test(line)) cls = 'log-line-debug';
    html += '<span class="' + cls + '">' + highlightText(line, query) + '</span>\n';
  }
  pre.innerHTML = html;
  var out = document.getElementById('logOutput');
  out.scrollTop = out.scrollHeight;
}

function toggleLogPause() {
  _logPaused = !_logPaused;
  document.getElementById('logPauseBtn').textContent = _logPaused ? 'Resume' : 'Pause';
  if (!_logPaused) fetchLogs();
}

document.getElementById('logRuntimeLevel').addEventListener('change', function(event) {
  setLoggingLevel(event.target.value);
});

document.getElementById('logSearch').addEventListener('input', function() {
  _logPrevContent = '';
  fetchLogs();
});

/* ──── BUS Frame viewer ──── */
function fetchFrames() {
  if (_framePaused) return;
  apiGet('/api/frames').then(renderFrames).catch(function(err) { handleApiError(err, 'Failed to load BUS frames'); });
}

function renderFrames(data) {
  if (!data || !data.frames) return;
  var container = document.getElementById('framesOutput');
  var frames = data.frames;
  if (frames.length === 0) {
    container.innerHTML = '<div class="frame-entry"><span class="frame-raw" style="color:#888">No frames captured yet.</span></div>';
    document.getElementById('frameCount').textContent = '0 frames';
    return;
  }
  var html = '';
  for (var i = 0; i < frames.length; i++) {
    var f = frames[i];
    var t = '';
    if (f.t) {
      var d = new Date(f.t);
      t = pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
    }
    var sys = f.sys || '?';
    var raw = f.raw || '';
    var mapped = f.mapped ? ' [' + f.events + ' evt]' : '';
    html += '<div class="frame-entry">'
      + '<span class="frame-time">' + t + '</span>'
      + '<span class="frame-system">' + escapeHtml(sys) + '</span>'
      + '<span class="frame-raw">' + escapeHtml(raw) + '</span>'
      + (mapped ? '<span class="frame-mapped">' + mapped + '</span>' : '')
      + '</div>';
  }
  container.innerHTML = html;
  container.scrollTop = container.scrollHeight;
  document.getElementById('frameCount').textContent = frames.length + ' frames';
}

function pad2(n) { return n < 10 ? '0' + n : String(n); }

function toggleFramePause() {
  _framePaused = !_framePaused;
  document.getElementById('framePauseBtn').textContent = _framePaused ? 'Resume' : 'Pause';
  if (!_framePaused) fetchFrames();
}

/* ──── Init ──── */
document.getElementById('loginForm').addEventListener('submit', submitLogin);
document.getElementById('setupForm').addEventListener('submit', submitSetup);

checkSession();
