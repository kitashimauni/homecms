import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
    PUBLISH_MODE_DIRECT,
    PUBLISH_MODE_PREVIEW,
    getPublishMode,
    publishConfirmationMessage,
} from "./publish.js";

describe("Publish mode", () => {
    it("uses the reviewed preview only when it is ready", () => {
        assert.equal(getPublishMode({ deploymentEnabled: true, deploymentStatus: "ready" }), PUBLISH_MODE_PREVIEW);
        assert.equal(getPublishMode({ deploymentEnabled: true, deploymentStatus: "stale" }), PUBLISH_MODE_DIRECT);
        assert.equal(getPublishMode({ deploymentEnabled: true, deploymentStatus: "building" }), PUBLISH_MODE_DIRECT);
        assert.equal(getPublishMode({ deploymentEnabled: true, deploymentStatus: "failed" }), PUBLISH_MODE_DIRECT);
    });

    it("allows direct publishing when preview is disabled or unavailable", () => {
        assert.equal(getPublishMode({ deploymentEnabled: false }), PUBLISH_MODE_DIRECT);
        assert.equal(getPublishMode({ deploymentEnabled: true, deploymentStatus: null }), PUBLISH_MODE_DIRECT);
        assert.match(publishConfirmationMessage({ deploymentEnabled: false }), /現在の編集内容/);
        assert.match(publishConfirmationMessage({ deploymentEnabled: true, deploymentStatus: "failed" }), /失敗/);
    });
});
