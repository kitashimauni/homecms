export const LOCAL_PREVIEW_FRAME_TIMEOUT_MS = 10000;
export const LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS = 3;

const INITIAL_NAVIGATION_RETRY_DELAYS_MS = [250, 750];

const DEFAULT_TIMEOUT_MESSAGE = 'previewの読み込みを確認できません。新規タブで開いてください。';

export function shouldCloseEmbeddedLocalPreview({ hasCurrentPath } = {}) {
    return !hasCurrentPath;
}

export function shouldAutoShowEmbeddedLocalPreview({ status, hasCurrentPath, dismissed } = {}) {
    return hasCurrentPath && !dismissed && (status === 'starting' || status === 'ready');
}

export function shouldRetryLocalPreviewNavigation({ error, attempt } = {}) {
    if (!Number.isInteger(attempt) || attempt >= LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS) return false;
    const status = error?.status;
    return status === undefined || status === 408 || status === 425 || status === 429 || status >= 500;
}

export function shouldReloadEmbeddedLocalPreviewAfterFresh({
    cachedURL,
    freshURL,
    cachedFresh = false,
    cachedInvalidationGeneration = 0,
    cachedActiveBuildGeneration = 0,
    freshInvalidationGeneration = 0,
    freshActiveBuildGeneration = 0,
} = {}) {
    if (!cachedURL || !freshURL || cachedFresh) return false;
    if (freshURL !== cachedURL) return true;

    const generations = [
        cachedInvalidationGeneration,
        cachedActiveBuildGeneration,
        freshInvalidationGeneration,
        freshActiveBuildGeneration,
    ].map(Number);
    if (!generations.some(value => value > 0)) return true;
    const [cachedInvalidation, cachedActive, freshInvalidation, freshActive] = generations;
    if (freshActive <= cachedActive) return false;
    if (freshActive < freshInvalidation) return false;
    return freshInvalidation >= cachedInvalidation;
}

export function localPreviewFreshReloadKey({
    siteID = "",
    path = "",
    url = "",
    invalidationGeneration = 0,
    activeBuildGeneration = 0,
} = {}) {
    return [siteID, path, url, invalidationGeneration, activeBuildGeneration].join("\u0000");
}

export function isLocalPreviewBuildCoveredByLiveReload({
    cachedURL,
    freshURL,
    siteID,
    path,
    siteGeneration,
    cachedActiveBuildGeneration = 0,
    freshActiveBuildGeneration = 0,
    liveReload,
} = {}) {
    if (!liveReload || freshURL !== cachedURL) return false;
    return liveReload.siteID === siteID &&
        liveReload.path === path &&
        liveReload.siteGeneration === siteGeneration &&
        Number(liveReload.activeBuildGeneration) >= Number(freshActiveBuildGeneration) &&
        Number(liveReload.activeBuildGeneration) > Number(cachedActiveBuildGeneration);
}

export function localPreviewNavigationRetryDelay(attempt) {
    return INITIAL_NAVIGATION_RETRY_DELAYS_MS[Math.max(0, attempt - 1)] || 1000;
}

export function shouldUseLocalPreviewSplitDefault({ enabled, narrowViewport } = {}) {
    return enabled === true && narrowViewport !== true;
}

export function createLocalPreviewFrameController({
    getURL,
    wrapper,
    frame,
    button,
    loading,
    error,
    errorMessage,
    setTimeoutFn = globalThis.setTimeout,
    clearTimeoutFn = globalThis.clearTimeout,
    timeoutMs = LOCAL_PREVIEW_FRAME_TIMEOUT_MS,
    timeoutMessage = DEFAULT_TIMEOUT_MESSAGE,
} = {}) {
    let loadTimer = null;
    let dismissed = false;

    function clearLoadTimer() {
        if (loadTimer !== null) {
            clearTimeoutFn(loadTimer);
            loadTimer = null;
        }
    }

    function isVisible() {
        return Boolean(wrapper && !wrapper.classList.contains('hidden'));
    }

    function showError(message) {
        clearLoadTimer();
        if (loading) loading.classList.add('hidden');
        if (errorMessage && message) errorMessage.textContent = message;
        if (error) error.classList.remove('hidden');
    }

    function markReady() {
        clearLoadTimer();
        if (loading) loading.classList.add('hidden');
        if (error) error.classList.add('hidden');
    }

    function show({ reload = false } = {}) {
        const url = typeof getURL === 'function' ? getURL() : '';
        if (!wrapper || !frame || !url) return false;

        const wasVisible = isVisible();
        const currentSrc = typeof frame.getAttribute === 'function'
            ? frame.getAttribute('src')
            : frame.src;
        const shouldLoad = reload || currentSrc !== url;

        wrapper.classList.remove('hidden');
        if (button) button.textContent = '埋め込みを閉じる';

        if (!wasVisible || shouldLoad) {
            if (loading) loading.classList.remove('hidden');
            if (error) error.classList.add('hidden');
            clearLoadTimer();
            if (shouldLoad) frame.src = url;
            loadTimer = setTimeoutFn(() => showError(timeoutMessage), timeoutMs);
        }
        return true;
    }

    function close({ dismiss = false } = {}) {
        clearLoadTimer();
        if (dismiss) dismissed = true;
        if (wrapper) wrapper.classList.add('hidden');
        if (frame) frame.src = 'about:blank';
        if (button) button.textContent = '埋め込み表示';
        if (loading) loading.classList.add('hidden');
        if (error) error.classList.add('hidden');
    }

    return {
        clearLoadTimer,
        close,
        handleError: (message) => showError(message || 'preview ingressから応答を確認できませんでした。'),
        handleLoad: markReady,
        handleReady: markReady,
        isDismissed: () => dismissed,
        isVisible,
        resetDismissed: () => { dismissed = false; },
        show,
        showError,
    };
}
