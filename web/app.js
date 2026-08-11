(() => {
  'use strict';

  const API = '/api/v1';
  const state = {
    view: 'servers',
    theme: 'dark',
    status: null,
    config: null,
    servers: [],
    selectedServerId: null,
    selectedTab: 'tools',
    serverTools: [],
    serverResources: [],
    serverPrompts: [],
    globalTools: [],
    globalResources: [],
    globalPrompts: [],
    logs: [],
    serverQuery: '',
    toolQuery: '',
    loading: true,
  };

  const navItems = [
    ['overview', 'Overview', 'home'],
    ['servers', 'Servers', 'servers'],
    ['tools', 'Tools', 'tools'],
    ['resources', 'Resources', 'document'],
    ['prompts', 'Prompts', 'prompt'],
    ['activity', 'Activity', 'activity'],
    ['settings', 'Settings', 'settings'],
  ];

  const icons = {
    home: '<svg viewBox="0 0 24 24"><path d="m3 11 9-8 9 8"/><path d="M5 10v10h14V10M9 20v-6h6v6"/></svg>',
    servers: '<svg viewBox="0 0 24 24"><rect x="4" y="4" width="16" height="6" rx="2"/><rect x="4" y="14" width="16" height="6" rx="2"/><path d="M8 7h.01M8 17h.01M12 7h4M12 17h4"/></svg>',
    tools: '<svg viewBox="0 0 24 24"><path d="M14.7 6.3a4 4 0 0 0-5-5l2.1 2.1-2.4 2.4-2.1-2.1a4 4 0 0 0 5 5L20 16.4a2.5 2.5 0 0 1-3.6 3.6l-7.7-7.7"/><path d="m5 14-3 3 5 5 3-3"/></svg>',
    document: '<svg viewBox="0 0 24 24"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v5h5M9 13h6M9 17h6"/></svg>',
    prompt: '<svg viewBox="0 0 24 24"><path d="M4 5h16v12H8l-4 4z"/><path d="M8 9h8M8 13h5"/></svg>',
    activity: '<svg viewBox="0 0 24 24"><path d="M3 12h4l2-7 4 14 2-7h6"/></svg>',
    settings: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-1.6v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></svg>',
    search: '<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></svg>',
    refresh: '<svg viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg>',
    plus: '<svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>',
    moon: '<svg viewBox="0 0 24 24"><path d="M20 15.2A8 8 0 0 1 8.8 4 8.5 8.5 0 1 0 20 15.2Z"/></svg>',
    sun: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>',
    code: '<svg viewBox="0 0 24 24"><path d="m8 9-4 3 4 3M16 9l4 3-4 3M14 5l-4 14"/></svg>',
    close: '<svg viewBox="0 0 24 24"><path d="m6 6 12 12M18 6 6 18"/></svg>',
    chevron: '<svg viewBox="0 0 24 24"><path d="m9 6 6 6-6 6"/></svg>',
    trash: '<svg viewBox="0 0 24 24"><path d="M4 7h16M9 7V4h6v3M7 7l1 14h8l1-14M10 11v6M14 11v6"/></svg>',
  };

  function esc(value) {
    return String(value ?? '').replace(/[&<>'"]/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[ch]));
  }

  function initTheme() {
    const stored = localStorage.getItem('mcp-gateway-theme');
    state.theme = stored || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
    applyTheme();
  }

  function applyTheme() {
    document.documentElement.dataset.theme = state.theme;
    document.querySelector('#theme-icon').innerHTML = state.theme === 'dark' ? icons.sun : icons.moon;
    document.querySelector('#theme-label').textContent = state.theme === 'dark' ? 'Light mode' : 'Dark mode';
  }

  async function api(path, options = {}) {
    const response = await fetch(API + path, {
      headers: {'Content-Type': 'application/json', ...(options.headers || {})},
      ...options,
    });
    if (response.status === 204) return null;
    const contentType = response.headers.get('content-type') || '';
    const body = contentType.includes('application/json') ? await response.json() : await response.text();
    if (!response.ok) {
      const message = body?.error?.message || body?.message || body || `Request failed (${response.status})`;
      throw new Error(message);
    }
    return body;
  }

  async function bootstrap() {
    initTheme();
    renderNav();
    bindGlobal();
    try {
      const [status, serverData, configData] = await Promise.all([
        api('/status'), api('/servers'), api('/config'),
      ]);
      state.status = status;
      state.servers = serverData.servers || [];
      state.config = configData;
      state.selectedServerId = state.servers[0]?.id || null;
      state.loading = false;
      renderGatewayBadge();
      if (state.selectedServerId) await loadSelectedServerData();
      render();
    } catch (error) {
      state.loading = false;
      renderFatal(error);
    }
  }

  function renderNav() {
    const nav = document.querySelector('#primary-nav');
    nav.innerHTML = navItems.map(([id, label, icon]) => `
      <button class="nav-button ${state.view === id ? 'active' : ''}" data-view="${id}" type="button" title="${label}">
        ${icons[icon]}<span>${label}</span>
      </button>`).join('');
  }

  function bindGlobal() {
    document.querySelector('#primary-nav').addEventListener('click', event => {
      const button = event.target.closest('[data-view]');
      if (!button) return;
      navigate(button.dataset.view);
    });
    document.querySelector('#theme-toggle').addEventListener('click', () => {
      state.theme = state.theme === 'dark' ? 'light' : 'dark';
      localStorage.setItem('mcp-gateway-theme', state.theme);
      applyTheme();
    });
  }

  async function navigate(view) {
    state.view = view;
    renderNav();
    render();
    try {
      if (view === 'tools' && !state.globalTools.length) {
        const data = await api('/tools?limit=1000'); state.globalTools = data.tools || []; render();
      } else if (view === 'resources' && !state.globalResources.length) {
        const data = await api('/resources?limit=1000'); state.globalResources = data.resources || []; render();
      } else if (view === 'prompts' && !state.globalPrompts.length) {
        const data = await api('/prompts?limit=1000'); state.globalPrompts = data.prompts || []; render();
      } else if (view === 'activity') {
        await loadLogs(); render();
      } else if (view === 'overview' || view === 'settings') {
        state.status = await api('/status'); renderGatewayBadge(); render();
      }
    } catch (error) { toast(error.message, 'error'); }
  }

  function renderGatewayBadge() {
    const el = document.querySelector('#gateway-badge');
    if (!state.status) return;
    el.classList.remove('skeleton-block');
    const unhealthy = state.status.stats.unhealthyServers || 0;
    const cls = unhealthy ? 'degraded' : 'healthy';
    const label = unhealthy ? 'Gateway degraded' : 'Gateway online';
    el.innerHTML = `<strong class="status ${cls}"><span class="status-dot"></span>${label}</strong><small>v${esc(state.status.version)} · ${state.status.stats.serverCount} servers</small>`;
  }

  function render() {
    const content = document.querySelector('#content');
    if (state.loading) return;
    const renderers = {
      overview: renderOverview,
      servers: renderServers,
      tools: () => renderCatalog('Tools', 'All indexed tools across every MCP server.', state.globalTools, 'tool'),
      resources: () => renderCatalog('Resources', 'Application-controlled context exposed by connected servers.', state.globalResources, 'resource'),
      prompts: () => renderCatalog('Prompts', 'Reusable prompt templates exposed by connected servers.', state.globalPrompts, 'prompt'),
      activity: renderActivity,
      settings: renderSettings,
    };
    content.innerHTML = (renderers[state.view] || renderServers)();
    bindView();
  }

  function renderOverview() {
    const stats = state.status?.stats || {};
    const healthy = state.servers.filter(s => s.status === 'healthy').length;
    return `<section class="view standard-view"><div class="standard-view-inner">
      <header class="standard-header"><div><h1 class="page-title">Overview</h1><p class="page-subtitle">Gateway health and runtime footprint at a glance.</p></div><button class="button ghost" data-action="refresh-all">${icons.refresh}<span>Refresh</span></button></header>
      <div class="stats-strip">
        ${stat(healthy, 'Healthy servers')}${stat(stats.toolCount || 0, 'Indexed tools')}${stat(formatBytes(stats.heapAllocBytes || 0), 'Heap allocated')}${stat(stats.goroutines || 0, 'Goroutines')}${stat(formatDurationSince(stats.startedAt), 'Uptime')}
      </div>
      <div class="surface"><div class="surface-header"><h2>Server health</h2><span class="count-label">${state.servers.length} configured</span></div>${serverHealthTable(state.servers)}</div>
    </div></section>`;
  }

  function stat(value, label) { return `<div class="stat-item"><strong>${esc(value)}</strong><span>${esc(label)}</span></div>`; }

  function renderServers() {
    const query = state.serverQuery.trim().toLowerCase();
    const servers = state.servers.filter(server => !query || `${server.name} ${server.id} ${server.transport} ${server.status}`.toLowerCase().includes(query));
    const selected = state.servers.find(server => server.id === state.selectedServerId);
    return `<section class="view servers-view">
      <div class="server-browser">
        <header class="panel-header"><div class="header-row"><div><h1 class="page-title">Servers</h1><p class="page-subtitle">Manage and monitor MCP servers</p></div><div class="header-actions"><button class="button primary" data-action="add-server">${icons.plus}<span>Add server</span></button></div></div></header>
        <div class="search-row"><label class="search-box">${icons.search}<input id="server-search" value="${esc(state.serverQuery)}" placeholder="Search servers…" autocomplete="off"></label><button class="icon-button" data-action="refresh-all" title="Refresh">${icons.refresh}</button></div>
        <div class="table-wrap"><table class="data-table server-table"><thead><tr><th>Server</th><th>Status</th><th>Transport</th><th>Tools</th><th>Last sync</th></tr></thead><tbody>
          ${servers.length ? servers.map(serverRow).join('') : `<tr><td colspan="5"><div class="empty-state"><strong>No servers found</strong>${state.servers.length ? 'Try a different search.' : 'Add your first MCP server to begin discovery.'}</div></td></tr>`}
        </tbody></table></div>
        <footer class="panel-footer"><span>${servers.length} server${servers.length === 1 ? '' : 's'}</span><button class="link-button" data-action="refresh-all">${icons.refresh} Refresh</button></footer>
      </div>
      <div class="detail-pane">${selected ? renderServerDetail(selected) : renderNoServer()}</div>
    </section>`;
  }

  function serverRow(server) {
    return `<tr class="clickable ${server.id === state.selectedServerId ? 'selected' : ''}" data-server-id="${esc(server.id)}">
      <td><div class="entity-cell"><span class="entity-icon">${initials(server.name)}</span><span class="entity-text"><strong>${esc(server.name)}</strong><small>${esc(server.id)}</small></span></div></td>
      <td>${status(server.status)}</td><td><span class="transport">${esc(server.transport)}</span></td><td>${server.toolCount || 0}</td><td class="count-label">${timeAgo(server.lastDiscoveredAt || server.lastHealthCheck)}</td>
    </tr>`;
  }

  function renderServerDetail(server) {
    const tabs = ['overview', 'tools', 'resources', 'prompts', 'activity', 'settings'];
    return `<header class="detail-header">
      <div class="header-row"><div class="server-identity"><span class="entity-icon">${initials(server.name)}</span><div><h2>${esc(server.name)}</h2><div class="server-meta">${status(server.status)}<span class="meta-divider"></span><span class="transport">${esc(server.transport)}</span><span class="meta-divider"></span><span>${server.toolCount || 0} tools</span></div></div></div>
      <div class="header-actions"><button class="button ghost" data-action="discover" data-server-id="${esc(server.id)}">${icons.refresh}<span>Discover</span></button><button class="icon-button" data-action="delete-server" data-server-id="${esc(server.id)}" title="Remove server">${icons.trash}</button></div></div>
      <div class="tabs">${tabs.map(tab => `<button class="tab-button ${state.selectedTab === tab ? 'active' : ''}" data-tab="${tab}">${capitalize(tab)}</button>`).join('')}</div>
    </header><div class="detail-body">${renderSelectedTab(server)}</div>`;
  }

  function renderSelectedTab(server) {
    switch (state.selectedTab) {
      case 'overview': return renderServerOverview(server);
      case 'resources': return renderServerResources();
      case 'prompts': return renderServerPrompts();
      case 'activity': return renderServerActivity(server);
      case 'settings': return renderServerSettings(server);
      default: return renderServerTools(server);
    }
  }

  function renderServerTools(server) {
    const query = state.toolQuery.trim().toLowerCase();
    const tools = state.serverTools.filter(tool => !query || `${tool.name} ${tool.title || ''} ${tool.description || ''}`.toLowerCase().includes(query));
    return `<div class="detail-toolbar"><label class="search-box">${icons.search}<input id="tool-search" value="${esc(state.toolQuery)}" placeholder="Search tools…" autocomplete="off"></label><span class="count-label">${tools.length} tools</span></div>
      ${tools.length ? `<div class="table-wrap"><table class="data-table tool-table"><thead><tr><th>Tool</th><th>Description</th><th>Status</th><th>Schema</th></tr></thead><tbody>${tools.map(tool => `<tr><td><span class="tool-name">${esc(tool.name)}</span></td><td><span class="tool-description">${esc(tool.description || 'No description provided')}</span></td><td>${status(server.status === 'healthy' ? 'active' : server.status)}</td><td><button class="button small ghost schema-button" data-action="view-schema" data-server-id="${esc(tool.serverId)}" data-tool-name="${esc(tool.name)}">{ }</button></td></tr>`).join('')}</tbody></table></div>` : empty('No tools indexed', 'Run discovery to retrieve this server’s tools.')}
      <section class="section"><h3 class="section-title">Details</h3>${detailsList(server)}</section>`;
  }

  function renderServerOverview(server) {
    return `<section class="section"><h3 class="section-title">Connection</h3>${detailsList(server)}</section>
      <section class="section"><h3 class="section-title">Capabilities</h3><dl class="description-list"><dt>Tools</dt><dd>${server.toolCount || 0}</dd><dt>Resources</dt><dd>${server.resourceCount || 0}</dd><dt>Templates</dt><dd>${server.resourceTemplateCount || 0}</dd><dt>Prompts</dt><dd>${server.promptCount || 0}</dd></dl></section>
      <section class="section"><h3 class="section-title">Health</h3><dl class="description-list"><dt>State</dt><dd>${status(server.status)}</dd><dt>Message</dt><dd>${esc(server.statusMessage || '—')}</dd><dt>Last check</dt><dd>${formatDate(server.lastHealthCheck)}</dd></dl></section>`;
  }

  function renderServerResources() {
    return state.serverResources.length ? `<div class="surface"><div class="surface-header"><h2>Resources</h2><span class="count-label">${state.serverResources.length}</span></div><table class="data-table"><thead><tr><th>Resource</th><th>MIME type</th></tr></thead><tbody>${state.serverResources.map(item => `<tr><td><span class="tool-name">${esc(item.name || item.uri)}</span><span class="tool-description">${esc(item.description || item.uri)}</span></td><td class="mono">${esc(item.mimeType || '—')}</td></tr>`).join('')}</tbody></table></div>` : empty('No resources indexed', 'This server may not expose resources, or discovery has not run yet.');
  }

  function renderServerPrompts() {
    return state.serverPrompts.length ? `<div class="surface"><div class="surface-header"><h2>Prompts</h2><span class="count-label">${state.serverPrompts.length}</span></div><table class="data-table"><thead><tr><th>Name</th><th>Description</th></tr></thead><tbody>${state.serverPrompts.map(item => `<tr><td><span class="tool-name">${esc(item.name)}</span></td><td class="tool-description">${esc(item.description || 'No description provided')}</td></tr>`).join('')}</tbody></table></div>` : empty('No prompts indexed', 'This server may not expose prompt templates.');
  }

  function renderServerActivity(server) {
    const logs = state.logs.filter(entry => entry.attrs?.server === server.id);
    return logs.length ? renderLogList(logs) : empty('No recent activity', 'The bounded log buffer does not contain events for this server.');
  }

  function renderServerSettings(server) {
    return `<section class="section"><h3 class="section-title">Configuration summary</h3>${detailsList(server)}</section><section class="section"><h3 class="section-title">Management</h3><div class="header-actions"><button class="button" data-action="discover" data-server-id="${esc(server.id)}">${icons.refresh} Rediscover</button><button class="button danger" data-action="delete-server" data-server-id="${esc(server.id)}">${icons.trash} Remove server</button></div></section>`;
  }

  function detailsList(server) {
    return `<dl class="description-list"><dt>Server ID</dt><dd><span class="code-field">${esc(server.id)}</span></dd><dt>Transport</dt><dd>${esc(server.transport)}</dd><dt>Endpoint</dt><dd><span class="code-field">${esc(server.endpoint || '—')}</span></dd><dt>Protocol</dt><dd>${esc(server.protocolVersion || 'Not negotiated')}</dd><dt>Connected since</dt><dd>${formatDate(server.connectedSince)}</dd><dt>Last discovery</dt><dd>${formatDate(server.lastDiscoveredAt)}</dd></dl>`;
  }

  function renderNoServer() {
    return `<div class="empty-state"><strong>No server selected</strong>Select a server from the list or add a new one.</div>`;
  }

  function renderCatalog(title, subtitle, items, kind) {
    const rows = items.map(item => {
      if (kind === 'tool') return `<tr><td><span class="tool-name">${esc(item.exposedName)}</span></td><td>${esc(item.serverId)}</td><td class="tool-description">${esc(item.description || 'No description provided')}</td></tr>`;
      if (kind === 'resource') return `<tr><td><span class="tool-name">${esc(item.name || item.uri)}</span></td><td>${esc(item.serverId)}</td><td class="tool-description">${esc(item.description || item.uri)}</td></tr>`;
      return `<tr><td><span class="tool-name">${esc(item.name)}</span></td><td>${esc(item.serverId)}</td><td class="tool-description">${esc(item.description || 'No description provided')}</td></tr>`;
    }).join('');
    return `<section class="view standard-view"><div class="standard-view-inner"><header class="standard-header"><div><h1 class="page-title">${esc(title)}</h1><p class="page-subtitle">${esc(subtitle)}</p></div><button class="button ghost" data-action="reload-view">${icons.refresh} Refresh</button></header><div class="stats-strip">${stat(items.length, `Indexed ${title.toLowerCase()}`)}${stat(state.servers.length, 'Source servers')}</div><div class="surface"><table class="data-table"><thead><tr><th>Name</th><th>Server</th><th>Description</th></tr></thead><tbody>${rows || `<tr><td colspan="3">${empty(`No ${title.toLowerCase()} indexed`, 'Run server discovery to populate the catalog.')}</td></tr>`}</tbody></table></div></div></section>`;
  }

  function renderActivity() {
    return `<section class="view standard-view"><div class="standard-view-inner"><header class="standard-header"><div><h1 class="page-title">Activity</h1><p class="page-subtitle">Recent bounded gateway logs. Payloads and secrets are intentionally excluded.</p></div><button class="button ghost" data-action="reload-logs">${icons.refresh} Refresh</button></header><div class="stats-strip">${stat(state.logs.length, 'Buffered entries')}${stat(formatBytes(state.status?.stats?.logBufferBytes || 0), 'Buffer size')}</div><div class="surface">${state.logs.length ? renderLogList(state.logs) : empty('No activity yet', 'Gateway events will appear here as servers are discovered and used.')}</div></div></section>`;
  }

  function renderLogList(logs) {
    return `<div class="log-list">${logs.slice().reverse().map(entry => `<div class="log-row"><span class="log-time">${new Date(entry.time).toLocaleTimeString()}</span><span class="log-level ${esc(entry.level)}">${esc(entry.level)}</span><span>${esc(entry.message)}${entry.attrs?.server ? ` <span class="mono">server=${esc(entry.attrs.server)}</span>` : ''}</span></div>`).join('')}</div>`;
  }

  function renderSettings() {
    const cfg = state.config || {};
    const stats = state.status?.stats || {};
    return `<section class="view standard-view"><div class="standard-view-inner"><header class="standard-header"><div><h1 class="page-title">Settings</h1><p class="page-subtitle">Runtime configuration and bounded-memory controls.</p></div></header><div class="stats-strip">${stat(formatBytes(stats.heapAllocBytes || 0), 'Heap allocated')}${stat(formatBytes(stats.schemaCacheBytes || 0), 'Schema cache')}${stat(formatBytes(stats.logBufferBytes || 0), 'Log buffer')}${stat(stats.goroutines || 0, 'Goroutines')}</div><div class="settings-grid">
      <section class="settings-panel"><h2>Gateway</h2><dl class="description-list"><dt>Address</dt><dd><span class="code-field">${esc(cfg.gateway?.host || '127.0.0.1')}:${esc(cfg.gateway?.port || 4444)}</span></dd><dt>MCP endpoint</dt><dd><span class="code-field">${esc(cfg.gateway?.mcpPath || '/mcp')}</span></dd><dt>Admin UI</dt><dd><span class="code-field">${esc(cfg.gateway?.adminPath || '/admin')}</span></dd></dl></section>
      <section class="settings-panel"><h2>Storage</h2><dl class="description-list"><dt>Adapter</dt><dd>${esc(cfg.storage?.type || 'sqlite')}</dd><dt>Path</dt><dd><span class="code-field">${esc(cfg.storage?.path || '—')}</span></dd><dt>External DB</dt><dd>${cfg.storage?.type === 'postgres' ? 'Configured' : 'Optional via PostgreSQL adapter'}</dd></dl></section>
      <section class="settings-panel"><h2>Memory budgets</h2><dl class="description-list"><dt>Soft limit</dt><dd>${esc(cfg.memory?.softLimitMB || 128)} MB</dd><dt>Schema cache</dt><dd>${esc(cfg.memory?.schemaCacheMB || 16)} MB</dd><dt>Log buffer</dt><dd>${esc(cfg.memory?.logBufferMB || 2)} MB</dd><dt>Concurrency</dt><dd>${esc(cfg.memory?.maxConcurrent || 128)} global</dd></dl></section>
      <section class="settings-panel"><h2>Appearance</h2><p class="page-subtitle">The selected theme is saved only in this browser.</p><button class="button" data-action="toggle-theme">${state.theme === 'dark' ? icons.sun : icons.moon} Switch to ${state.theme === 'dark' ? 'light' : 'dark'} mode</button></section>
    </div></div></section>`;
  }

  function serverHealthTable(servers) {
    return `<table class="data-table"><thead><tr><th>Server</th><th>Health</th><th>Transport</th><th>Tools</th><th>Last check</th></tr></thead><tbody>${servers.map(server => `<tr class="clickable" data-open-server="${esc(server.id)}"><td><div class="entity-cell"><span class="entity-icon">${initials(server.name)}</span><span class="entity-text"><strong>${esc(server.name)}</strong><small>${esc(server.id)}</small></span></div></td><td>${status(server.status)}</td><td class="mono">${esc(server.transport)}</td><td>${server.toolCount || 0}</td><td class="count-label">${timeAgo(server.lastHealthCheck)}</td></tr>`).join('')}</tbody></table>`;
  }

  function bindView() {
    document.querySelectorAll('[data-server-id]').forEach(row => {
      if (row.tagName === 'TR') row.addEventListener('click', () => selectServer(row.dataset.serverId));
    });
    document.querySelectorAll('[data-open-server]').forEach(row => row.addEventListener('click', () => { state.selectedServerId = row.dataset.openServer; state.view = 'servers'; renderNav(); loadSelectedServerData().then(render); }));
    document.querySelectorAll('[data-tab]').forEach(button => button.addEventListener('click', async () => { state.selectedTab = button.dataset.tab; if (state.selectedTab === 'activity' && !state.logs.length) await loadLogs(); render(); }));
    const serverSearch = document.querySelector('#server-search');
    if (serverSearch) serverSearch.addEventListener('input', event => { state.serverQuery = event.target.value; render(); requestAnimationFrame(() => { const input = document.querySelector('#server-search'); input?.focus(); input?.setSelectionRange(state.serverQuery.length, state.serverQuery.length); }); });
    const toolSearch = document.querySelector('#tool-search');
    if (toolSearch) toolSearch.addEventListener('input', event => { state.toolQuery = event.target.value; render(); requestAnimationFrame(() => { const input = document.querySelector('#tool-search'); input?.focus(); input?.setSelectionRange(state.toolQuery.length, state.toolQuery.length); }); });
    document.querySelectorAll('[data-action]').forEach(button => button.addEventListener('click', handleAction));
  }

  async function handleAction(event) {
    const button = event.currentTarget;
    const action = button.dataset.action;
    try {
      if (action === 'add-server') return openAddServer();
      if (action === 'refresh-all') return refreshAll();
      if (action === 'discover') return discoverServer(button.dataset.serverId);
      if (action === 'delete-server') return deleteServer(button.dataset.serverId);
      if (action === 'view-schema') return viewSchema(button.dataset.serverId, button.dataset.toolName);
      if (action === 'reload-logs') { await loadLogs(); render(); return; }
      if (action === 'toggle-theme') { document.querySelector('#theme-toggle').click(); render(); return; }
      if (action === 'reload-view') { await reloadCatalog(); render(); return; }
    } catch (error) { toast(error.message, 'error'); }
  }

  async function selectServer(id) {
    if (id === state.selectedServerId) return;
    state.selectedServerId = id;
    state.toolQuery = '';
    render();
    await loadSelectedServerData();
    render();
  }

  async function loadSelectedServerData() {
    const id = state.selectedServerId;
    if (!id) return;
    try {
      const [tools, resources, prompts] = await Promise.all([
        api(`/tools?server=${encodeURIComponent(id)}&limit=1000`),
        api(`/resources?server=${encodeURIComponent(id)}&limit=1000`),
        api(`/prompts?server=${encodeURIComponent(id)}&limit=1000`),
      ]);
      if (state.selectedServerId !== id) return;
      state.serverTools = tools.tools || [];
      state.serverResources = resources.resources || [];
      state.serverPrompts = prompts.prompts || [];
    } catch (error) { toast(error.message, 'error'); }
  }

  async function refreshAll() {
    const [statusData, serverData, configData] = await Promise.all([api('/status'), api('/servers'), api('/config')]);
    state.status = statusData; state.servers = serverData.servers || []; state.config = configData;
    if (!state.servers.some(server => server.id === state.selectedServerId)) state.selectedServerId = state.servers[0]?.id || null;
    renderGatewayBadge();
    await loadSelectedServerData();
    render();
    toast('Gateway state refreshed', 'success');
  }

  async function discoverServer(id) {
    toast(`Discovering ${id}…`);
    await api(`/servers/${encodeURIComponent(id)}/discover`, {method: 'POST', body: '{}'});
    await refreshAll();
    toast(`${id} discovery completed`, 'success');
  }

  async function deleteServer(id) {
    if (!confirm(`Remove “${id}” and its cached capabilities?`)) return;
    await api(`/servers/${encodeURIComponent(id)}`, {method: 'DELETE'});
    state.selectedServerId = null;
    await refreshAll();
    toast(`${id} removed`, 'success');
  }

  async function viewSchema(serverId, toolName) {
    const tool = await api(`/tools/${encodeURIComponent(serverId)}/${encodeURIComponent(toolName)}`);
    openModal(`<div class="modal-header"><h2>${esc(tool.exposedName)}</h2><button class="icon-button" data-close-modal>${icons.close}</button></div><div class="modal-body"><pre class="schema-pre">${esc(JSON.stringify({inputSchema: tool.inputSchema, outputSchema: tool.outputSchema, annotations: tool.annotations}, null, 2))}</pre></div>`);
  }

  function openAddServer() {
    openModal(`<form id="add-server-form">
      <div class="modal-header"><div><h2>Add MCP server</h2><p class="page-subtitle">The configuration is validated and saved atomically.</p></div><button class="icon-button" type="button" data-close-modal>${icons.close}</button></div>
      <div class="modal-body"><div id="modal-error"></div><div class="form-grid">
        <div class="form-field"><label for="server-name">Server ID</label><input class="input" id="server-name" name="name" required pattern="[a-z0-9_-]+" placeholder="github"><span class="help">Lowercase letters, numbers, hyphens, and underscores.</span></div>
        <div class="form-field"><label for="server-type">Transport</label><select class="select" id="server-type" name="type"><option value="stdio">Stdio</option><option value="streamable-http">Streamable HTTP</option><option value="sse">Legacy SSE</option></select></div>
        <div class="form-field full" data-stdio-field><label for="server-command">Command</label><input class="input" id="server-command" name="command" placeholder="npx"></div>
        <div class="form-field full" data-stdio-field><label for="server-args">Arguments (JSON array)</label><textarea class="textarea" id="server-args" name="args" placeholder='["-y", "@modelcontextprotocol/server-filesystem", "."]'></textarea></div>
        <div class="form-field full" data-http-field hidden><label for="server-url">MCP endpoint</label><input class="input" id="server-url" name="url" placeholder="https://example.com/mcp"></div>
        <div class="form-field" data-http-field hidden><label for="auth-mode">Bearer authentication</label><select class="select" id="auth-mode" name="authMode"><option value="none">None</option><option value="env">Environment variable</option><option value="inline">Inline token</option></select></div>
        <div class="form-field" data-http-field hidden><label for="auth-value">Token or environment variable</label><input class="input" id="auth-value" name="authValue" placeholder="GITHUB_TOKEN"></div>
        <div class="form-field"><label for="lifecycle-mode">Lifecycle</label><select class="select" id="lifecycle-mode" name="lifecycle"><option value="lazy">Lazy (recommended)</option><option value="persistent">Persistent</option></select></div>
        <div class="form-field"><label for="idle-timeout">Idle timeout</label><input class="input" id="idle-timeout" name="idleTimeout" value="5m"></div>
      </div></div>
      <div class="modal-footer"><button class="button ghost" type="button" data-close-modal>Cancel</button><button class="button primary" type="submit">Add server</button></div>
    </form>`);
    const type = document.querySelector('#server-type');
    const updateFields = () => {
      const isStdio = type.value === 'stdio';
      document.querySelectorAll('[data-stdio-field]').forEach(el => el.hidden = !isStdio);
      document.querySelectorAll('[data-http-field]').forEach(el => el.hidden = isStdio);
    };
    type.addEventListener('change', updateFields); updateFields();
    document.querySelector('#add-server-form').addEventListener('submit', submitAddServer);
  }

  async function submitAddServer(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const type = form.get('type');
    const server = {
      type,
      lifecycle: {mode: form.get('lifecycle'), idleTimeout: form.get('idleTimeout') || '5m'},
    };
    try {
      if (type === 'stdio') {
        server.command = String(form.get('command') || '').trim();
        server.args = String(form.get('args') || '').trim() ? JSON.parse(form.get('args')) : [];
      } else {
        server.url = String(form.get('url') || '').trim();
        const authMode = form.get('authMode');
        const authValue = String(form.get('authValue') || '').trim();
        if (authMode !== 'none' && authValue) {
          server.headers = {Authorization: authMode === 'env' ? 'Bearer ${' + authValue + '}' : 'Bearer ' + authValue};
        }
      }
      const name = String(form.get('name') || '').trim();
      await api('/servers', {method: 'POST', body: JSON.stringify({name, server})});
      closeModal();
      state.selectedServerId = name;
      await refreshAll();
      try { await discoverServer(name); } catch (_) { toast(`${name} was added, but discovery did not complete. Check its status.`, 'error'); }
    } catch (error) {
      const target = document.querySelector('#modal-error');
      if (target) target.innerHTML = `<div class="error-box">${esc(error.message)}</div>`;
    }
  }

  function openModal(content) {
    const root = document.querySelector('#modal-root');
    root.innerHTML = `<div class="modal-backdrop"><div class="modal">${content}</div></div>`;
    root.querySelectorAll('[data-close-modal]').forEach(button => button.addEventListener('click', closeModal));
    root.querySelector('.modal-backdrop').addEventListener('click', event => { if (event.target.classList.contains('modal-backdrop')) closeModal(); });
  }

  function closeModal() { document.querySelector('#modal-root').innerHTML = ''; }

  async function loadLogs() { const data = await api('/logs?limit=500'); state.logs = data.logs || []; }

  async function reloadCatalog() {
    if (state.view === 'tools') { const data = await api('/tools?limit=1000'); state.globalTools = data.tools || []; }
    if (state.view === 'resources') { const data = await api('/resources?limit=1000'); state.globalResources = data.resources || []; }
    if (state.view === 'prompts') { const data = await api('/prompts?limit=1000'); state.globalPrompts = data.prompts || []; }
  }

  function status(value) {
    value = value || 'unknown';
    return `<span class="status ${esc(value)}"><span class="status-dot"></span>${esc(value)}</span>`;
  }

  function initials(name) {
    const parts = String(name || '?').split(/[-_\s]+/).filter(Boolean);
    return esc(parts.slice(0, 2).map(part => part[0]).join('').toUpperCase() || '?');
  }

  function timeAgo(value) {
    if (!value || value.startsWith?.('0001-')) return '—';
    const time = new Date(value).getTime();
    if (!Number.isFinite(time)) return '—';
    const seconds = Math.max(0, Math.round((Date.now() - time) / 1000));
    if (seconds < 60) return `${seconds}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }

  function formatDate(value) {
    if (!value || value.startsWith?.('0001-')) return '—';
    const date = new Date(value);
    return Number.isFinite(date.getTime()) ? date.toLocaleString() : '—';
  }

  function formatBytes(bytes) {
    const value = Number(bytes) || 0;
    const units = ['B', 'KB', 'MB', 'GB'];
    let size = value, index = 0;
    while (size >= 1024 && index < units.length - 1) { size /= 1024; index++; }
    return `${size >= 10 || index === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[index]}`;
  }

  function formatDurationSince(value) {
    if (!value) return '—';
    const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
    return `${Math.floor(seconds / 86400)}d`;
  }

  function capitalize(value) { return value.charAt(0).toUpperCase() + value.slice(1); }
  function empty(title, description) { return `<div class="empty-state"><strong>${esc(title)}</strong>${esc(description)}</div>`; }

  function toast(message, type = '') {
    const region = document.querySelector('#toast-region');
    const item = document.createElement('div');
    item.className = `toast ${type}`;
    item.textContent = message;
    region.appendChild(item);
    setTimeout(() => item.remove(), 4200);
  }

  function renderFatal(error) {
    document.querySelector('#content').innerHTML = `<div class="initial-loader"><div><h1 class="page-title">Gateway unavailable</h1><p class="page-subtitle">${esc(error.message)}</p><p><button id="fatal-retry" class="button">Retry</button></p></div></div>`;
    document.querySelector('#fatal-retry')?.addEventListener('click', () => location.reload());
  }

  bootstrap();
})();
