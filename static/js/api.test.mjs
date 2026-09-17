import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { installTestWindow } from "./test_helpers/browser.mjs";

installTestWindow();
const API = await import("./api.js");

describe("site-scoped preview API contracts", () => {
    it("preserves the production base revision and conflict details", async () => {
        const calls = [];
        globalThis.fetch = async (url, options = {}) => {
            calls.push({ url, options });
            if (url === "/admin/api/csrf-token") {
                return { ok: true, status: 200, json: async () => ({ csrf_token: "csrf" }) };
            }
            return {
                ok: false,
                status: 409,
                json: async () => ({ code: "CONFLICT", message: "reload", current_revision: "sha256:remote" }),
            };
        };

        API.setCurrentSite("site-a");
        await assert.rejects(
            API.saveArticle({ path: "posts/one.md", content: "stale", base_revision: "sha256:old" }),
            error => error.status === 409 && error.code === "CONFLICT" && error.currentRevision === "sha256:remote",
        );
        assert.equal(JSON.parse(calls[1].options.body).base_revision, "sha256:old");
    });

    it("surfaces HTTP errors from Git Sync", async () => {
        globalThis.fetch = async () => ({
            ok: false,
            status: 409,
            json: async () => ({ code: "CONFLICT", message: "Git Sync conflict" }),
        });

        await assert.rejects(
            API.runSync(),
            error => error.status === 409 && error.code === "CONFLICT" && error.message === "Git Sync conflict",
        );
    });

    it("pins read requests to their explicit site", async () => {
        const calls = [];
        globalThis.fetch = async (url, options = {}) => {
            calls.push({ url, options });
            return { ok: true, status: 200, json: async () => ({ status: "ready" }) };
        };

        API.setCurrentSite("site-a");
        await API.fetchConfig("site-b");
        await API.fetchArticles("site-b");
        await API.fetchLocalPreviewStatus(undefined, "site-b");
        await API.fetchPreviewDeployment("draft/id", undefined, "site-b");

        assert.deepEqual(calls.map(call => call.url), [
            "/admin/api/config?site=site-b",
            "/admin/api/articles?site=site-b",
            "/admin/api/preview/local/status?site=site-b",
            "/admin/api/preview/deployments/draft%2Fid?site=site-b",
        ]);
        calls.forEach(call => assert.equal(call.options.headers["X-CMS-Site"], "site-b"));
    });

    it("pins destructive preview operations to the selected site", async () => {
        const calls = [];
        globalThis.fetch = async (url, options = {}) => {
            calls.push({ url, options });
            if (url === "/admin/api/csrf-token") return { ok: true, status: 200, json: async () => ({ csrf_token: "csrf" }) };
            return { ok: true, status: 200, json: async () => ({ status: "ok" }) };
        };

        API.setCurrentSite("site-a");
        await API.stopLocalPreviewContent("site-b");
        await API.retryPreviewDeployment("draft/id", "site-b");
        const requestCalls = calls.filter(call => call.url !== "/admin/api/csrf-token");
        assert.deepEqual(requestCalls.map(call => call.url), [
            "/admin/api/preview/local/stop?site=site-b",
            "/admin/api/preview/deployments/draft%2Fid/retry?site=site-b",
        ]);
        requestCalls.forEach(call => assert.equal(call.options.headers["X-CMS-Site"], "site-b"));
    });
});

describe("media API contracts", () => {
    it("pins article context to Article media deletion", async () => {
        const calls = [];
        globalThis.fetch = async (url, options = {}) => {
            calls.push({ url, options });
            if (url === "/admin/api/csrf-token") return { ok: true, status: 200, json: async () => ({ csrf_token: "csrf" }) };
            return { ok: true, status: 200, json: async () => ({ status: "deleted" }) };
        };

        await API.deleteMedia("content/posts/20260608/images/photo.jpg", "posts/20260608/takao.md");
        const request = calls.find(call => call.url === "/admin/api/media/delete?site=site-a");
        assert.deepEqual(JSON.parse(request.options.body), {
            repo_path: "content/posts/20260608/images/photo.jpg",
            article_path: "posts/20260608/takao.md",
        });
    });
});
