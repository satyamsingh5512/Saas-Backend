(() => {
  'use strict';

  const TOKEN_KEY = 'tenancy.session.token';
  const TENANT_KEY = 'tenancy.session.tenant';
  const state = { token: sessionStorage.getItem(TOKEN_KEY), me: null, users: [], tenant: readJSON(TENANT_KEY), adminOnly: false };
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

  function readJSON(key) { try { return JSON.parse(sessionStorage.getItem(key) || 'null'); } catch { return null; } }
  function escapeHTML(value) { const node = document.createElement('span'); node.textContent = String(value || ''); return node.innerHTML; }
  function initials(value) { return (value || '?').split(/[.@ _-]/).filter(Boolean).slice(0, 2).map(part => part[0]).join('').toUpperCase() || '?'; }
  function shortID(value) { return value ? `${value.slice(0, 8)}…${value.slice(-4)}` : '—'; }
  function formatTime(value) { if (!value) return 'Just now'; const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000)); if (seconds < 60) return 'Just now'; if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`; if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`; return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(new Date(value)); }

  async function request(path, options = {}) {
    const headers = { Accept: 'application/json', ...(options.headers || {}) };
    if (state.token) headers.Authorization = `Bearer ${state.token}`;
    if (options.body) headers['Content-Type'] = 'application/json';
    let response;
    try { response = await fetch(path, { ...options, headers }); } catch { throw new Error('Network connection failed. Check that the server is reachable.'); }
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      if (response.status === 401 && state.token) logout('Your session has expired. Please sign in again.');
      throw new Error(body.error || `Request failed (${response.status})`);
    }
    return body;
  }

  function notify(message, tone = 'default') {
    const toast = document.createElement('div'); toast.className = `toast ${tone}`; toast.innerHTML = `<i></i><span>${escapeHTML(message)}</span>`;
    $('#toast-region').append(toast); window.setTimeout(() => toast.remove(), 3600);
  }

  function setAuthMessage(message, success = false) {
    const element = $('#auth-message');
    if (!message) { element.hidden = true; element.textContent = ''; return; }
    element.hidden = false; element.textContent = message; element.classList.toggle('success', success);
  }

  function setSubmitting(form, submitting) {
    const button = $('button[type="submit"]', form); button.disabled = submitting;
    if (!button.dataset.label) button.dataset.label = button.querySelector('span').textContent;
    button.querySelector('span').textContent = submitting ? 'Please wait…' : button.dataset.label;
  }

  function switchAuth(mode) {
    const isLogin = mode === 'login';
    $('#login-form').hidden = !isLogin; $('#register-form').hidden = isLogin;
    $$('.auth-tab').forEach(tab => { const active = tab.dataset.mode === mode; tab.classList.toggle('active', active); tab.setAttribute('aria-selected', String(active)); });
    $('#auth-title').textContent = isLogin ? 'Welcome back.' : 'Create your workspace.';
    $('#auth-subtitle').textContent = isLogin ? 'Enter your credentials to continue to your workspace.' : 'Start with a secure home for your organization.';
    $('#form-step').textContent = isLogin ? '01 / 02' : '02 / 02'; setAuthMessage('');
    window.setTimeout(() => $(isLogin ? '#login-email' : '#tenant-name').focus(), 0);
  }

  async function handleLogin(event) {
    event.preventDefault(); const form = event.currentTarget; const data = Object.fromEntries(new FormData(form));
    if (!data.email || !data.password) return setAuthMessage('Enter your email and password to continue.');
    setAuthMessage(''); setSubmitting(form, true);
    try { const response = await request('/api/v1/auth/login', { method: 'POST', body: JSON.stringify(data) }); beginSession(response.token); }
    catch (error) { if (!state.token) setAuthMessage(error.message); }
    finally { setSubmitting(form, false); }
  }

  async function handleRegister(event) {
    event.preventDefault(); const form = event.currentTarget; const data = Object.fromEntries(new FormData(form));
    if (data.password.length < 8) return setAuthMessage('Choose a password with at least 8 characters.');
    if (!/^[a-z0-9-]+$/.test(data.tenant_slug)) return setAuthMessage('Workspace URL may only use lowercase letters, numbers, and hyphens.');
    setAuthMessage(''); setSubmitting(form, true);
    try {
      const response = await request('/api/v1/auth/register', { method: 'POST', body: JSON.stringify(data) });
      state.tenant = response.tenant || { name: data.tenant_name, slug: data.tenant_slug }; sessionStorage.setItem(TENANT_KEY, JSON.stringify(state.tenant)); beginSession(response.token);
    } catch (error) { if (!state.token) setAuthMessage(error.message); }
    finally { setSubmitting(form, false); }
  }

  function beginSession(token) { state.token = token; sessionStorage.setItem(TOKEN_KEY, token); showApp(); hydrate(); }
  function logout(message = '') { state.token = null; state.me = null; state.users = []; sessionStorage.removeItem(TOKEN_KEY); $('#app-view').hidden = true; $('#auth-view').hidden = false; document.body.classList.remove('nav-open'); switchAuth('login'); if (message) setAuthMessage(message); }
  function showApp() { $('#auth-view').hidden = true; $('#app-view').hidden = false; }

  async function hydrate() {
    setLoading(true);
    try {
      const [meData, usersData, health] = await Promise.all([request('/api/v1/me'), request('/api/v1/users'), fetch('/health').then(response => response.ok ? response.json() : Promise.reject())]);
      state.me = meData; state.users = Array.isArray(usersData.users) ? usersData.users : [];
      renderDashboard(); $('#health-status').textContent = health.status === 'ok' ? 'Healthy' : 'Review'; $('#sync-time').textContent = 'Synced just now';
    } catch (error) {
      if (state.token) { notify(error.message); renderDashboard(); $('#health-status').textContent = 'Unavailable'; }
    } finally { setLoading(false); }
  }

  function setLoading(isLoading) { $('#refresh-button').classList.toggle('spinning', isLoading); $('#refresh-button').disabled = isLoading; if (isLoading) $('#users-list').innerHTML = '<div class="loading-line"><i></i><i></i><i></i></div>'; }

  function currentUser() { return state.users.find(user => user.id === state.me?.user_id) || state.users[0]; }
  function workspaceLabel() { return state.tenant?.name || (currentUser()?.email ? `${currentUser().email.split('@')[0]}'s workspace` : 'Your workspace'); }

  function renderDashboard() {
    const user = currentUser(); const label = workspaceLabel(); const email = user?.email || 'Workspace account'; const role = state.me?.role || user?.role || 'member';
    $('#workspace-name').textContent = label; $('#workspace-initial').textContent = initials(label).charAt(0); $('#greeting-name').textContent = email.split('@')[0] || 'there';
    $('#profile-email').textContent = email; $('#profile-role').textContent = role; $('#profile-avatar').textContent = initials(email); $('#top-avatar').textContent = initials(email);
    $('#member-count').textContent = state.users.length || '0'; $('#nav-user-count').textContent = state.users.length; $('#member-summary').textContent = state.users.length === 1 ? '1 verified member' : `${state.users.length} verified members`;
    $('#role-value').textContent = role; $('#tenant-id-value').textContent = shortID(state.me?.tenant_id); $('#activity-time').textContent = 'Session active now';
    renderUsers();
  }

  function renderUsers() {
    const query = $('#user-search').value.trim().toLowerCase();
    let users = state.users.filter(user => user.email.toLowerCase().includes(query) || user.role.toLowerCase().includes(query));
    if (state.adminOnly) users = users.filter(user => user.role === 'admin');
    const list = $('#users-list');
    if (!users.length) { list.innerHTML = `<div class="empty-state">${state.users.length ? 'No people match this filter.' : 'No people have been added to this workspace yet.'}</div>`; return; }
    list.innerHTML = users.map(user => `<div class="user-row"><span class="user-avatar">${escapeHTML(initials(user.email))}</span><div class="user-info"><strong>${escapeHTML(user.email)}</strong><span>Joined ${escapeHTML(formatTime(user.created_at))}</span></div>${user.id === state.me?.user_id ? '<span class="you-badge">You</span>' : ''}<span class="role-badge">${escapeHTML(user.role)}</span></div>`).join('');
  }

  function activateSection(section) {
    const names = { overview: 'Overview', people: 'People', security: 'Security', activity: 'Activity', settings: 'Settings' }; $('#breadcrumb-current').textContent = names[section] || 'Overview';
    $$('.nav-item[data-section]').forEach(item => item.classList.toggle('active', item.dataset.section === section));
    if (section === 'people') { document.querySelector('#people').scrollIntoView({ behavior: 'smooth', block: 'start' }); window.setTimeout(() => $('#user-search').focus(), 350); }
    if (section === 'security') document.querySelector('#security').scrollIntoView({ behavior: 'smooth', block: 'start' });
    if (section === 'activity') document.querySelector('#activity').scrollIntoView({ behavior: 'smooth', block: 'start' });
    if (section === 'settings') notify('Workspace settings are not exposed by the current API yet.');
  }

  function setupEvents() {
    $('#year').textContent = new Date().getFullYear();
    $$('.auth-tab').forEach(tab => tab.addEventListener('click', () => switchAuth(tab.dataset.mode)));
    $$('.password-toggle').forEach(button => button.addEventListener('click', () => { const input = $(`#${button.dataset.passwordToggle}`); const visible = input.type === 'text'; input.type = visible ? 'password' : 'text'; button.textContent = visible ? 'Show' : 'Hide'; button.setAttribute('aria-label', visible ? 'Show password' : 'Hide password'); }));
    $('#tenant-name').addEventListener('input', event => { const slug = $('#tenant-slug'); if (!slug.dataset.changed) slug.value = event.target.value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, ''); });
    $('#tenant-slug').addEventListener('input', event => { event.target.dataset.changed = 'true'; event.target.value = event.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''); });
    $('#login-form').addEventListener('submit', handleLogin); $('#register-form').addEventListener('submit', handleRegister);
    $('#logout-button').addEventListener('click', () => logout('You have been signed out.'));
    $('#refresh-button').addEventListener('click', hydrate); $('#user-search').addEventListener('input', renderUsers);
    $('#admin-filter').addEventListener('click', event => { state.adminOnly = !state.adminOnly; event.currentTarget.classList.toggle('active', state.adminOnly); renderUsers(); });
    $('#show-all-users').addEventListener('click', () => { state.adminOnly = false; $('#admin-filter').classList.remove('active'); $('#user-search').value = ''; renderUsers(); $('#people').scrollIntoView({ behavior: 'smooth', block: 'start' }); });
    $('#copy-tenant-id').addEventListener('click', async () => { const id = state.me?.tenant_id; if (!id) return; try { await navigator.clipboard.writeText(id); notify('Workspace ID copied.', 'success'); } catch { notify('Unable to access the clipboard.'); } });
    $('#invite-button').addEventListener('click', () => $('#invite-modal').showModal()); $$('[data-close-modal]').forEach(button => button.addEventListener('click', () => $('#invite-modal').close()));
    $('#menu-button').addEventListener('click', () => document.body.classList.add('nav-open')); $('#close-sidebar').addEventListener('click', () => document.body.classList.remove('nav-open')); $('#sidebar-scrim').addEventListener('click', () => document.body.classList.remove('nav-open'));
    $$('.nav-item[data-section]').forEach(item => item.addEventListener('click', event => { event.preventDefault(); activateSection(item.dataset.section); document.body.classList.remove('nav-open'); }));
    $('#focus-search').addEventListener('click', () => activateSection('people'));
    window.addEventListener('keydown', event => { if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); activateSection('people'); } });
  }

  setupEvents();
  if (state.token) { showApp(); hydrate(); }
})();
