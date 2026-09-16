// CSRF Token Management
let csrfToken = null;
const CURRENT_SITE_STORAGE_KEY = 'homecms:site';
const LEGACY_CURRENT_SITE_STORAGE_KEY = 'hugo-cms:site';
let currentSite = window.localStorage.getItem(CURRENT_SITE_STORAGE_KEY)
    || window.localStorage.getItem(LEGACY_CURRENT_SITE_STORAGE_KEY)
    || "";

export function getCurrentSite() {
    return currentSite;
}

export function setCurrentSite(siteID) {
    currentSite = siteID || "";
    if (currentSite) {
        window.localStorage.setItem(CURRENT_SITE_STORAGE_KEY, currentSite);
        window.localStorage.removeItem(LEGACY_CURRENT_SITE_STORAGE_KEY);
    } else {
        window.localStorage.removeItem(CURRENT_SITE_STORAGE_KEY);
        window.localStorage.removeItem(LEGACY_CURRENT_SITE_STORAGE_KEY);
    }
}

export function initializeCurrentSite(registry) {
    const sites = registry?.sites || [];
    const siteExists = currentSite && sites.some(site => site.id === currentSite);
    if (!siteExists) {
        setCurrentSite(registry?.default_site || sites[0]?.id || "");
    }
}

function withSite(url, siteID = currentSite) {
    if (!siteID) return url;
    const parsed = new URL(url, window.location.origin);
    parsed.searchParams.set('site', siteID);
    return parsed.pathname + parsed.search;
}

function siteHeaders(siteID = currentSite) {
    return siteID ? { 'X-CMS-Site': siteID } : {};
}

async function ensureCSRFToken(forceRefresh = false) {
    if (csrfToken && !forceRefresh) return csrfToken;
    const res = await fetch('/admin/api/csrf-token');
    if (!res.ok) throw new Error("Failed to fetch CSRF token");
    const data = await res.json();
    csrfToken = data.csrf_token;
    return csrfToken;
}

function getCSRFHeaders() {
    return csrfToken ? { 'X-CSRF-Token': csrfToken } : {};
}

function resetCSRFToken() {
    csrfToken = null;
}

async function fetchWithCSRF(url, options) {
    await ensureCSRFToken();
    const request = () => fetch(url, {
        ...options,
        headers: { ...(options.headers || {}), ...getCSRFHeaders() }
    });
    let res = await request();
    if (res.status === 403) {
        resetCSRFToken();
        await ensureCSRFToken(true);
        res = await request();
    }
    return res;
}

async function responseError(res, fallback) {
	let message = fallback;
	let data = null;
	try {
		data = await res.json();
		message = data?.message || data?.error || fallback;
	} catch (_) {
        // Keep the stable fallback when the server did not return JSON.
    }
	const error = new Error(message);
	error.status = res.status;
	error.code = data?.code || "";
	error.currentRevision = data?.current_revision || "";
	return error;
}

export async function fetchConfig(siteID = currentSite, signal) {
    const res = await fetch(withSite('/admin/api/config', siteID), { headers: siteHeaders(siteID), signal });
    if (!res.ok) throw new Error("Config fetch failed");
    return await res.json();
}

export async function fetchSites() {
    const res = await fetch('/admin/api/sites');
    if (!res.ok) throw new Error("Sites fetch failed");
    return await res.json();
}

export async function fetchArticles(siteID = currentSite, signal) {
    const res = await fetch(withSite('/admin/api/articles', siteID), { headers: siteHeaders(siteID), signal });
    if (res.status === 401) {
        window.location.href = "/admin/login";
        return null;
    }
    return await res.json();
}

export async function fetchArticle(path, signal) {
	const params = new URLSearchParams({ path });
	const res = await fetch(withSite(`/admin/api/article?${params.toString()}`), { headers: siteHeaders(), signal });
	if (!res.ok) throw await responseError(res, "Failed to load article");
	return await res.json();
}

export async function saveArticle(payload) {
	const request = () => fetch(withSite('/admin/api/article'), {
		method: 'POST',
		headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(),
            ...getCSRFHeaders()
		},
		body: JSON.stringify(payload)
	});
	await ensureCSRFToken();
	let res = await request();
	if (res.status === 403) {
		resetCSRFToken();
		await ensureCSRFToken(true);
		res = await request();
	}
	if (!res.ok) throw await responseError(res, "Save failed");
	return await res.json();
}

export async function createArticle(arg1, arg2) {
    await ensureCSRFToken();
    let body;
    if (typeof arg1 === 'object') {
        body = arg1;
    } else {
        body = { path: arg1, content: arg2 };
    }

    const res = await fetch(withSite('/admin/api/create'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(),
            ...getCSRFHeaders()
        },
        body: JSON.stringify(body)
    });
    if (!res.ok) {
        const data = await res.json();
        throw new Error(data.message || data.error || "Create failed");
    }
    return await res.json();
}

export async function deleteArticle(path, baseRevision = "") {
	await ensureCSRFToken();
	const res = await fetch(withSite('/admin/api/delete'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(),
            ...getCSRFHeaders()
        },
		body: JSON.stringify({ path, base_revision: baseRevision })
	});
	if (!res.ok) throw await responseError(res, "Delete failed");
	return await res.json();
}

export async function getDiff(payload) {
    await ensureCSRFToken();
    const res = await fetch(withSite('/admin/api/diff'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(),
            ...getCSRFHeaders()
        },
        body: JSON.stringify(payload)
    });
    return await res.json();
}

export async function runSync() {
    await ensureCSRFToken();
    const res = await fetch(withSite('/admin/api/sync'), {
        method: 'POST',
        headers: {
            ...siteHeaders(),
            ...getCSRFHeaders()
        }
    });
    return await res.json();
}

export async function runPublish(path, draftID, siteID = currentSite, mode = 'preview') {
    const res = await fetchWithCSRF(withSite('/admin/api/publish', siteID), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(siteID)
        },
        body: JSON.stringify({ path, draft_id: draftID, mode })
    });
    if (!res.ok) throw new Error("Publish failed");
    return await res.json();
}

export async function renderMarkdownPreview(payload, signal) {
    const res = await fetchWithCSRF(withSite('/admin/api/preview/markdown'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders()
        },
        body: JSON.stringify(payload),
        signal
    });
    if (!res.ok) throw new Error("Markdown preview failed");
    return await res.json();
}

export async function updateLocalPreviewContent(payload, revision, signal) {
    const res = await fetchWithCSRF(withSite('/admin/api/preview/local'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders()
        },
        body: JSON.stringify({ ...payload, revision }),
        signal
    });
    if (!res.ok) throw await responseError(res, "Local Live Preview update failed");
    return await res.json();
}

export async function resolveLocalPreviewArticleURL(path, signal, siteID = currentSite) {
    const res = await fetchWithCSRF(withSite('/admin/api/preview/local/navigate'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(siteID)
        },
        body: JSON.stringify({ path }),
        signal
    });
    if (!res.ok) throw await responseError(res, "Local Live Preview URL resolution failed");
    return await res.json();
}

// Backward-compatible name for integrations that still use the pre-#44 API
// helper. The endpoint now resolves and returns the article URL directly.
export const navigateLocalPreviewContent = resolveLocalPreviewArticleURL;

export async function fetchLocalPreviewStatus(signal, siteID = currentSite) {
    const url = '/admin/api/preview/local/status';
    const res = await fetch(withSite(url, siteID), {
        headers: siteHeaders(siteID),
        signal
    });
    if (!res.ok) throw await responseError(res, "Local Live Preview status failed");
    return await res.json();
}

export async function stopLocalPreviewContent(siteID = currentSite) {
    const res = await fetchWithCSRF(withSite('/admin/api/preview/local/stop', siteID), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(siteID)
        },
    });
    if (!res.ok) throw await responseError(res, "Local Live Preview stop failed");
    return await res.json();
}

export async function triggerPreviewDeployment(path, draftID, siteID = currentSite) {
    const res = await fetchWithCSRF(withSite('/admin/api/preview/deployments', siteID), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(siteID)
        },
        body: JSON.stringify({ path, draft_id: draftID })
    });
    if (!res.ok) throw new Error("Failed to update deployment preview");
    return await res.json();
}

export async function fetchPreviewDeployment(draftID, signal, siteID = currentSite) {
    const id = encodeURIComponent(draftID);
    const res = await fetch(withSite(`/admin/api/preview/deployments/${id}`, siteID), {
        headers: siteHeaders(siteID),
        signal
    });
    if (res.status === 404) return null;
    if (!res.ok) throw new Error("Failed to fetch deployment preview");
    return await res.json();
}

async function postPreviewDeploymentAction(draftID, action, siteID = currentSite) {
    const id = encodeURIComponent(draftID);
    const res = await fetchWithCSRF(withSite(`/admin/api/preview/deployments/${id}/${action}`, siteID), {
        method: 'POST',
        headers: {
            ...siteHeaders(siteID)
        }
    });
    if (!res.ok) throw new Error(`Failed to ${action} deployment preview`);
    return await res.json();
}

export function retryPreviewDeployment(draftID, siteID = currentSite) {
    return postPreviewDeploymentAction(draftID, 'retry', siteID);
}

export function discardPreviewDeployment(draftID, siteID = currentSite) {
    return postPreviewDeploymentAction(draftID, 'discard', siteID);
}

export async function fetchMedia(mode, path) {
    let url = `/admin/api/media?mode=${mode}`;
    if (path) url += `&path=${encodeURIComponent(path)}`;
    const res = await fetch(withSite(url), { headers: siteHeaders() });
    if (!res.ok) throw new Error("Failed to fetch media");
    return await res.json();
}

export async function uploadMedia(file, mode, path) {
    await ensureCSRFToken();
    const formData = new FormData();
    formData.append('file', file);
    if (mode) formData.append('mode', mode);
    if (path) formData.append('path', path);
    const res = await fetch(withSite('/admin/api/media'), {
        method: 'POST',
        headers: {
            ...siteHeaders(),
            ...getCSRFHeaders()
        },
        body: formData
    });
    if (!res.ok) throw new Error("Upload failed");
    return await res.json();
}

export async function deleteMedia(repoPath) {
    await ensureCSRFToken();
    const res = await fetch(withSite('/admin/api/media/delete'), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            ...siteHeaders(),
            ...getCSRFHeaders()
        },
        body: JSON.stringify({ repo_path: repoPath })
    });
    if (!res.ok) throw new Error("Delete failed");
    return await res.json();
}

export async function fetchSnippets() {
    const res = await fetch(withSite('/admin/api/snippets'), { headers: siteHeaders() });
    if (!res.ok) throw new Error("Failed to fetch snippets");
    return await res.json();
}
