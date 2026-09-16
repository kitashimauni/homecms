// ui.js - 画面描画ロジック
import * as API from './api.js';

function normalizePreviewEngine(contentArea, engine = contentArea.dataset.previewEngine) {
    const requested = engine || (contentArea.classList.contains('local-preview-enabled') ? 'local' : 'markdown');
    return requested === 'local' && contentArea.classList.contains('local-preview-enabled') ? 'local' : 'markdown';
}

function updatePreviewEngineControls(contentArea) {
    const engine = normalizePreviewEngine(contentArea);
    document.querySelectorAll('[data-preview-engine]').forEach(button => {
        const buttonEngine = button.dataset?.previewEngine;
        button.classList.toggle('active', buttonEngine === engine);
        if (typeof button.setAttribute === 'function') button.setAttribute('aria-pressed', String(buttonEngine === engine));
        if (buttonEngine === 'local') button.disabled = !contentArea.classList.contains('local-preview-enabled');
    });
}

function renderView(contentArea) {
    const viewName = contentArea.dataset.viewMode || 'edit';
    const engine = normalizePreviewEngine(contentArea);
    const editView = document.getElementById('edit-view');
    const previewView = document.getElementById('preview-view');
    const localPreviewView = document.getElementById('local-preview-view');

    contentArea.dataset.previewEngine = engine;
    contentArea.classList.toggle('split-mode', viewName === 'split');
    contentArea.classList.toggle('local-preview-view-mode', viewName === 'preview' && engine === 'local');
    contentArea.classList.toggle('preview-engine-local', engine === 'local');
    contentArea.classList.toggle('preview-engine-markdown', engine === 'markdown');
    editView.style.display = viewName === 'edit' || viewName === 'split' ? 'flex' : 'none';
    previewView.style.display = viewName !== 'edit' && engine === 'markdown' ? 'block' : 'none';
    localPreviewView.style.display = viewName !== 'edit' && engine === 'local' ? 'flex' : 'none';

    document.querySelectorAll('.view-toggle').forEach(button => {
        const active = button.id === `btn-view-${viewName === 'split' ? 'split' : viewName === 'preview' ? 'preview' : 'edit'}`;
        button.classList.toggle('active', active);
    });
    updatePreviewEngineControls(contentArea);

}

export function switchView(viewName) {
    const contentArea = document.getElementById('content-area');
    if (viewName === 'markdown') {
        setPreviewEngine('markdown');
        return;
    }
    if (!['edit', 'preview', 'split'].includes(viewName)) return;
    contentArea.dataset.viewMode = viewName;
    renderView(contentArea);
}

export function setPreviewEngine(engine) {
    const contentArea = document.getElementById('content-area');
    contentArea.dataset.previewEngine = engine;
    renderView(contentArea);
}

export function getPreviewEngine() {
    const contentArea = document.getElementById('content-area');
    return normalizePreviewEngine(contentArea);
}

export function toggleSplitView() {
    const contentArea = document.getElementById('content-area');
    switchView(contentArea.dataset.viewMode === 'split' ? 'edit' : 'split');
}

export function toggleSidebar() {
    document.querySelector('aside').classList.toggle('sidebar-open');
    const backdrop = document.getElementById('sidebar-backdrop');
    if (backdrop) {
        if (document.querySelector('aside').classList.contains('sidebar-open')) {
            backdrop.style.display = 'block';
        } else {
            backdrop.style.display = 'none';
        }
    }
}

export function renderSiteSelector(registry, selectedSiteID, onChange, disabled = false) {
    const container = document.getElementById('site-selector-container');
    if (!container) return;

    const sites = registry?.sites || [];
    container.innerHTML = '';
    if (sites.length <= 1) {
        container.classList.add('hidden');
        return;
    }
    container.classList.remove('hidden');

    const label = document.createElement('label');
    label.setAttribute('for', 'site-selector');
    label.textContent = 'Site';

    const select = document.createElement('select');
    select.id = 'site-selector';
    select.disabled = disabled;
    sites.forEach(site => {
        const option = document.createElement('option');
        option.value = site.id;
        option.textContent = site.name || site.id;
        if (site.id === selectedSiteID) option.selected = true;
        select.appendChild(option);
    });
    select.onchange = () => onChange(select.value);

    container.appendChild(label);
    container.appendChild(select);
}

export function renderConfigWarnings(config) {
    const panel = document.getElementById('config-warning-panel');
    if (!panel) return;

    const warnings = Array.isArray(config?._cms?.warnings) ? config._cms.warnings : [];
    panel.innerHTML = '';
    if (warnings.length === 0) {
        panel.classList.add('hidden');
        return;
    }

    panel.classList.remove('hidden');
    const details = document.createElement('details');
    details.className = 'config-warning-details';
    details.open = warnings.some(warning => warning.severity === 'error');

    const summary = document.createElement('summary');
    summary.className = 'config-warning-title';
    const errorCount = warnings.filter(warning => warning.severity === 'error').length;
    summary.textContent = errorCount > 0
        ? `Config warnings (${warnings.length}, ${errorCount} error${errorCount === 1 ? '' : 's'})`
        : `Config warnings (${warnings.length})`;
    details.appendChild(summary);

    warnings.forEach(warning => {
        const item = document.createElement('div');
        const severity = warning.severity === 'error' ? 'error' : 'warning';
        item.className = `config-warning-item ${severity}`;

        const message = document.createElement('div');
        message.className = 'config-warning-message';
        message.textContent = warning.message || warning.code || 'Configuration warning';
        item.appendChild(message);

        if (warning.path || warning.code) {
            const meta = document.createElement('div');
            meta.className = 'config-warning-meta';
            meta.textContent = [warning.path, warning.code].filter(Boolean).join(' · ');
            item.appendChild(meta);
        }

        details.appendChild(item);
    });

    panel.appendChild(details);
}

export async function showLoadingEditor() {
    const fmContainer = document.getElementById('fm-container');
    const editor = document.getElementById('editor');

    // switchView('edit'); // Removed to preserve current view
    fmContainer.style.display = 'none';
    editor.value = "Loading...";
    editor.disabled = true;

    // 描画待ち
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
}

export function updateEditorContent(data, path, config) {
    const fmContainer = document.getElementById('fm-container');
    const editor = document.getElementById('editor');
    editor.disabled = false;

    if (data.frontmatter) {
        renderFrontMatterForm(data.frontmatter, path, config, fmContainer);
        fmContainer.style.display = 'block';
        editor.value = data.body || "";
    } else {
        fmContainer.style.display = 'none';
        fmContainer.innerHTML = '';
        editor.value = data.content || "";
    }
    editor.placeholder = "Write content here...";
}

export function showEditorError(error) {
    const editor = document.getElementById('editor');
    editor.value = "Error loading file: " + error;
    editor.disabled = false;
}

export function renderFileList(files, config) {
    const list = document.getElementById('file-list');
    list.innerHTML = "";

    const grouped = {};
    const others = [];

    if (config && config.collections) {
        config.collections.forEach(col => {
            grouped[col.name] = { label: col.label, files: [] };
        });
    }

    files.forEach(f => {
        let matched = false;
        if (config && config.collections) {
            for (const col of config.collections) {
                if (pathMatchesCollection(f.path, col, config)) {
                    grouped[col.name].files.push(f);
                    matched = true;
                    break;
                }
            }
        }
        if (!matched) {
            others.push(f);
        }
    });

    if (config && config.collections) {
        config.collections.forEach(col => {
            const group = grouped[col.name];
            if (group.files.length > 0) {
                renderCollectionGroup(list, group.label, group.files);
            }
        });
    }

    if (others.length > 0) {
        renderCollectionGroup(list, "Others", others);
    }
}

function renderCollectionGroup(container, label, files) {
    const details = document.createElement('details');
    details.open = true;
    details.style.marginBottom = '10px';

    const summary = document.createElement('summary');
    summary.textContent = label;
    summary.style.cursor = 'pointer';
    summary.style.padding = '10px';
    summary.style.background = '#333';
    summary.style.color = '#fff';
    summary.style.fontWeight = 'bold';
    summary.style.borderBottom = '1px solid #444';

    details.appendChild(summary);

    files.forEach(f => {
        const div = document.createElement('div');
        div.className = 'file-item';
        div.style.paddingLeft = '20px';

        const titleDiv = document.createElement('div');
        titleDiv.style.fontWeight = 'bold';

        let titleText = f.title || f.path;
        if (f.is_dirty) {
            titleText = "✎ " + titleText;
            titleDiv.style.color = "#e2c08d";
        }
        titleDiv.textContent = titleText;

        const pathDiv = document.createElement('div');
        pathDiv.style.fontSize = '12px';
        pathDiv.style.color = '#888';
        pathDiv.textContent = f.path;

        div.appendChild(titleDiv);
        div.appendChild(pathDiv);

        // グローバル関数 loadFile を呼び出す
        div.onclick = () => window.loadFile(f.path);
        details.appendChild(div);
    });

    container.appendChild(details);
}

function normalizePath(path) {
    return (path || '').replace(/\\/g, '/').replace(/^\/+/, '').replace(/\/+$/, '');
}

function getConfiguredContentDir(config) {
    return normalizePath(config?._cms?.content_dir || config?.content_dir || 'content');
}

function collectionFolderForArticlePaths(collection, config) {
    const folder = normalizePath(collection.folder);
    const contentDir = getConfiguredContentDir(config);
    if (contentDir && folder === contentDir) return '';
    if (contentDir && folder.startsWith(contentDir + '/')) {
        return folder.slice(contentDir.length + 1);
    }
    // Backward compatibility for configs loaded before server-side metadata
    // existed, and for the default content directory.
    return folder.replace(/^content\//, '');
}

function pathMatchesCollection(path, collection, config) {
    const normalizedPath = normalizePath(path);
    const colFolder = collectionFolderForArticlePaths(collection, config);
    if (colFolder === '') return normalizedPath !== '';
    return normalizedPath.startsWith(colFolder + "/") || normalizedPath === colFolder;
}

export function getCollectionForPath(path, config) {
    if (!config || !config.collections) return null;
    for (const col of config.collections) {
        if (pathMatchesCollection(path, col, config)) {
            return col;
        }
    }
    return null;
}

function renderFrontMatterForm(fm, path, config, container) {
    container.innerHTML = '';

    const details = document.createElement('details');
    details.style.marginBottom = '10px';

    const summary = document.createElement('summary');
    summary.textContent = "Article Settings";
    summary.style.fontWeight = 'bold';
    summary.style.cursor = 'pointer';
    summary.style.padding = '8px';
    summary.style.backgroundColor = '#2a2a2a';
    summary.style.color = '#ccc';
    summary.style.borderRadius = '4px';
    summary.style.outline = 'none';

    details.appendChild(summary);

    const wrapper = document.createElement('div');
    wrapper.style.padding = '10px';
    wrapper.style.border = '1px solid #333';
    wrapper.style.borderTop = 'none';

    const fragment = document.createDocumentFragment();
    const collection = getCollectionForPath(path, config);
    const definedFields = collection ? collection.fields : [];
    const processedKeys = new Set();

    definedFields.forEach(field => {
        if (field.name === 'body') return;
        const val = fm[field.name];
        renderField(fragment, field, val);
        processedKeys.add(field.name);
    });

    for (const [key, value] of Object.entries(fm)) {
        if (!processedKeys.has(key)) {
            let widget = 'string';
            if (typeof value === 'boolean') widget = 'boolean';
            else if (Array.isArray(value)) widget = 'list';
            renderField(fragment, { name: key, label: key + " (Extra)", widget: widget }, value);
        }
    }

    wrapper.appendChild(fragment);
    details.appendChild(wrapper);
    container.appendChild(details);
}

function renderField(container, field, value) {
    const div = document.createElement('div');
    div.className = 'fm-field';

    const label = document.createElement('label');
    label.className = 'fm-label';
    label.textContent = field.label || field.name;
    div.appendChild(label);

    if (field.widget === 'datetime') {
        const wrapper = document.createElement('div');
        wrapper.style.display = 'flex';
        wrapper.style.gap = '5px';
        const input = createInputForWidget(field, value);
        input.style.flex = '1';

        const nowBtn = document.createElement('button');
        nowBtn.textContent = 'Now';
        nowBtn.className = 'action-btn';
        nowBtn.style.background = '#444';
        nowBtn.style.padding = '4px 8px';
        nowBtn.style.fontSize = '12px';
        nowBtn.onclick = () => {
            const d = new Date();
            const pad = (n) => n < 10 ? '0' + n : n;
            const localIso = d.getFullYear() + '-' +
                           pad(d.getMonth()+1) + '-' +
                           pad(d.getDate()) + 'T' +
                           pad(d.getHours()) + ':' +
                           pad(d.getMinutes()) + ':' +
                           pad(d.getSeconds());
            input.value = localIso;
        };
        wrapper.appendChild(input);
        wrapper.appendChild(nowBtn);
        div.appendChild(wrapper);
    } else {
        const input = createInputForWidget(field, value);
        div.appendChild(input);
    }
    container.appendChild(div);
}

function createInputForWidget(field, value) {
    let input;
    if (field.widget === 'boolean') {
        input = document.createElement('input');
        input.type = 'checkbox';
        input.className = 'fm-checkbox';
        input.checked = value === true;
        input.dataset.key = field.name;
        input.dataset.widget = 'boolean';
    } else if (field.widget === 'datetime') {
        input = document.createElement('input');
        input.type = 'datetime-local';
        input.className = 'fm-input';
        if (value) {
            try {
                const d = new Date(value);
                const pad = (n) => n < 10 ? '0' + n : n;
                const localIso = d.getFullYear() + '-' +
                               pad(d.getMonth()+1) + '-' +
                               pad(d.getDate()) + 'T' +
                               pad(d.getHours()) + ':' +
                               pad(d.getMinutes()) + ':' +
                               pad(d.getSeconds());
                input.value = localIso;
            } catch (e) {
                input.value = value;
            }
        }
        input.dataset.key = field.name;
        input.dataset.widget = 'datetime';
    } else if (field.widget === 'list') {
        input = document.createElement('input');
        input.type = 'text';
        input.className = 'fm-input';
        input.placeholder = "Comma separated values";
        if (Array.isArray(value)) {
            input.value = value.join(', ');
        } else if (value) {
            input.value = String(value);
        }
        input.dataset.key = field.name;
        input.dataset.widget = 'list';
    } else {
        input = document.createElement('input');
        input.type = 'text';
        input.className = 'fm-input';
        input.value = (value === null || value === undefined) ? (field.default || '') : value;
        input.dataset.key = field.name;
        input.dataset.widget = 'string';
    }
    return input;
}

export function collectFrontMatter() {
    const fm = {};
    const inputs = document.querySelectorAll('#fm-container input');

    inputs.forEach(input => {
        const key = input.dataset.key;
        const widget = input.dataset.widget;

        if (widget === 'boolean') {
            fm[key] = input.checked;
        } else if (widget === 'list') {
            const val = input.value.trim();
            if (val === "") {
                fm[key] = [];
            } else {
                fm[key] = val.split(',').map(s => s.trim()).filter(s => s !== "");
            }
        } else if (widget === 'datetime') {
            if (input.value) {
                const d = new Date(input.value);
                const pad = (n) => (n < 10 ? '0' : '') + n;
                const tzo = -d.getTimezoneOffset();
                const dif = tzo >= 0 ? '+' : '-';
                const offH = pad(Math.floor(Math.abs(tzo) / 60));
                const offM = pad(Math.abs(tzo) % 60);

                fm[key] = d.getFullYear() + '-' +
                    pad(d.getMonth() + 1) + '-' +
                    pad(d.getDate()) + 'T' +
                    pad(d.getHours()) + ':' +
                    pad(d.getMinutes()) + ':' +
                    pad(d.getSeconds()) +
                    dif + offH + ':' + offM;
            } else {
                fm[key] = null;
            }
        } else {
            fm[key] = input.value;
        }
    });
    return fm;
}

export function clearMarkdownPreview() {
    const preview = document.getElementById('markdown-preview');
    const status = document.getElementById('markdown-preview-status');
    if (preview) preview.replaceChildren();
    if (status) {
        status.textContent = '記事を選択すると本文プレビューを表示します。';
        status.className = 'markdown-preview-status empty';
    }
}

export function showMarkdownPreviewLoading() {
    const status = document.getElementById('markdown-preview-status');
    if (!status) return;
    status.textContent = '本文プレビューを更新中…';
    status.className = 'markdown-preview-status loading';
    status.setAttribute('aria-busy', 'true');
}

export function renderMarkdownPreview(html) {
    const preview = document.getElementById('markdown-preview');
    const status = document.getElementById('markdown-preview-status');
    if (!preview || !status) return;

    // /admin/api/preview/markdown is the trust boundary: the server returns
    // sanitized HTML with raw Markdown HTML disabled.
    preview.innerHTML = html;
    status.removeAttribute('aria-busy');
    status.className = 'markdown-preview-status';
    status.textContent = html.trim() === '' ? '本文は空です。' : '';

    preview.querySelectorAll('a[href]').forEach(link => {
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
    });
    preview.querySelectorAll('img').forEach(image => {
        image.loading = 'lazy';
        image.decoding = 'async';
        image.referrerPolicy = 'no-referrer';
    });
}

export function showMarkdownPreviewError(error) {
    const preview = document.getElementById('markdown-preview');
    const status = document.getElementById('markdown-preview-status');
    if (!status) return;
    if (preview) preview.replaceChildren();
    status.removeAttribute('aria-busy');
    status.className = 'markdown-preview-status error';
    status.textContent = `本文プレビューを更新できませんでした: ${error?.message || 'Unknown error'}`;
}

export function safeExternalURL(value) {
    if (!value) return '';
    try {
        const rawValue = String(value).trim();
        if (!/^https?:\/\//i.test(rawValue)) return '';
        const parsed = new URL(rawValue);
        return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : '';
    } catch (_) {
        return '';
    }
}

export function normalizeDeploymentState(value) {
    if (!value || typeof value !== 'object') return null;
    const rawStatus = String(value.status || value.state || '').toLowerCase();
    const status = ['queued', 'building', 'ready', 'failed', 'stale'].includes(rawStatus) ? rawStatus : 'queued';
    return {
        ...value,
        status,
        commit_sha: String(value.commit_sha || value.commit || ''),
        url: safeExternalURL(value.url || value.deployment_url),
        log_url: safeExternalURL(value.log_url),
    };
}

export function normalizeLocalPreviewState(value) {
    if (!value || typeof value !== 'object') return null;
    return { ...value };
}

export function configureDeploymentPreview(config) {
    const panel = document.getElementById('deployment-panel');
    if (!panel) return false;
    const deployment = config?._cms?.preview_deployment;
    const enabled = deployment?.enabled === true;
    panel.classList.toggle('hidden', !enabled);

    const provider = document.getElementById('deployment-provider');
    if (provider) provider.textContent = enabled && deployment.provider ? deployment.provider : '';

    const warning = document.getElementById('deployment-access-warning');
    if (warning) warning.classList.toggle('hidden', !enabled || deployment.access_protected === true);
    if (!enabled) renderDeploymentState(null);
    return enabled;
}

export function renderDeploymentState(rawState) {
    const state = normalizeDeploymentState(rawState);
    const status = document.getElementById('deployment-status');
    const commit = document.getElementById('deployment-commit');
    const message = document.getElementById('deployment-message');
    const openLink = document.getElementById('deployment-open-link');
    const logLink = document.getElementById('deployment-log-link');
    const updateButton = document.getElementById('deployment-update-btn');
    const retryButton = document.getElementById('deployment-retry-btn');
    const discardButton = document.getElementById('deployment-discard-btn');
    if (!status) return;

    const inProgress = state?.status === 'queued' || state?.status === 'building';
    status.textContent = state ? ({
        queued: '待機中',
        building: 'ビルド中',
        ready: '確認可能',
        failed: '失敗',
        stale: '更新が必要',
    })[state.status] : '未作成';
    status.className = `deployment-status ${state?.status || 'idle'}`;
    if (commit) commit.textContent = state?.commit_sha ? state.commit_sha.slice(0, 8) : '';
    if (message) message.textContent = state?.message || state?.failure_reason || state?.error || '';

    setExternalLink(openLink, state?.status === 'ready' ? state.url : '', 'デプロイプレビューを開く');
    setExternalLink(logLink, state?.status === 'failed' ? state.log_url : '', 'ビルドログを開く');
    if (updateButton) updateButton.disabled = inProgress;
    if (retryButton) retryButton.classList.toggle('hidden', state?.status !== 'failed' || state?.retryable === false);
    if (discardButton) discardButton.classList.toggle('hidden', !state);
}

function setExternalLink(link, url, label) {
    if (!link) return;
    link.classList.toggle('hidden', !url);
    if (!url) {
        link.removeAttribute('href');
        return;
    }
    link.href = url;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.textContent = label;
}

export function showDiffModal(diffText) {
    const body = document.getElementById('modal-body');
    body.innerHTML = ''; // Clear previous content

    if (!diffText) {
        body.textContent = "No differences";
    } else {
        const lines = diffText.split('\n');
        lines.forEach(line => {
            const div = document.createElement('div');
            if (line.startsWith('+')) {
                const span = document.createElement('span');
                span.className = 'diff-added';
                span.textContent = line;
                div.appendChild(span);
            } else if (line.startsWith('-')) {
                const span = document.createElement('span');
                span.className = 'diff-removed';
                span.textContent = line;
                div.appendChild(span);
            } else {
                div.textContent = line;
            }
            body.appendChild(div);
        });
    }
    document.getElementById('modal-overlay').style.display = 'flex';
}

export function toggleHeaderMenu() {
    document.getElementById("header-menu-dropdown").classList.toggle("show");
}

// Close the dropdown if the user clicks outside of it
window.onclick = function (event) {
    if (!event.target.matches('.mobile-actions button') && !event.target.matches('.mobile-actions button *')) {
        const dropdowns = document.getElementsByClassName("dropdown-content");
        for (let i = 0; i < dropdowns.length; i++) {
            const openDropdown = dropdowns[i];
            if (openDropdown.classList.contains('show')) {
                openDropdown.classList.remove('show');
            }
        }
    }
}

export function showCreationModal(config, onCreate) {
    const overlay = document.getElementById('modal-overlay');
    const header = document.getElementById('modal-header');
    const body = document.getElementById('modal-body');

    header.querySelector('span').textContent = "New Article";
    body.innerHTML = '';
    overlay.style.display = 'flex';

    if (!config || !config.collections || config.collections.length === 0) {
        body.innerHTML = '<p>No collections defined in config.</p>';
        return;
    }

    // Collection Selector
    const selWrapper = document.createElement('div');
    selWrapper.style.marginBottom = '15px';
    selWrapper.innerHTML = '<strong>Collection: </strong>';

    const select = document.createElement('select');
    select.className = 'fm-input';
    select.style.width = 'auto';
    select.style.display = 'inline-block';

    config.collections.forEach(c => {
        const opt = document.createElement('option');
        opt.value = c.name;
        opt.textContent = c.label || c.name;
        select.appendChild(opt);
    });
    selWrapper.appendChild(select);
    body.appendChild(selWrapper);

    // Fields Container
    const fieldsContainer = document.createElement('div');
    fieldsContainer.id = 'creation-fields';
    body.appendChild(fieldsContainer);

    // Render fields for initial selection
    const render = () => {
        fieldsContainer.innerHTML = '';
        const colName = select.value;
        const col = config.collections.find(c => c.name === colName);
        if (col && col.fields) {
            col.fields.forEach(field => {
                if (field.name === "body") return;
                // Pre-fill defaults
                const val = field.default !== undefined ? field.default : null;
                renderField(fieldsContainer, field, val);
            });
        }
    };
    select.onchange = render;
    render();

    // Create Button
    const btnDiv = document.createElement('div');
    btnDiv.style.marginTop = '20px';
    btnDiv.style.textAlign = 'right';

    const createBtn = document.createElement('button');
    createBtn.className = 'action-btn';
    createBtn.style.background = '#2da44e';
    createBtn.textContent = 'Create';
    createBtn.onclick = () => {
        const colName = select.value;
        // Collect data
        const fields = {};
        const inputs = fieldsContainer.querySelectorAll('input');
        inputs.forEach(input => {
            const key = input.dataset.key;
            const widget = input.dataset.widget;
            if (widget === 'boolean') {
                fields[key] = input.checked;
            } else if (widget === 'list') {
                const val = input.value.trim();
                fields[key] = val === "" ? [] : val.split(',').map(s => s.trim());
            } else if (widget === 'datetime') {
                if (input.value) {
                    const d = new Date(input.value);
                    const pad = (n) => (n < 10 ? '0' : '') + n;
                    const tzo = -d.getTimezoneOffset();
                    const dif = tzo >= 0 ? '+' : '-';
                    const offH = pad(Math.floor(Math.abs(tzo) / 60));
                    const offM = pad(Math.abs(tzo) % 60);

                    fields[key] = d.getFullYear() + '-' +
                        pad(d.getMonth() + 1) + '-' +
                        pad(d.getDate()) + 'T' +
                        pad(d.getHours()) + ':' +
                        pad(d.getMinutes()) + ':' +
                        pad(d.getSeconds()) +
                        dif + offH + ':' + offM;
                } else {
                    fields[key] = null;
                }
            } else {
                fields[key] = input.value;
            }
        });
        onCreate(colName, fields);
        closeModal();
    };

    btnDiv.appendChild(createBtn);
    body.appendChild(btnDiv);
}

export function closeModal() {
    document.getElementById('modal-overlay').style.display = 'none';
}

// Toast Notifications
export function showToast(message, type = 'info') {
    let container = document.getElementById('toast-container');
    if (!container) {
        container = document.createElement('div');
        container.id = 'toast-container';
        document.body.appendChild(container);
    }

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    
    const msgSpan = document.createElement('span');
    msgSpan.textContent = message;
    toast.appendChild(msgSpan);

    // Close button
    const closeBtn = document.createElement('span');
    closeBtn.innerHTML = '&times;';
    closeBtn.style.cursor = 'pointer';
    closeBtn.style.marginLeft = '10px';
    closeBtn.onclick = () => {
        toast.style.opacity = '0';
        setTimeout(() => toast.remove(), 300);
    };
    toast.appendChild(closeBtn);

    container.appendChild(toast);

    // Auto remove
    setTimeout(() => {
        if (toast.parentElement) {
            toast.style.animation = 'fadeOut 0.3s forwards';
            setTimeout(() => toast.remove(), 300);
        }
    }, 5000);
}

export async function showMediaLibrary(onSelect, collectionName = null, currentPath = null) {
    const overlay = document.getElementById('modal-overlay');
    const header = document.getElementById('modal-header');
    const body = document.getElementById('modal-body');

    header.querySelector('span').textContent = "Media Library";
    body.innerHTML = '';
    overlay.style.display = 'flex';

    // Tabs
    const tabs = document.createElement('div');
    tabs.style.display = 'flex';
    tabs.style.gap = '10px';
    tabs.style.marginBottom = '15px';
    tabs.style.borderBottom = '1px solid #444';
    tabs.style.paddingBottom = '5px';

    const createTab = (id, label) => {
        const t = document.createElement('button');
        t.textContent = label;
        t.style.padding = '8px 16px';
        t.style.border = 'none';
        t.style.background = 'transparent';
        t.style.color = '#888';
        t.style.cursor = 'pointer';
        t.style.fontSize = '14px';
        t.style.fontWeight = 'bold';
        t.id = `tab-media-${id}`;
        return t;
    };

    const tabStatic = createTab('static', 'Static');
    const tabArticle = createTab('content', 'Article');

    const isBundle = currentPath && (currentPath.endsWith('/index.md') || currentPath.endsWith('/_index.md'));

    if (!isBundle) {
        tabArticle.disabled = true;
        tabArticle.style.opacity = '0.5';
        tabArticle.title = "Only available for page bundles (index.md)";
    }

    tabs.appendChild(tabStatic);
    tabs.appendChild(tabArticle);
    body.appendChild(tabs);

    const contentArea = document.createElement('div');
    body.appendChild(contentArea);

    // Tab Logic
    const switchTab = (mode) => {
        // Update styles
        [tabStatic, tabArticle].forEach(t => {
            t.style.borderBottom = 'none';
            t.style.color = '#888';
        });
        const activeTab = mode === 'static' ? tabStatic : tabArticle;
        activeTab.style.borderBottom = '2px solid #6f42c1';
        activeTab.style.color = '#fff';

        // Load content
        loadAndRenderMedia(contentArea, mode, currentPath, onSelect);
    };

    tabStatic.onclick = () => switchTab('static');
    tabArticle.onclick = () => { if(isBundle) switchTab('content'); };

    // Default tab
    switchTab(isBundle ? 'content' : 'static');
}

async function loadAndRenderMedia(container, mode, currentPath, onSelect) {
    container.innerHTML = 'Loading...';
    try {
        const files = await API.fetchMedia(mode, currentPath);
        renderMediaGrid(container, files || [], mode, currentPath, onSelect);
    } catch (e) {
        container.innerHTML = `<p style="color:red">Failed to load media: ${e.message}</p>`;
    }
}

function renderMediaGrid(container, files, mode, currentPath, onSelect) {
    container.innerHTML = '';

    // Toolbar (Upload)
    const toolbar = document.createElement('div');
    toolbar.style.marginBottom = '10px';
    toolbar.style.display = 'flex';
    toolbar.style.justifyContent = 'space-between';
    
    const fileInput = document.createElement('input');
    fileInput.type = 'file';
    fileInput.accept = 'image/*';
    fileInput.style.display = 'none';
    fileInput.onchange = async (e) => {
        if (e.target.files.length > 0) {
            const file = e.target.files[0];
            showToast("Uploading...", "info");
            try {
                const result = await API.uploadMedia(file, mode, currentPath);
                if (result?.local_preview_sync === false) {
                    showToast("Uploaded, but Local Live Preview sync failed", "warning");
                } else {
                    showToast("Uploaded!", "success");
                }
                loadAndRenderMedia(container, mode, currentPath, onSelect);
            } catch (err) {
                showToast("Upload failed: " + err.message, "error");
            }
        }
    };

    const uploadBtn = document.createElement('button');
    uploadBtn.className = 'action-btn';
    uploadBtn.textContent = `⬆ Upload to ${mode === 'static' ? 'Static' : 'Article'}`;
    uploadBtn.onclick = () => fileInput.click();

    toolbar.appendChild(uploadBtn);
    container.appendChild(toolbar);
    container.appendChild(fileInput);

    // Grid
    const grid = document.createElement('div');
    grid.style.display = 'grid';
    grid.style.gridTemplateColumns = 'repeat(auto-fill, minmax(100px, 1fr))';
    grid.style.gap = '10px';
    grid.style.maxHeight = '400px';
    grid.style.overflowY = 'auto';

    if (files.length === 0) {
        grid.innerHTML = '<p style="grid-column: 1/-1; text-align:center; color:#888;">No images found.</p>';
    }

    files.forEach(f => {
        const item = document.createElement('div');
        item.style.border = '1px solid #444';
        item.style.borderRadius = '4px';
        item.style.overflow = 'hidden';
        item.style.cursor = 'pointer';
        item.style.position = 'relative';
        item.style.backgroundColor = '#222';

        const img = document.createElement('img');
        img.src = f.url;
        img.style.width = '100%';
        img.style.height = '100px';
        img.style.objectFit = 'cover';
        img.title = f.name;

        const name = document.createElement('div');
        name.textContent = f.name;
        name.style.position = 'absolute';
        name.style.bottom = '0';
        name.style.width = '100%';
        name.style.background = 'rgba(0,0,0,0.7)';
        name.style.fontSize = '10px';
        name.style.padding = '2px';
        name.style.whiteSpace = 'nowrap';
        name.style.overflow = 'hidden';
        name.style.textOverflow = 'ellipsis';
        name.style.textAlign = 'center';

        const delBtn = document.createElement('button');
        delBtn.textContent = '×';
        delBtn.style.position = 'absolute';
        delBtn.style.top = '0';
        delBtn.style.right = '0';
        delBtn.style.background = 'red';
        delBtn.style.color = 'white';
        delBtn.style.border = 'none';
        delBtn.style.cursor = 'pointer';
        delBtn.style.padding = '0 5px';
        
        delBtn.onclick = async (e) => {
            e.stopPropagation();
            if (!confirm(`Delete ${f.name}?`)) return;
            try {
                const result = await API.deleteMedia(f.repo_path);
                if (result?.local_preview_sync === false) {
                    showToast("Deleted, but Local Live Preview sync failed", "warning");
                } else {
                    showToast("Deleted", "success");
                }
                loadAndRenderMedia(container, mode, currentPath, onSelect);
            } catch (err) {
                showToast("Delete failed", "error");
            }
        };

        item.onclick = () => {
            onSelect(f);
            closeModal();
        };

        item.appendChild(img);
        item.appendChild(name);
        item.appendChild(delBtn);
        grid.appendChild(item);
    });

        container.appendChild(grid);

    }

    

    export function showSnippetsModal(snippets, onSelect) {

        const overlay = document.getElementById('modal-overlay');

        const header = document.getElementById('modal-header');

        const body = document.getElementById('modal-body');

    

        header.querySelector('span').textContent = "Insert Snippet";

        body.innerHTML = '';

        overlay.style.display = 'flex';

    

        if (!snippets || Object.keys(snippets).length === 0) {

            body.innerHTML = '<p>No snippets found.</p>';

            return;

        }

    

        const list = document.createElement('div');

        list.style.display = 'flex';

        list.style.flexDirection = 'column';

        list.style.gap = '5px';

    

        for (const [key, val] of Object.entries(snippets)) {

            const btn = document.createElement('button');

            btn.className = 'action-btn secondary';

            btn.style.textAlign = 'left';

            btn.style.display = 'flex';

            btn.style.flexDirection = 'column';

            btn.style.alignItems = 'flex-start';

            btn.style.padding = '8px';

            

            const title = document.createElement('span');

            title.style.fontWeight = 'bold';

            title.textContent = key;

            

            const desc = document.createElement('span');

            desc.style.fontSize = '12px';

            desc.style.color = '#ccc';

            desc.textContent = val.description || "";

    

            btn.appendChild(title);

            if (val.description) btn.appendChild(desc);

    

            btn.onclick = () => {

                onSelect(val);

            };

            list.appendChild(btn);

        }

        body.appendChild(list);

    }

    

    export function showSnippetInputModal(variables, onConfirm, onBack) {

        const overlay = document.getElementById('modal-overlay');

        const header = document.getElementById('modal-header');

        const body = document.getElementById('modal-body');

    

        header.querySelector('span').textContent = "Snippet Parameters";

        body.innerHTML = '';

        

        const form = document.createElement('div');

        form.style.display = 'flex';

        form.style.flexDirection = 'column';

        form.style.gap = '10px';

    

        const inputs = {};

    

        variables.forEach(v => {

            const wrapper = document.createElement('div');

            wrapper.style.display = 'flex';

            wrapper.style.flexDirection = 'column';

            

            const label = document.createElement('label');

            label.textContent = v.label || `Parameter ${v.id}`;

            label.style.fontSize = '12px';

            label.style.marginBottom = '4px';

    

            const input = document.createElement('input');

            input.type = 'text';

            input.className = 'fm-input';

            input.placeholder = v.default || ""; // Set default as placeholder
            input.value = ""; // Clear initial value

            

            inputs[v.id] = { el: input, default: v.default || "" }; // Store default for fallback

            

            wrapper.appendChild(label);

            wrapper.appendChild(input);

            form.appendChild(wrapper);

        });

    

        const btnRow = document.createElement('div');

        btnRow.style.display = 'flex';

        btnRow.style.justifyContent = 'space-between'; // Changed to space-between
        btnRow.style.marginTop = '10px';

        const backBtn = document.createElement('button');
        backBtn.className = 'action-btn secondary';
        backBtn.textContent = 'Back';
        backBtn.onclick = () => {
             if (onBack) onBack();
        };

        const confirmBtn = document.createElement('button');

        confirmBtn.className = 'action-btn';

        confirmBtn.textContent = 'Insert';

        confirmBtn.onclick = () => {

            const values = {};

            for (const [id, data] of Object.entries(inputs)) {
                // Use input value or fallback to default
                values[id] = data.el.value !== "" ? data.el.value : data.default;
            }

            onConfirm(values);

            closeModal();

        };
    
        btnRow.appendChild(backBtn);
        btnRow.appendChild(confirmBtn);

        form.appendChild(btnRow);

        body.appendChild(form);

        // Focus first input
        if (variables.length > 0) {
            inputs[variables[0].id].el.focus();
        }
    }
