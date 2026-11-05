import assert from "node:assert/strict";
import { after, beforeEach, test } from "node:test";
import { mock } from "bun:test";

let writeText;
let fallback;
let fallbackCalls;
const success = [];
const errors = [];
const previousNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");

mock.module("antd", () => ({ App: { useApp: () => ({ message: { success: (text) => success.push(text), error: (text) => errors.push(text) } }) } }));
mock.module("copy-to-clipboard", () => ({
    default: (...args) => {
        fallbackCalls++;
        return fallback(...args);
    },
}));
const { useCopyText } = await import("./use-copy-text.ts");

beforeEach(() => {
    success.length = 0;
    errors.length = 0;
    fallbackCalls = 0;
    writeText = async () => {};
    fallback = async () => false;
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { clipboard: { writeText: (...args) => writeText(...args) } } });
});

after(() => {
    if (previousNavigator) Object.defineProperty(globalThis, "navigator", previousNavigator);
    else delete globalThis.navigator;
    mock.restore();
});

test("reports success only after the native clipboard promise completes", async () => {
    let finish;
    writeText = () =>
        new Promise((resolve) => {
            finish = resolve;
        });
    const copying = useCopyText()("complete-key", "copied");
    assert.equal(success.length, 0);
    finish();
    assert.equal(await copying, true);
    assert.deepEqual(success, ["copied"]);
    assert.equal(fallbackCalls, 0);
});

test("awaits an asynchronous fallback and checks its boolean result", async () => {
    writeText = async () => {
        throw new Error("NotAllowedError");
    };
    fallback = async (value, options) => {
        assert.equal(value, "complete-key");
        assert.equal(options.fallbackToPrompt, false);
        return true;
    };
    assert.equal(await useCopyText()("complete-key"), true);
    assert.equal(fallbackCalls, 1);
    assert.deepEqual(success, ["已复制"]);
    assert.equal(errors.length, 0);
});

test("returns false for denied native copy and false fallback without a success toast", async () => {
    writeText = async () => {
        throw new Error("NotAllowedError");
    };
    fallback = async () => false;
    assert.equal(await useCopyText()("complete-key"), false);
    assert.equal(success.length, 0);
    assert.deepEqual(errors, ["复制失败，请手动选择并复制"]);
});

test("suppresses feedback and fallback after the owner changes during native copy", async () => {
    let current = true;
    writeText = async () => {
        current = false;
    };
    assert.equal(await useCopyText()("complete-key", "copied", () => current), false);
    assert.equal(fallbackCalls, 0);
    assert.equal(success.length, 0);
    assert.equal(errors.length, 0);
});

test("suppresses late fallback results and failures after the dialog closes", async () => {
    writeText = async () => {
        throw new Error("NotAllowedError");
    };
    for (const reject of [false, true]) {
        let current = true;
        fallback = async () => {
            current = false;
            if (reject) throw new Error("copy failed");
            return true;
        };
        assert.equal(await useCopyText()("complete-key", "copied", () => current), false);
    }
    assert.equal(success.length, 0);
    assert.equal(errors.length, 0);
});

test("does not touch clipboard for an already stale identity", async () => {
    writeText = async () => {
        assert.fail("must not copy for a stale identity");
    };
    assert.equal(await useCopyText()("complete-key", "copied", () => false), false);
    assert.equal(fallbackCalls, 0);
    assert.equal(success.length, 0);
    assert.equal(errors.length, 0);
});
