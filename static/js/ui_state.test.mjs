import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createClassList, installTestWindow } from "./test_helpers/browser.mjs";

const restoreWindow = installTestWindow();
const {
    normalizeDeploymentState,
    normalizeLocalPreviewState,
    safeExternalURL,
    switchView,
} = await import("./ui.js");

describe("UI state normalization", () => {
    it("accepts only absolute HTTP(S) links", () => {
        assert.equal(safeExternalURL("https://preview.example.test/build/1"), "https://preview.example.test/build/1");
        assert.equal(safeExternalURL("javascript:alert(1)"), "");
        assert.equal(safeExternalURL("/admin"), "");
        assert.equal(safeExternalURL("//evil.example.test"), "");
    });

    it("normalizes deployment aliases and rejects unknown ready states", () => {
        assert.deepEqual(normalizeDeploymentState({
            state: "READY",
            commit: "0123456789abcdef",
            deployment_url: "https://preview.example.test/commit",
            log_url: "javascript:alert(1)",
        }), {
            state: "READY",
            commit: "0123456789abcdef",
            deployment_url: "https://preview.example.test/commit",
            log_url: "",
            status: "ready",
            commit_sha: "0123456789abcdef",
            url: "https://preview.example.test/commit",
        });
        assert.equal(normalizeDeploymentState({ status: "unexpected" }).status, "queued");
        assert.equal(normalizeDeploymentState(null), null);
    });

    it("does not restore browser ownership fields to runtime state", () => {
        const state = normalizeLocalPreviewState({ status: "ready", process_state: "ready", workspace_active: true });
        assert.equal(state.status, "ready");
        assert.equal(state.workspace_active, true);
        assert.equal("session_owned" in state, false);
    });
});

describe("view surface integration", () => {
    function createViewHarness({ localPreviewEnabled = false } = {}) {
        const makeElement = id => ({ id, classList: createClassList(), dataset: {}, style: { display: "" } });
        const contentArea = makeElement("content-area");
        if (localPreviewEnabled) contentArea.classList.add("local-preview-enabled");
        const elements = new Map([
            ["content-area", contentArea],
            ["edit-view", makeElement("edit-view")],
            ["preview-view", makeElement("preview-view")],
            ["local-preview-view", makeElement("local-preview-view")],
            ["btn-view-edit", makeElement("btn-view-edit")],
            ["btn-view-preview", makeElement("btn-view-preview")],
            ["btn-view-split", makeElement("btn-view-split")],
        ]);
        const previousDocument = globalThis.document;
        globalThis.document = {
            getElementById(id) { return elements.get(id); },
            querySelectorAll: () => [elements.get("btn-view-edit"), elements.get("btn-view-preview"), elements.get("btn-view-split")],
        };
        return {
            contentArea,
            editView: elements.get("edit-view"),
            previewView: elements.get("preview-view"),
            localPreviewView: elements.get("local-preview-view"),
            restore() { globalThis.document = previousDocument; },
        };
    }

    it("uses Local Live Preview for Preview and Split when enabled", () => {
        const harness = createViewHarness({ localPreviewEnabled: true });
        try {
            switchView("preview");
            assert.equal(harness.localPreviewView.style.display, "flex");
            assert.equal(harness.previewView.style.display, "none");
            switchView("split");
            assert.equal(harness.contentArea.classList.contains("split-mode"), true);
            assert.equal(harness.editView.style.display, "flex");
            assert.equal(harness.localPreviewView.style.display, "flex");
        } finally {
            harness.restore();
        }
    });

    it("keeps Markdown Preview when Local Live Preview is disabled", () => {
        const harness = createViewHarness();
        try {
            switchView("preview");
            assert.equal(harness.previewView.style.display, "block");
            assert.equal(harness.localPreviewView.style.display, "none");
        } finally {
            harness.restore();
        }
    });
});

restoreWindow();
