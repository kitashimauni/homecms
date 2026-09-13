import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createClassList } from "./test_helpers/browser.mjs";

const {
    createLocalPreviewFrameController,
    LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS,
    localPreviewNavigationRetryDelay,
    shouldAutoShowEmbeddedLocalPreview,
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

    it("bounds transient navigation retries and keeps Split desktop-only", () => {
        assert.equal(LOCAL_PREVIEW_INITIAL_NAVIGATION_MAX_ATTEMPTS, 3);
        assert.equal(shouldRetryLocalPreviewNavigation({ error: new TypeError("network"), attempt: 1 }), true);
        assert.equal(shouldRetryLocalPreviewNavigation({ error: { status: 409 }, attempt: 1 }), false);
        assert.equal(localPreviewNavigationRetryDelay(1), 250);
        assert.equal(localPreviewNavigationRetryDelay(2), 750);
        assert.equal(shouldUseLocalPreviewSplitDefault({ enabled: true, narrowViewport: false }), true);
        assert.equal(shouldUseLocalPreviewSplitDefault({ enabled: true, narrowViewport: true }), false);
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
