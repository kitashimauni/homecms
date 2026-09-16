import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createClassList } from "./test_helpers/browser.mjs";

const {
    createLocalPreviewFrameController,
    isLocalPreviewBuildCoveredByLiveReload,
    LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS,
    localPreviewNavigationRetryDelay,
    localPreviewFreshReloadKey,
    shouldReloadEmbeddedLocalPreviewAfterFresh,
    shouldAutoShowEmbeddedLocalPreview,
    shouldAutoExpandLocalPreviewDetails,
    shouldCloseEmbeddedLocalPreview,
    shouldRetryLocalPreviewNavigation,
    shouldUseLocalPreviewSplitDefault,
} = await import("./local_preview.js");

function createPreviewFrameHarness() {
    const wrapper = { classList: createClassList("hidden") };
    const frame = { src: "about:blank", getAttribute(name) { return name === "src" ? this.src : null; } };
    const button = { textContent: "埋め込み表示" };
    const loading = { classList: createClassList("hidden") };
    const error = { classList: createClassList("hidden") };
    const errorMessage = { textContent: "" };
    let timerCallback = null;
    const controller = createLocalPreviewFrameController({
        getURL: () => "https://preview.example.test/",
        wrapper,
        frame,
        button,
        loading,
        error,
        errorMessage,
        setTimeoutFn(callback) { timerCallback = callback; return "preview-timer"; },
        clearTimeoutFn() { timerCallback = null; },
    });
    return { controller, frame, button, loading, error, errorMessage, triggerTimeout: () => timerCallback?.() };
}

describe("Local Preview state transitions", () => {
    it("closes only when the selected article is missing", () => {
        assert.equal(shouldCloseEmbeddedLocalPreview({ status: "ready", hasCurrentPath: false }), true);
        assert.equal(shouldCloseEmbeddedLocalPreview({ status: "ready", hasCurrentPath: true }), false);
    });

    it("auto-shows ready runtimes unless the user dismissed them", () => {
        assert.equal(shouldAutoShowEmbeddedLocalPreview({ status: "starting", hasCurrentPath: true, dismissed: false }), true);
        assert.equal(shouldAutoShowEmbeddedLocalPreview({ status: "ready", hasCurrentPath: true, dismissed: true }), false);
        assert.equal(shouldAutoShowEmbeddedLocalPreview({ status: "stopped", hasCurrentPath: true, dismissed: false }), false);
    });

    it("only auto-expands details when the runtime enters a failed state", () => {
        assert.equal(shouldAutoExpandLocalPreviewDetails({ status: "failed", previousStatus: "ready" }), true);
        assert.equal(shouldAutoExpandLocalPreviewDetails({ status: "failed", previousStatus: "failed" }), false);
        assert.equal(shouldAutoExpandLocalPreviewDetails({ status: "ready", previousStatus: "failed" }), false);
    });

    it("bounds transient navigation retries and keeps Split desktop-only", () => {
        assert.equal(LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS, 3);
        assert.equal(shouldRetryLocalPreviewNavigation({ error: new TypeError("network"), attempt: 1 }), true);
        assert.equal(shouldRetryLocalPreviewNavigation({ error: { status: 409 }, attempt: 1 }), false);
        assert.equal(localPreviewNavigationRetryDelay(1), 250);
        assert.equal(localPreviewNavigationRetryDelay(2), 750);
        assert.equal(shouldUseLocalPreviewSplitDefault({ enabled: true, narrowViewport: false }), true);
        assert.equal(shouldUseLocalPreviewSplitDefault({ enabled: true, narrowViewport: true }), false);
    });

    it("reloads same-URL stale previews only after a newer build generation", () => {
        const stale = {
            cachedURL: "https://preview.example.test/posts/one/",
            cachedFresh: false,
            cachedInvalidationGeneration: 12,
            cachedActiveBuildGeneration: 11,
        };
        assert.equal(shouldReloadEmbeddedLocalPreviewAfterFresh({
            ...stale,
            freshURL: stale.cachedURL,
            freshInvalidationGeneration: 12,
            freshActiveBuildGeneration: 12,
        }), true);
        assert.equal(shouldReloadEmbeddedLocalPreviewAfterFresh({
            ...stale,
            freshURL: stale.cachedURL,
            freshInvalidationGeneration: 12,
            freshActiveBuildGeneration: 11,
        }), false);
        assert.equal(shouldReloadEmbeddedLocalPreviewAfterFresh({
            ...stale,
            freshURL: "https://preview.example.test/posts/changed/",
            freshInvalidationGeneration: 12,
            freshActiveBuildGeneration: 11,
        }), true);
    });

    it("does not reload a fresh preview or repeat a fresh-generation key", () => {
        assert.equal(shouldReloadEmbeddedLocalPreviewAfterFresh({
            cachedURL: "https://preview.example.test/posts/one/",
            freshURL: "https://preview.example.test/posts/one/",
            cachedFresh: true,
            cachedInvalidationGeneration: 12,
            cachedActiveBuildGeneration: 12,
            freshInvalidationGeneration: 12,
            freshActiveBuildGeneration: 12,
        }), false);
        assert.equal(
            localPreviewFreshReloadKey({ siteID: "daily-blog", path: "posts/one.md", url: "https://preview.example.test/posts/one/", invalidationGeneration: 12, activeBuildGeneration: 12 }),
            localPreviewFreshReloadKey({ siteID: "daily-blog", path: "posts/one.md", url: "https://preview.example.test/posts/one/", invalidationGeneration: 12, activeBuildGeneration: 12 }),
        );
        assert.notEqual(
            localPreviewFreshReloadKey({ siteID: "daily-blog", path: "posts/one.md", url: "https://preview.example.test/posts/one/", invalidationGeneration: 12, activeBuildGeneration: 12 }),
            localPreviewFreshReloadKey({ siteID: "daily-blog", path: "posts/one.md", url: "https://preview.example.test/posts/one/", invalidationGeneration: 13, activeBuildGeneration: 13 }),
        );
        const liveReload = { siteID: "daily-blog", path: "posts/one.md", siteGeneration: 4, activeBuildGeneration: 12 };
        assert.equal(isLocalPreviewBuildCoveredByLiveReload({
            cachedURL: "https://preview.example.test/posts/one/",
            freshURL: "https://preview.example.test/posts/one/",
            siteID: "daily-blog",
            path: "posts/one.md",
            siteGeneration: 4,
            cachedActiveBuildGeneration: 11,
            freshActiveBuildGeneration: 12,
            liveReload,
        }), true);
        assert.equal(isLocalPreviewBuildCoveredByLiveReload({
            cachedURL: "https://preview.example.test/posts/one/",
            freshURL: "https://preview.example.test/posts/changed/",
            siteID: "daily-blog",
            path: "posts/one.md",
            siteGeneration: 4,
            cachedActiveBuildGeneration: 11,
            freshActiveBuildGeneration: 12,
            liveReload,
        }), false);
    });

    it("transitions the embedded frame through loading and fallback states", () => {
        const harness = createPreviewFrameHarness();
        assert.equal(harness.controller.show(), true);
        assert.equal(harness.frame.src, "https://preview.example.test/");
        assert.equal(harness.loading.classList.contains("hidden"), false);
        harness.controller.handleReady();
        assert.equal(harness.loading.classList.contains("hidden"), true);
        harness.controller.show({ reload: true });
        harness.triggerTimeout();
        assert.equal(harness.error.classList.contains("hidden"), false);
        assert.match(harness.errorMessage.textContent, /確認できません/);
    });
});
