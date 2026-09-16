import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { installTestWindow } from "./test_helpers/browser.mjs";

installTestWindow();

const {
    clearEditor,
    execAutoSave,
    finishForGitSync,
    flushLocalPreviewBeforeArticleSwitch,
    flushPendingSave,
    getCurrentPath,
    hasUnsavedChanges,
    initAutoSave,
    isGitSyncInProgress,
    loadFile,
    prepareForGitSync,
    refreshLocalLivePreview,
    runGitMutation,
    setArticlePathChangeListener,
    setConfig,
    waitForLocalPreviewUpdates,
} = await import("./editor.js");

describe("Local Preview destructive operations", () => {
    it("waits for in-flight updates before deleting an article", async () => {
        let updateApplied = false;
        let resolveUpdate;
        const update = new Promise(resolve => {
            resolveUpdate = () => {
                updateApplied = true;
                resolve();
            };
        });

        const waiting = waitForLocalPreviewUpdates(new Set([update]));
        await Promise.resolve();
        assert.equal(updateApplied, false);

        resolveUpdate();
        await waiting;
        assert.equal(updateApplied, true);
    });

    it("flushes the latest preview payload before switching articles", async () => {
        let oldUpdateApplied = false;
        let resolveOldUpdate;
        const oldUpdate = new Promise(resolve => {
            resolveOldUpdate = () => {
                oldUpdateApplied = true;
                resolve();
            };
        });
        const pending = new Set([oldUpdate]);
        const events = [];
        const switching = flushLocalPreviewBeforeArticleSwitch(async () => {
            assert.equal(oldUpdateApplied, true);
            events.push("latest payload sent");
            const latestUpdate = Promise.resolve().then(() => events.push("latest payload applied"));
            pending.add(latestUpdate);
        }, pending);

        await Promise.resolve();
        assert.deepEqual(events, []);
        resolveOldUpdate();
        await switching;
        assert.deepEqual(events, ["latest payload sent", "latest payload applied"]);
    });

    it("does not block a switch when an in-flight preview update rejects", async () => {
        let rejectUpdate;
        const pending = new Set([new Promise((_, reject) => { rejectUpdate = reject; })]);
        const switching = flushLocalPreviewBeforeArticleSwitch(async () => "latest payload sent", pending);

        rejectUpdate(new Error("preview update failed"));
        await switching;
        assert.equal(getCurrentPath(), "");
    });

    it("notifies Publish visibility when an article is loaded or cleared", async () => {
        const harness = createArticleSwitchHarness();
        const changedPaths = [];
        setArticlePathChangeListener(path => changedPaths.push(path));
        try {
            await loadFile("posts/old.md");
            clearEditor();
            assert.deepEqual(changedPaths, ["posts/old.md", ""]);
        } finally {
            setArticlePathChangeListener(null);
            harness.restore();
        }
    });

    function createArticleSwitchHarness({ generator = "hugo", previewFailure = null, saveFailure = false, saveResponse = null, articleResponses = new Map(), markdownResponses = new Map(), localPreviewResponses = new Map() } = {}) {
        const previousDocument = globalThis.document;
        const previousFetch = globalThis.fetch;
        const previousRequestAnimationFrame = globalThis.requestAnimationFrame;
        const editor = { disabled: false, value: "", placeholder: "" };
        const frontMatterControl = { disabled: false };
        const fmContainer = {
            style: { display: "" },
            innerHTML: "",
            querySelectorAll() { return [frontMatterControl]; },
        };
        const markdownPreview = { replaceChildren() {}, innerHTML: "" };
        const markdownStatus = {
            textContent: "",
            className: "",
            removeAttribute() {},
            setAttribute() {},
        };
        const filename = { textContent: "" };
        const makeToastElement = () => ({
            id: "",
            className: "",
            textContent: "",
            innerHTML: "",
            style: {},
            parentElement: null,
            appendChild(child) {
                child.parentElement = this;
            },
            remove() {
                this.parentElement = null;
            },
        });
        const body = makeToastElement();
        const elements = new Map([
            ["editor", editor],
            ["fm-container", fmContainer],
            ["filename-display", filename],
            ["markdown-preview", markdownPreview],
            ["markdown-preview-status", markdownStatus],
        ]);
        const calls = [];

        globalThis.document = {
            getElementById(id) { return elements.get(id) || null; },
            querySelectorAll() { return []; },
            createElement: makeToastElement,
            body,
        };
        globalThis.requestAnimationFrame = callback => {
            callback();
            return 1;
        };
        globalThis.fetch = async (url, options = {}) => {
            const requestURL = String(url);
            calls.push({ url: requestURL, options });
            if (requestURL === "/admin/api/csrf-token") {
                return { ok: true, status: 200, json: async () => ({ csrf_token: "csrf" }) };
            }
            if (requestURL.includes("/admin/api/article?") && !options.method) {
                const path = new URL(requestURL, "http://localhost").searchParams.get("path");
                const responseOverride = articleResponses.get(path);
                if (responseOverride) return responseOverride();
                return {
                    ok: true,
                    status: 200,
                    json: async () => ({ path, content: path === "posts/old.md" ? "before" : "after" }),
                };
            }
            if (requestURL.includes("/admin/api/preview/markdown")) {
                const markdownPath = JSON.parse(options.body || "{}").path;
                const responseOverride = markdownResponses.get(markdownPath);
                if (responseOverride) return responseOverride();
                return { ok: true, status: 200, json: async () => ({ html: `<p>${markdownPath}</p>` }) };
            }
            if (requestURL.includes("/admin/api/preview/local") && options.method === "POST") {
                const localPreviewPath = JSON.parse(options.body || "{}").path;
                const responseOverride = localPreviewResponses.get(localPreviewPath);
                if (responseOverride) return responseOverride();
                if (previewFailure === "network") {
                    throw new Error("preview network failed");
                }
                if (previewFailure) {
                    return {
                        ok: false,
                        status: previewFailure,
                        json: async () => ({ message: `preview failed with ${previewFailure}` }),
                    };
                }
                return { ok: true, status: 200, json: async () => ({ status: "ok" }) };
            }
            if (requestURL.endsWith("/admin/api/article") && options.method === "POST") {
                if (saveResponse) return saveResponse();
                if (saveFailure) {
                    return { ok: false, status: 500, json: async () => ({}) };
                }
                return { ok: true, status: 200, json: async () => ({ status: "ok" }) };
            }
            throw new Error(`Unexpected request: ${requestURL}`);
        };

        clearEditor();
        setConfig({ _cms: { local_preview: { enabled: true, generator } } });
        return {
            editor,
            frontMatterControl,
            markdownPreview,
            calls,
            restore() {
                clearEditor();
                setConfig(null);
                globalThis.document = previousDocument;
                globalThis.fetch = previousFetch;
                globalThis.requestAnimationFrame = previousRequestAnimationFrame;
            },
        };
    }

    function deferred() {
        let resolve;
        let reject;
        const promise = new Promise((promiseResolve, promiseReject) => {
            resolve = promiseResolve;
            reject = promiseReject;
        });
        return { promise, resolve, reject };
    }

    for (const generator of ["hugo", "eleventy"]) {
        for (const previewFailure of [503, 500, "network"]) {
            it(`continues ${generator} article switching after Local Preview ${previewFailure} failure`, async () => {
                const harness = createArticleSwitchHarness({ generator, previewFailure });
                try {
                    await loadFile("posts/old.md");
                    harness.calls.length = 0;
                    harness.editor.value = "changed before switch";

                    await loadFile("posts/new.md");

                    assert.equal(getCurrentPath(), "posts/new.md");
                    const saveIndex = harness.calls.findIndex(call => call.url.endsWith("/admin/api/article") && call.options.method === "POST");
                    const previewIndex = harness.calls.findIndex(call => call.url.includes("/admin/api/preview/local"));
                    const newArticleIndex = harness.calls.findIndex(call => call.url.includes("/admin/api/article?") && call.url.includes("posts%2Fnew.md"));
                    assert.ok(saveIndex >= 0, "production save should complete before switching");
                    assert.ok(previewIndex > saveIndex, "Local Preview flush should follow the production save");
                    assert.ok(newArticleIndex > previewIndex, "the new article should load after the failed flush");
                } finally {
                    harness.restore();
                }
            });
        }
    }

    it("does not let an older Markdown Preview response overwrite the newest article", async () => {
        const markdownStarted = deferred();
        const markdownResult = deferred();
        const markdownResponses = new Map([
            ["posts/b.md", async () => {
                markdownStarted.resolve();
                return markdownResult.promise;
            }],
        ]);
        const harness = createArticleSwitchHarness({ markdownResponses });
        try {
            await loadFile("posts/a.md");
            const loadingB = loadFile("posts/b.md");
            await markdownStarted.promise;

            const loadingC = loadFile("posts/c.md");
            await loadingC;
            assert.equal(getCurrentPath(), "posts/c.md");
            assert.equal(harness.editor.value, "after");
            assert.equal(harness.markdownPreview.innerHTML, "<p>posts/c.md</p>");

            markdownResult.resolve({
                ok: true,
                status: 200,
                json: async () => ({ html: "<p>stale B preview</p>" }),
            });
            await loadingB;
            assert.equal(getCurrentPath(), "posts/c.md");
            assert.equal(harness.markdownPreview.innerHTML, "<p>posts/c.md</p>");
        } finally {
            harness.restore();
        }
    });

    it("does not apply an older Local Preview response after switching articles", async () => {
        const localPreviewStarted = deferred();
        const localPreviewResult = deferred();
        let localPreviewCalls = 0;
        const localPreviewResponses = new Map([
            ["posts/a.md", async () => {
                localPreviewCalls += 1;
                if (localPreviewCalls === 1) {
                    localPreviewStarted.resolve();
                    return localPreviewResult.promise;
                }
                return {
                    ok: true,
                    status: 200,
                    json: async () => ({ article_url: "valid A preview" }),
                };
            }],
        ]);
        const harness = createArticleSwitchHarness({ localPreviewResponses });
        const previousRefresh = window.refreshLocalPreviewArticleURL;
        const refreshedURLs = [];
        window.refreshLocalPreviewArticleURL = async result => {
            refreshedURLs.push(result.article_url);
        };
        try {
            await loadFile("posts/a.md");
            const refreshingA = refreshLocalLivePreview();
            await localPreviewStarted.promise;

            const loadingB = loadFile("posts/b.md");
            localPreviewResult.resolve({
                ok: true,
                status: 200,
                json: async () => ({ article_url: "stale A preview" }),
            });
            await loadingB;
            await refreshingA;

            assert.equal(getCurrentPath(), "posts/b.md");
            assert.deepEqual(refreshedURLs, ["valid A preview"]);
        } finally {
            window.refreshLocalPreviewArticleURL = previousRefresh;
            harness.restore();
        }
    });

    it("pauses editor and front matter writes during the entire article switch", async () => {
        const saveStarted = deferred();
        const saveResult = deferred();
        const localPreviewStarted = deferred();
        const localPreviewResult = deferred();
        const localPreviewResponses = new Map([
            ["posts/a.md", async () => {
                localPreviewStarted.resolve();
                return localPreviewResult.promise;
            }],
        ]);
        const harness = createArticleSwitchHarness({
            saveResponse: () => {
                saveStarted.resolve();
                return saveResult.promise;
            },
            localPreviewResponses,
        });
        try {
            await loadFile("posts/a.md");
            harness.editor.value = "draft A";

            const loadingB = loadFile("posts/b.md");
            await saveStarted.promise;
            assert.equal(harness.editor.disabled, true);
            assert.equal(harness.frontMatterControl.disabled, true);

            saveResult.resolve({
                ok: true,
                status: 200,
                json: async () => ({ status: "ok" }),
            });
            await localPreviewStarted.promise;
            assert.equal(harness.editor.disabled, true);
            assert.equal(harness.frontMatterControl.disabled, true);

            localPreviewResult.resolve({
                ok: true,
                status: 200,
                json: async () => ({ status: "ok" }),
            });
            await loadingB;
            assert.equal(getCurrentPath(), "posts/b.md");
            assert.equal(harness.editor.disabled, false);
            assert.equal(harness.frontMatterControl.disabled, false);
        } finally {
            harness.restore();
        }
    });

    it("keeps the article unchanged when production save fails", async () => {
        const harness = createArticleSwitchHarness({ saveFailure: true });
        try {
            await loadFile("posts/old.md");
            harness.calls.length = 0;
            harness.editor.value = "unsaved production change";

            await loadFile("posts/new.md");

            assert.equal(getCurrentPath(), "posts/old.md");
            assert.equal(harness.calls.some(call => call.url.includes("/admin/api/preview/local")), false);
            assert.equal(harness.calls.some(call => call.url.includes("posts%2Fnew.md")), false);
        } finally {
            harness.restore();
        }
    });

    it("stops autosave after a production revision conflict without retrying", async () => {
        const harness = createArticleSwitchHarness({
            saveResponse: () => ({
                ok: false,
                status: 409,
                json: async () => ({
                    code: "CONFLICT",
                    message: "Article was changed externally; reload before saving",
                    current_revision: "sha256:remote",
                }),
            }),
        });
        try {
            await loadFile("posts/a.md");
            harness.calls.length = 0;
            harness.editor.value = "stale editor content";

            await assert.rejects(execAutoSave(), error => error.status === 409);
            assert.equal(hasUnsavedChanges(), true);
            assert.equal(harness.calls.filter(call => call.url.endsWith("/admin/api/article") && call.options.method === "POST").length, 1);

            assert.equal(await execAutoSave(), false);
            assert.equal(harness.calls.filter(call => call.url.endsWith("/admin/api/article") && call.options.method === "POST").length, 1);
        } finally {
            harness.restore();
        }
    });

    for (const staleResult of ["success", "failure"]) {
        it(`keeps the newest article when an older load finishes ${staleResult} later`, async () => {
            const bFetchStarted = deferred();
            const bFetchResult = deferred();
            const articleResponses = new Map([
                ["posts/b.md", async () => {
                    bFetchStarted.resolve();
                    return bFetchResult.promise;
                }],
            ]);
            const harness = createArticleSwitchHarness({ articleResponses });
            try {
                await loadFile("posts/a.md");
                harness.calls.length = 0;
                harness.editor.value = "draft A";

                const loadingB = loadFile("posts/b.md");
                await bFetchStarted.promise;
                assert.equal(getCurrentPath(), "posts/a.md");

                const bRequest = harness.calls.find(call => call.url.includes("posts%2Fb.md"));
                assert.ok(bRequest?.options.signal, "article fetch should receive an AbortSignal");

                const loadingC = loadFile("posts/c.md");
                await loadingC;
                assert.equal(getCurrentPath(), "posts/c.md");
                assert.equal(harness.editor.value, "after");
                assert.equal(bRequest.options.signal.aborted, true);

                const saves = harness.calls
                    .filter(call => call.url.endsWith("/admin/api/article") && call.options.method === "POST")
                    .map(call => JSON.parse(call.options.body));
                assert.ok(saves.length >= 1, "the active article should be saved before navigation");
                assert.ok(saves.every(payload => payload.path === "posts/a.md"), "navigation must not save under the pending B path");
                assert.ok(saves.some(payload => payload.path === "posts/a.md" && payload.body === "draft A"), JSON.stringify(saves));

                if (staleResult === "success") {
                    bFetchResult.resolve({
                        ok: true,
                        status: 200,
                        json: async () => ({ path: "posts/b.md", content: "stale B" }),
                    });
                } else {
                    bFetchResult.reject(new Error("stale B fetch failed"));
                }
                await loadingB;

                assert.equal(getCurrentPath(), "posts/c.md");
                assert.equal(harness.editor.value, "after");
            } finally {
                harness.restore();
            }
        });
    }

});
describe("Git Sync editor gate", () => {
    it("waits for an AutoSave already in flight before allowing Sync to continue", async () => {
        const previousDocument = globalThis.document;
        const previousFetch = globalThis.fetch;
        const previousRequestAnimationFrame = globalThis.requestAnimationFrame;
        const editor = { disabled: false, value: "", placeholder: "" };
        const fmContainer = {
            style: { display: "" },
            innerHTML: "",
            querySelectorAll() { return []; },
        };
        const preview = { replaceChildren() {}, querySelectorAll() { return []; } };
        const previewStatus = {
            textContent: "",
            className: "",
            removeAttribute() {},
            setAttribute() {},
        };
        const elements = new Map([
            ["editor", editor],
            ["fm-container", fmContainer],
            ["filename-display", { textContent: "" }],
            ["markdown-preview", preview],
            ["markdown-preview-status", previewStatus],
        ]);
        globalThis.document = {
            getElementById(id) { return elements.get(id) || null; },
            querySelectorAll() { return []; },
        };
        globalThis.requestAnimationFrame = callback => {
            callback();
            return 1;
        };

        let resolveSave;
        let saveStarted;
        const saveStartedPromise = new Promise(resolve => { saveStarted = resolve; });
        let saveCompleted = false;
        globalThis.fetch = async (url, options = {}) => {
            if (url === "/admin/api/csrf-token") {
                return { ok: true, status: 200, json: async () => ({ csrf_token: "csrf" }) };
            }
            if (String(url).includes("/admin/api/article?") && !options.method) {
                return { ok: true, status: 200, json: async () => ({ path: "posts/pending.md", content: "before" }) };
            }
            if (String(url).includes("/admin/api/preview/markdown")) {
                return { ok: true, status: 200, json: async () => ({ html: "<p>before</p>" }) };
            }
            if (String(url).endsWith("/admin/api/article") && options.method === "POST") {
                saveStarted();
                return new Promise(resolve => {
                    resolveSave = () => {
                        saveCompleted = true;
                        resolve({ ok: true, status: 200, json: async () => ({ status: "ok" }) });
                    };
                });
            }
            throw new Error(`Unexpected request: ${url}`);
        };

        try {
            await loadFile("posts/pending.md");
            editor.value = "after";
            const saveOperation = execAutoSave();
            await saveStartedPromise;

            const syncPreparation = prepareForGitSync();
            await Promise.resolve();
            assert.equal(isGitSyncInProgress(), true);
            assert.equal(saveCompleted, false);
            await assert.rejects(flushPendingSave, /Git Sync is in progress/);
            let mutationCalled = false;
            await assert.rejects(
                () => runGitMutation(() => { mutationCalled = true; }),
                /Git Sync is in progress/,
            );
            assert.equal(mutationCalled, false);

            resolveSave();
            await saveOperation;
            await syncPreparation;
            assert.equal(saveCompleted, true);
        } finally {
            if (isGitSyncInProgress()) finishForGitSync();
            globalThis.document = previousDocument;
            globalThis.fetch = previousFetch;
            globalThis.requestAnimationFrame = previousRequestAnimationFrame;
        }
    });

    it("pauses editor writes and blocks edits while Sync is running", async () => {
        const previousDocument = globalThis.document;
        const previousMarkDeploymentPreviewStale = window.markDeploymentPreviewStale;
        const listeners = {};
        const editor = {
            disabled: false,
            value: "before sync",
            addEventListener(type, callback) { listeners[type] = callback; },
        };
        const controls = [{ disabled: false }, { disabled: false }];
        const fmContainer = {
            querySelectorAll() { return controls; },
            addEventListener(type, callback) { listeners[type] = callback; },
        };
        let staleMarkCount = 0;
        window.markDeploymentPreviewStale = () => { staleMarkCount += 1; };
        globalThis.document = {
            getElementById(id) {
                if (id === "editor") return editor;
                if (id === "fm-container") return fmContainer;
                return null;
            },
        };

        try {
            initAutoSave();
            await prepareForGitSync();

            assert.equal(isGitSyncInProgress(), true);
            assert.equal(editor.disabled, true);
            assert.deepEqual(controls.map(control => control.disabled), [true, true]);
            editor.value = "changed during sync";
            listeners.input();
            assert.equal(staleMarkCount, 0);
            assert.equal(await execAutoSave(), false);
        } finally {
            finishForGitSync();
            globalThis.document = previousDocument;
            window.markDeploymentPreviewStale = previousMarkDeploymentPreviewStale;
        }

        assert.equal(isGitSyncInProgress(), false);
        assert.equal(editor.disabled, false);
        assert.deepEqual(controls.map(control => control.disabled), [false, false]);
    });
});
