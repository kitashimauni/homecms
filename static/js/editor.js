import * as API from './api.js';
import * as UI from './ui.js';

let currentPath = "";
let currentData = null;
let cmsConfig = null;
let autoSaveTimer = null;
let lastSavedPayload = "";
let lastQueuedPayload = "";
let saveQueue = Promise.resolve();
let deletingPath = "";
let previewTimer = null;
let previewController = null;
let previewRevision = 0;
let localPreviewTimer = null;
let localPreviewRevision = 0;
let articleLoadGeneration = 0;
let articleLoadController = null;
let gitSyncInProgress = false;
let articleRevisionConflictPath = "";
const localPreviewInflight = new Set();

const PREVIEW_DEBOUNCE_MS = 180;
const LOCAL_PREVIEW_DEBOUNCE_MS = 250;
const GIT_SYNC_WRITE_PAUSED_MESSAGE = "Git Sync is in progress";
const ARTICLE_REVISION_CONFLICT_MESSAGE = "この記事は別tabまたは外部で更新されました。ResetまたはDiffで確認してください。";
const gitMutationInflight = new Set();

export function getCurrentPath() {
    return currentPath;
}

export function hasUnsavedChanges() {
    if (!currentPath || currentPath === deletingPath) return false;
    return serializePayload(getPayload()) !== lastSavedPayload;
}

function setEditorWritePaused(paused) {
    const editor = document.getElementById('editor');
    if (editor) editor.disabled = paused;

    const fmContainer = document.getElementById('fm-container');
    if (fmContainer && typeof fmContainer.querySelectorAll === 'function') {
        fmContainer.querySelectorAll('input, textarea, select, button').forEach(control => {
            control.disabled = paused;
        });
    }
}

function assertGitSyncWritesAllowed() {
    if (gitSyncInProgress) {
        throw new Error(GIT_SYNC_WRITE_PAUSED_MESSAGE);
    }
}

export async function runGitMutation(operation) {
    assertGitSyncWritesAllowed();
    const mutation = Promise.resolve().then(() => {
        assertGitSyncWritesAllowed();
        return operation();
    });
    gitMutationInflight.add(mutation);
    mutation.then(
        () => gitMutationInflight.delete(mutation),
        () => gitMutationInflight.delete(mutation),
    );
    return mutation;
}

export function getCurrentLocalPreviewFrontMatterKey() {
    if (!currentPath) return "";
    return JSON.stringify(getPayload().frontmatter ?? null);
}

function draftStorageKey(siteID, path) {
    return `homecms:draft:${siteID}:${path}`;
}

function legacyDraftStorageKey(siteID, path) {
    return `hugo-cms:draft:${siteID}:${path}`;
}

export function createDraftUUID(cryptoProvider = window.crypto) {
    if (typeof cryptoProvider?.randomUUID === 'function') {
        return cryptoProvider.randomUUID();
    }
    if (typeof cryptoProvider?.getRandomValues !== 'function') {
        throw new Error('Secure random number generation is unavailable');
    }

    const bytes = new Uint8Array(16);
    cryptoProvider.getRandomValues(bytes);
    bytes[6] = (bytes[6] & 0x0f) | 0x40; // UUID version 4
    bytes[8] = (bytes[8] & 0x3f) | 0x80; // RFC 4122 variant

    const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0'));
    return `${hex.slice(0, 4).join('')}-${hex.slice(4, 6).join('')}-${hex.slice(6, 8).join('')}-${hex.slice(8, 10).join('')}-${hex.slice(10).join('')}`;
}

export function getOrCreateDraftID(siteID, path, storage = window.sessionStorage, createUUID = createDraftUUID) {
    if (!siteID || !path) return "";
    const key = draftStorageKey(siteID, path);
    const legacyKey = legacyDraftStorageKey(siteID, path);
    let draftID = storage.getItem(key) || storage.getItem(legacyKey);
    if (!draftID) {
        draftID = createUUID();
    }
    storage.setItem(key, draftID);
    storage.removeItem(legacyKey);
    return draftID;
}

export function getDraftID() {
    return getOrCreateDraftID(API.getCurrentSite(), currentPath);
}

export function resetDraftID() {
    if (currentPath) {
        const siteID = API.getCurrentSite();
        window.sessionStorage.removeItem(draftStorageKey(siteID, currentPath));
        window.sessionStorage.removeItem(legacyDraftStorageKey(siteID, currentPath));
    }
}

export function setConfig(cfg) {
    cmsConfig = cfg;
}

function localPreviewEnabled() {
    return cmsConfig?._cms?.local_preview?.enabled === true;
}

function beginArticleLoad() {
    if (articleLoadController) articleLoadController.abort();
    const request = {
        generation: ++articleLoadGeneration,
        controller: new AbortController(),
    };
    articleLoadController = request.controller;
    return request;
}

function isCurrentArticleLoad(request) {
    return request.generation === articleLoadGeneration && articleLoadController === request.controller;
}

function cancelArticleLoad() {
    articleLoadGeneration += 1;
    if (articleLoadController) articleLoadController.abort();
    articleLoadController = null;
}

function finishArticleLoad(request) {
    if (articleLoadController === request.controller) articleLoadController = null;
}

export function clearEditor() {
    cancelArticleLoad();
    clearAutoSaveTimer();
    cancelMarkdownPreview();
    cancelLocalPreviewTimer();
    resetLocalPreviewClientState();
    currentPath = "";
    currentData = null;
    lastSavedPayload = "";
    lastQueuedPayload = "";
    deletingPath = "";
    articleRevisionConflictPath = "";

    const display = document.getElementById('filename-display');
    if (display) display.textContent = "Select a file...";

    const editor = document.getElementById('editor');
    if (editor) {
        editor.value = "";
        editor.placeholder = "Select a file to edit...";
        editor.disabled = false;
    }

    const fmContainer = document.getElementById('fm-container');
    if (fmContainer) {
        fmContainer.innerHTML = "";
        fmContainer.style.display = 'none';
    }
    setEditorWritePaused(gitSyncInProgress);

    UI.clearMarkdownPreview();
}

export function initAutoSave() {
    const editor = document.getElementById('editor');
    const fmContainer = document.getElementById('fm-container');

    if (editor) editor.addEventListener('input', handleEditorChange);
    if (fmContainer) {
        fmContainer.addEventListener('input', handleEditorChange);
        fmContainer.addEventListener('change', handleEditorChange);
    }
}

function handleEditorChange() {
    if (gitSyncInProgress) return;
    if (currentData && typeof currentData.raw_content === 'string') {
        currentData.raw_content = '';
    }
    triggerAutoSave();
    scheduleMarkdownPreview();
    scheduleLocalLivePreview();
    if (window.markDeploymentPreviewStale) window.markDeploymentPreviewStale();
}

function triggerAutoSave() {
    if (gitSyncInProgress || !currentPath) return;
    if (currentPath === deletingPath) return;
    if (articleRevisionConflictPath === currentPath) return;
    clearAutoSaveTimer();

    // Debounce 3 seconds
    autoSaveTimer = setTimeout(() => {
        autoSaveTimer = null;
        execAutoSave().catch(() => {
            // The editor status already reports the failure. Avoid an
            // unhandled rejection from the timer callback.
        });
    }, 3000);
}

function clearAutoSaveTimer() {
    if (autoSaveTimer) {
        clearTimeout(autoSaveTimer);
        autoSaveTimer = null;
    }
}

function updateSaveStatus(msg, type) {
    const el = document.getElementById('save-status');
    if (!el) return;
    el.textContent = msg;
    if (type === 'saving') el.style.color = '#e2c08d';
    else if (type === 'saved') {
        el.style.color = '#81b181';
        setTimeout(() => { if (el.textContent === msg) el.textContent = ''; }, 2000);
    }
    else if (type === 'error') el.style.color = '#d67a7a';
    else el.style.color = '#888';
}

function isArticleRevisionConflict(error) {
    return error?.status === 409;
}

function markArticleRevisionConflict() {
    if (!currentPath) return;
    articleRevisionConflictPath = currentPath;
    clearAutoSaveTimer();
    updateSaveStatus("外部更新を検出", "error");
    UI.showToast(ARTICLE_REVISION_CONFLICT_MESSAGE, "warning");
}

function serializePayload(payload) {
    if (!payload) return "";
    const { base_revision: _baseRevision, ...contentPayload } = payload;
    return JSON.stringify(contentPayload);
}

function cancelMarkdownPreview() {
    if (previewTimer) {
        clearTimeout(previewTimer);
        previewTimer = null;
    }
    if (previewController) {
        previewController.abort();
        previewController = null;
    }
    previewRevision += 1;
}

function scheduleMarkdownPreview() {
    if (gitSyncInProgress || !currentPath || currentPath === deletingPath) return;
    if (previewTimer) clearTimeout(previewTimer);
    previewTimer = setTimeout(() => {
        previewTimer = null;
        refreshMarkdownPreview().catch(() => undefined);
    }, PREVIEW_DEBOUNCE_MS);
}

export async function refreshMarkdownPreview({ allowDuringGitSync = false } = {}) {
    if ((gitSyncInProgress && !allowDuringGitSync) || !currentPath || currentPath === deletingPath) {
        UI.clearMarkdownPreview();
        return;
    }

    if (previewTimer) {
        clearTimeout(previewTimer);
        previewTimer = null;
    }
    if (previewController) previewController.abort();

    const requestPath = currentPath;
    const revision = ++previewRevision;
    previewController = new AbortController();
    const payload = {
        path: requestPath,
        body: document.getElementById('editor')?.value || "",
        frontmatter: UI.collectFrontMatter()
    };
    UI.showMarkdownPreviewLoading();

    try {
        const data = await API.renderMarkdownPreview(payload, previewController.signal);
        if (revision !== previewRevision || requestPath !== currentPath) return;
        UI.renderMarkdownPreview(typeof data?.html === 'string' ? data.html : "");
    } catch (e) {
        if (e?.name === 'AbortError' || revision !== previewRevision) return;
        UI.showMarkdownPreviewError(e);
        throw e;
    } finally {
        if (revision === previewRevision) previewController = null;
    }
}

function cancelLocalPreviewTimer() {
    if (localPreviewTimer) {
        clearTimeout(localPreviewTimer);
        localPreviewTimer = null;
    }
}

function resetLocalPreviewClientState() {
    localPreviewRevision = 0;
}

function scheduleLocalLivePreview() {
    if (gitSyncInProgress || !localPreviewEnabled() || !currentPath || currentPath === deletingPath) return;
    if (localPreviewTimer) clearTimeout(localPreviewTimer);
    localPreviewTimer = setTimeout(() => {
        localPreviewTimer = null;
        refreshLocalLivePreview().catch(() => undefined);
    }, LOCAL_PREVIEW_DEBOUNCE_MS);
}

export async function refreshLocalLivePreview() {
    if (gitSyncInProgress || !localPreviewEnabled() || !currentPath || currentPath === deletingPath) return null;
    cancelLocalPreviewTimer();

    const requestPath = currentPath;
    const articleGeneration = articleLoadGeneration;
    const revision = ++localPreviewRevision;
    const payload = getPayload();
    const frontMatterKey = JSON.stringify(payload.frontmatter ?? null);

    const request = API.updateLocalPreviewContent(payload, revision);
    localPreviewInflight.add(request);
    try {
        const result = await request;
        if (
            requestPath !== currentPath ||
            articleGeneration !== articleLoadGeneration ||
            revision !== localPreviewRevision
        ) return null;
        if (typeof window.refreshLocalPreviewArticleURL === 'function') {
            window.refreshLocalPreviewArticleURL(result, frontMatterKey).catch(() => undefined);
        }
        return result;
    } catch (e) {
        console.error("[LocalPreview] Update failed:", e);
        throw e;
    } finally {
        localPreviewInflight.delete(request);
    }
}

// Destructive article/site operations must wait for preview writes that have
// already been sent. The site-scoped workspace stays alive across deletion,
// so a late update must not recreate a removed article.
export function waitForLocalPreviewUpdates(pending = localPreviewInflight) {
    return Promise.allSettled(Array.from(pending));
}

// Stop is destructive for the site-scoped runtime. Cancel delayed writes and
// drain requests already sent before asking the server to stop and detach it.
export async function prepareLocalLivePreviewStop() {
    cancelLocalPreviewTimer();
    await waitForLocalPreviewUpdates();
    resetLocalPreviewClientState();
}

// Git Sync updates the production repository tree. Pause every editor write
// source before it starts so an old autosave or preview request cannot race
// the pull and recreate the pre-sync state afterwards.
export async function prepareForGitSync() {
    if (gitSyncInProgress) return;
    gitSyncInProgress = true;
    setEditorWritePaused(true);
    clearAutoSaveTimer();
    cancelMarkdownPreview();
    cancelLocalPreviewTimer();
    await saveQueue.catch(() => undefined);
    await waitForLocalPreviewUpdates();
    while (gitMutationInflight.size > 0) {
        await Promise.allSettled(Array.from(gitMutationInflight));
    }
    resetLocalPreviewClientState();
}

export function finishForGitSync() {
    gitSyncInProgress = false;
    setEditorWritePaused(false);
}

export function isGitSyncInProgress() {
    return gitSyncInProgress;
}

// Article switching cancels the debounce timer, so explicitly send the
// current editor payload after the previous preview writes have settled.
// Keeping the pending set shared lets the final wait include this flush too.
export async function flushLocalPreviewBeforeArticleSwitch(flush = refreshLocalLivePreview, pending = localPreviewInflight) {
    await waitForLocalPreviewUpdates(pending);
    await flush();
    await waitForLocalPreviewUpdates(pending);
}

export async function execAutoSave() {
    if (gitSyncInProgress) return false;
    if (articleRevisionConflictPath === currentPath) return false;
    return queueCurrentSave("Auto Saving...");
}

async function queueCurrentSave(statusMessage) {
    while (currentPath && currentPath !== deletingPath) {
        assertGitSyncWritesAllowed();
        // Another payload may become the saved value while we wait. Read the
        // editor again afterwards so preview/publish always uses what is
        // currently visible, including a revert to an older payload.
        if (lastQueuedPayload !== "") {
            await saveQueue;
            continue;
        }

        const payloadObj = getPayload();
        const payloadStr = serializePayload(payloadObj);

        if (payloadStr === lastSavedPayload) {
            return false;
        }

        lastQueuedPayload = payloadStr;
        const operation = saveQueue.then(async () => {
            updateSaveStatus(statusMessage, "saving");
            try {
                const response = await API.saveArticle(payloadObj);
                if (currentData?.path === payloadObj.path && response?.revision) {
                    currentData.revision = response.revision;
                }
                if (serializePayload(getPayload()) === payloadStr) {
                    lastSavedPayload = serializePayload(getPayload());
                }
                console.log("[AutoSave] Saved:", payloadObj.path);
                updateSaveStatus("Saved", "saved");
                return true;
            } catch (e) {
                console.error("[AutoSave] Failed:", e);
                if (isArticleRevisionConflict(e)) {
                    markArticleRevisionConflict();
                } else {
                    updateSaveStatus("Save Failed", "error");
                }
                throw e;
            } finally {
                if (lastQueuedPayload === payloadStr) {
                    lastQueuedPayload = "";
                }
            }
        });

        // Return the rejecting operation to its caller, but keep the queue tail
        // fulfilled so an old failure cannot block a later no-op, retry,
        // preview, or publish.
        saveQueue = operation.catch(() => undefined);
        return operation;
    }
    return false;
}

export async function flushPendingSave() {
    assertGitSyncWritesAllowed();
    clearAutoSaveTimer();
    if (currentPath && currentPath === deletingPath) {
        throw new Error("Article deletion is in progress");
    }
    await queueCurrentSave("Saving before publish...");
}

export async function loadFile(path, { allowDuringGitSync = false } = {}) {
    if (gitSyncInProgress && !allowDuringGitSync) return;
    const request = beginArticleLoad();
    setEditorWritePaused(true);
    const activePathAtStart = currentPath;
    try {
        clearAutoSaveTimer();
        cancelMarkdownPreview();
        cancelLocalPreviewTimer();
        const switchingArticle = Boolean(currentPath && currentPath !== path);
        if (switchingArticle) {
            try {
                await queueCurrentSave("Saving before article switch...");
            } catch (e) {
                if (isCurrentArticleLoad(request)) {
                    UI.showToast("Failed to prepare article before switching: " + e.message, "error");
                }
                return;
            }
            if (!isCurrentArticleLoad(request)) return;
            try {
                await flushLocalPreviewBeforeArticleSwitch(() => {
                    if (!isCurrentArticleLoad(request)) return null;
                    return refreshLocalLivePreview();
                });
            } catch (e) {
                if (!isCurrentArticleLoad(request)) return;
                console.warn("[LocalPreview] Failed to flush before article switch", e);
                UI.showToast("Local Previewの同期に失敗しました。記事切替は続行します。", "warning");
            }
        }
        await saveQueue.catch(() => {
            // Loading another file remains possible after a failed save.
        });
        if (!isCurrentArticleLoad(request)) return;

        // Keep the active path/editor payload unchanged until the new article
        // has been fetched. This keeps a second click from saving Loading... or
        // the previous article's editor state under the requested path.
        const data = await API.fetchArticle(path, request.controller.signal);
        if (!isCurrentArticleLoad(request)) return;

        currentPath = path;
        currentData = data;
        articleRevisionConflictPath = "";
        resetLocalPreviewClientState();
        const display = document.getElementById('filename-display');
        if (display) display.textContent = path;
        UI.updateEditorContent(data, path, cmsConfig);
        setEditorWritePaused(gitSyncInProgress);

        lastSavedPayload = serializePayload(getPayload());
        lastQueuedPayload = "";
        await refreshMarkdownPreview({ allowDuringGitSync });

    } catch (e) {
        if (!isCurrentArticleLoad(request) || e?.name === 'AbortError') return;
        if (activePathAtStart) {
            UI.showToast("Failed to load file: " + e.message, "error");
        } else {
            UI.showEditorError(e);
            UI.showToast("Failed to load file: " + e.message, "error");
        }
    } finally {
        if (isCurrentArticleLoad(request)) setEditorWritePaused(gitSyncInProgress);
        finishArticleLoad(request);
    }
}

function getPayload() {
    if (currentData?.path === currentPath && typeof currentData.raw_content === 'string' && currentData.raw_content !== '') {
        return {
            path: currentPath,
            content: currentData.raw_content,
            base_revision: currentData.revision || "",
        };
    }
    const payload = {
        path: currentPath,
        base_revision: currentData?.path === currentPath ? currentData.revision || "" : "",
    };
    const fm = UI.collectFrontMatter();
    if (fm) {
        payload.frontmatter = fm;
        payload.body = document.getElementById('editor').value;
        payload.format = currentData.format || 'yaml';
    } else {
        payload.content = document.getElementById('editor').value;
    }
    return payload;
}

export async function saveFile() {
    if (gitSyncInProgress) {
        return UI.showToast(GIT_SYNC_WRITE_PAUSED_MESSAGE, "warning");
    }
    if (!currentPath) return UI.showToast("No file selected", "warning");
    if (currentPath === deletingPath) {
        return UI.showToast("Article deletion is in progress", "warning");
    }
    if (articleRevisionConflictPath === currentPath) {
        return UI.showToast(ARTICLE_REVISION_CONFLICT_MESSAGE, "warning");
    }

    clearAutoSaveTimer();

    try {
        await queueCurrentSave("Saving...");
        UI.showToast("File saved successfully", "success");
    } catch (e) {
        if (!isArticleRevisionConflict(e)) {
            UI.showToast("Error saving: " + e.message, "error");
        }
    }
}

export async function deleteFile(refreshListCb) {
    if (gitSyncInProgress) {
        return UI.showToast(GIT_SYNC_WRITE_PAUSED_MESSAGE, "warning");
    }
    if (!currentPath) return UI.showToast("No file selected", "warning");
    if (currentPath === deletingPath) {
        return UI.showToast("Article deletion is already in progress", "warning");
    }

    if (!confirm("Are you sure you want to delete this article?\nThis action cannot be undone.")) return;

    const pathToDelete = currentPath;
    let deleted = false;
    clearAutoSaveTimer();
    cancelLocalPreviewTimer();
    deletingPath = pathToDelete;

    try {
        // Let a save that already reached the server finish, then prevent all
        // queued saves for this path from starting before DELETE.
        await saveQueue;
        // The site-scoped workspace remains alive after article deletion. Wait
        // for already-sent preview updates before removing the production and
        // shadow files, otherwise a late update could recreate the deleted
        // article in the resident workspace.
        await waitForLocalPreviewUpdates();
        assertGitSyncWritesAllowed();
        const deleteResponse = await runGitMutation(() => API.deleteArticle(pathToDelete, currentData?.revision || ""));
        // Production deletion is committed at this point. The server removes
        // the corresponding file from the site-scoped preview workspace while
        // keeping the generator runtime alive for the next article.
        deleted = true;
        if (deleteResponse?.local_preview_sync === false) {
            UI.showToast("Article deleted, but Local Live Preview sync failed", "warning");
        } else {
            UI.showToast("Article deleted", "success");
        }

        if (currentPath === pathToDelete) {
            cancelMarkdownPreview();
            currentPath = "";
            currentData = null;
            resetLocalPreviewClientState();
            lastSavedPayload = "";
            lastQueuedPayload = "";
            document.getElementById('filename-display').textContent = "Select a file...";
            document.getElementById('editor').value = "";
            document.getElementById('fm-container').style.display = 'none';
            UI.clearMarkdownPreview();
        }

        if (refreshListCb) await refreshListCb();
    } catch (e) {
        if (deleted) {
            UI.showToast("Article deleted, but refreshing the editor failed: " + e.message, "warning");
        } else if (isArticleRevisionConflict(e)) {
            markArticleRevisionConflict();
        } else {
            UI.showToast("Delete failed: " + e.message, "error");
        }
    } finally {
        if (deletingPath === pathToDelete) {
            deletingPath = "";
        }
        if (!deleted && currentPath === pathToDelete) {
            handleEditorChange();
        }
    }
}

export async function createNewFile(refreshListCb) {
    if (gitSyncInProgress) {
        return UI.showToast(GIT_SYNC_WRITE_PAUSED_MESSAGE, "warning");
    }
    if (!cmsConfig) {
        UI.showToast("Config not loaded", "error");
        return;
    }

    UI.showCreationModal(cmsConfig, async (colName, fields) => {
        try {
            assertGitSyncWritesAllowed();
            const res = await runGitMutation(() => API.createArticle({
                collection: colName,
                fields: fields
            }));

            if (res.status === 'created') {
                if (refreshListCb) await refreshListCb();
                if (res.path) {
                    await loadFile(res.path);
                    await refreshLocalLivePreview();
                    UI.showToast("File created successfully", "success");
                }
            }
        } catch (e) {
            UI.showToast("Create failed: " + e.message, "error");
        }
    });
}

export async function resetChanges() {
    if (!currentPath) return;
    if (!confirm("Are you sure you want to discard all changes?")) return;
    await loadFile(currentPath);
    await refreshLocalLivePreview();
    UI.showToast("Changes discarded", "info");
}

export function insertText(text) {
    if (gitSyncInProgress) return;
    const editor = document.getElementById('editor');
    if (!editor) return;

    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const val = editor.value;

    editor.value = val.substring(0, start) + text + val.substring(end);
    editor.selectionStart = editor.selectionEnd = start + text.length;
    editor.focus();
    
    // Trigger input event for auto-save
    editor.dispatchEvent(new Event('input'));
}

export async function showDiff() {
    if (!currentPath) return;
    const payload = getPayload();
    try {
        const data = await API.getDiff(payload);
        UI.showDiffModal(data.diff);
    } catch (e) {
        UI.showToast("Failed to get diff: " + e.message, "error");
    }
}
