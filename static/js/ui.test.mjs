import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { installTestWindow } from "./test_helpers/browser.mjs";

installTestWindow();
const previousDocument = globalThis.document;
const UI = await import("./ui.js");

describe("Front Matter collection", () => {
    it("preserves boolean draft values from the checkbox", () => {
        const controls = [
            { dataset: { key: "draft", widget: "boolean" }, checked: false },
            { dataset: { key: "title", widget: "string" }, value: "Published" },
        ];
        globalThis.document = { querySelectorAll: () => controls };

        assert.deepEqual(UI.collectFrontMatter(), { draft: false, title: "Published" });
    });
});

globalThis.document = previousDocument;
