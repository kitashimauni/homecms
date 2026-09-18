import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { reloadConfigAfterSync } from "./config_reload.js";

describe("Git Sync CMS config reload", () => {
    it("applies the latest config after a successful sync", async () => {
        const applied = [];
        const config = { collections: [{ name: "updated-posts" }] };

        const reloaded = await reloadConfigAfterSync({
            siteID: "site-a",
            generation: 4,
            fetchConfig: async siteID => {
                assert.equal(siteID, "site-a");
                return config;
            },
            isCurrent: (siteID, generation) => siteID === "site-a" && generation === 4,
            applyConfig: value => applied.push(value),
        });

        assert.equal(reloaded, true);
        assert.deepEqual(applied, [config]);
    });

    it("does not apply a config response after the site context becomes stale", async () => {
        let resolveConfig;
        const configRequest = new Promise(resolve => { resolveConfig = resolve; });
        const applied = [];
        let currentSite = "site-a";
        let currentGeneration = 4;

        const reload = reloadConfigAfterSync({
            siteID: "site-a",
            generation: 4,
            fetchConfig: async () => configRequest,
            isCurrent: (siteID, generation) => siteID === currentSite && generation === currentGeneration,
            applyConfig: config => applied.push(config),
        });

        currentSite = "site-b";
        currentGeneration = 5;
        resolveConfig({ collections: [{ name: "stale" }] });

        assert.equal(await reload, false);
        assert.deepEqual(applied, []);
    });
});
