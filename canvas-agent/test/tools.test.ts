import assert from "node:assert/strict";
import test from "node:test";

import { parseToolInput } from "../src/tools.js";

test("project shot validation reports the failing field path", () => {
    assert.throws(() => parseToolInput("project_create_or_update_shots", {
        shots: [{
            title: "开场",
            durationMs: 8_000,
            definition: {
                schemaVersion: "mingwant.short-drama.shot/v1",
                shotCode: "EP001-SHOT001",
                sourceRefs: [{ unitId: "unit-1", role: "action" }],
                purpose: "建立人物",
                informationChange: "未知人物 -> 看清主角",
                assetBindings: [],
                endBoundary: { positions: ["主角抬头看向门口"] },
            },
        }],
    }), /shots\.0\.definition\.startBoundary/);
});

test("project shot validation accepts a structured definition object", () => {
    const input = parseToolInput("project_create_or_update_shots", {
        shots: [{
            unitId: "unit-1",
            title: "开场",
            durationMs: 8_000,
            definition: {
                schemaVersion: "mingwant.short-drama.shot/v1",
                shotCode: "EP001-SHOT001",
                sourceRefs: [{ unitId: "unit-1", role: "action" }],
                purpose: "建立人物",
                informationChange: "未知人物 -> 看清主角",
                assetBindings: [],
                startBoundary: { positions: ["主角坐在桌边"] },
                endBoundary: { positions: ["主角抬头看向门口"] },
            },
        }],
    }) as { shots: Array<{ durationMs?: number }> };
    assert.equal(input.shots[0].durationMs, 8_000);
});
