import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { createStorage, installTestWindow } from "./test_helpers/browser.mjs";

installTestWindow();
const { createDraftUUID, getOrCreateDraftID } = await import("./editor.js");

describe("editor draft IDs", () => {
    it("uses getRandomValues for a UUID v4 when randomUUID is unavailable", () => {
        let called = false;
        const cryptoWithoutRandomUUID = {
            getRandomValues(bytes) {
                called = true;
                bytes.forEach((_, index) => { bytes[index] = index; });
                return bytes;
            },
        };

        const draftID = createDraftUUID(cryptoWithoutRandomUUID);
        assert.equal(called, true);
        assert.equal(draftID, "00010203-0405-4607-8809-0a0b0c0d0e0f");
        assert.match(draftID, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    });

    it("fails closed when no cryptographic random source is available", () => {
        assert.throws(() => createDraftUUID({}), /Secure random number generation is unavailable/);
    });

    it("persists one UUID per site and article for the browser session", () => {
        const memoryStorage = createStorage(new Map());
        let sequence = 0;
        const createUUID = () => `uuid-${++sequence}`;

        assert.equal(getOrCreateDraftID("docs", "posts/one.md", memoryStorage, createUUID), "uuid-1");
        assert.equal(getOrCreateDraftID("docs", "posts/one.md", memoryStorage, createUUID), "uuid-1");
        assert.equal(getOrCreateDraftID("docs", "posts/two.md", memoryStorage, createUUID), "uuid-2");
        assert.equal(getOrCreateDraftID("blog", "posts/one.md", memoryStorage, createUUID), "uuid-3");
    });
});
