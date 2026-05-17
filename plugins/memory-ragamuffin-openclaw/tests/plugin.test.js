/**
 * Tests for the OpenClaw plugin entry function.
 * Mocks the `api` object to verify tool/hook/CLI/service registration
 * and tests the execute handlers for each tool.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import pluginEntry, { RagamuffinClient } from "../index.js";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

function makeApi() {
  const tools = [];
  const hooks = {};
  const clis = [];
  const services = [];
  const logLines = [];

  return {
    pluginConfig: {
      endpoint: "http://ragamuffin:8080",
      authToken: "",
      vaultPrefix: "agent::",
      autoRecall: true,
      autoCapture: false,
      recallLimit: 5,
      recallThreshold: 0.3,
      recallMaxChars: 1000,
      captureMaxChars: 500,
    },
    registerTool: (def, meta) => {
      tools.push({ def, meta });
    },
    registerCli: (fn, meta) => {
      clis.push({ fn, meta });
    },
    registerService: (svc) => {
      services.push(svc);
    },
    on: (event, handler) => {
      hooks[event] = handler;
    },
    logger: {
      info: (...args) => logLines.push(["info", ...args]),
      warn: (...args) => logLines.push(["warn", ...args]),
      error: (...args) => logLines.push(["error", ...args]),
    },
    // Test accessors
    _tools: () => tools,
    _hooks: () => hooks,
    _clis: () => clis,
    _services: () => services,
    _logs: () => logLines,
  };
}

/** Run a tool's execute handler and return the result. */
async function runTool(api, toolName, params) {
  const tools = api._tools();
  const tool = tools.find((t) => t.meta && t.meta.name === toolName);
  if (!tool) throw new Error(`Tool "${toolName}" not registered`);
  return tool.def.execute("call-1", params);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("pluginEntry", () => {
  let api;

  beforeEach(() => {
    api = makeApi();
    mock.method(globalThis, "fetch", () =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      }),
    );
  });

  afterEach(() => {
    mock.reset();
  });

  it("registers three tools", () => {
    pluginEntry(api);
    const tools = api._tools();
    assert.equal(tools.length, 3);
    const names = tools.map((t) => t.meta.name);
    assert.ok(names.includes("memory_recall"));
    assert.ok(names.includes("memory_store"));
    assert.ok(names.includes("memory_forget"));
  });

  it("registers before_prompt_build hook", () => {
    pluginEntry(api);
    assert.ok(typeof api._hooks()["before_prompt_build"] === "function");
  });

  it("does not register agent_end hook when autoCapture is false", () => {
    pluginEntry(api);
    assert.equal(api._hooks()["agent_end"], undefined);
  });

  it("registers agent_end hook when autoCapture is true", () => {
    api.pluginConfig.autoCapture = true;
    pluginEntry(api);
    assert.ok(typeof api._hooks()["agent_end"] === "function");
  });

  it("registers CLI commands", () => {
    pluginEntry(api);
    assert.equal(api._clis().length, 1);
    assert.deepEqual(api._clis()[0].meta, { commands: ["ragamuffin"] });
  });

  it("registers a service", () => {
    pluginEntry(api);
    assert.equal(api._services().length, 1);
    assert.equal(api._services()[0].id, "memory-ragamuffin-openclaw");
  });

  describe("memory_recall tool", () => {
    it("returns memories on success", async () => {
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              results: [
                { text: "User prefers dark mode", score: 0.95 },
                { text: "Uses Vim", score: 0.72 },
              ],
              top_score: 0.95,
            }),
          text: () =>
            Promise.resolve(
              '{"results":[{"text":"User prefers dark mode","score":0.95},{"text":"Uses Vim","score":0.72}],"top_score":0.95}',
            ),
        }),
      );

      pluginEntry(api);
      const result = await runTool(api, "memory_recall", {
        query: "preferences",
        limit: 3,
        threshold: 0.5,
      });

      assert.equal(result.isError, undefined);
      assert.ok(result.content[0].text.includes("dark mode"));
      assert.ok(result.content[0].text.includes("Vim"));
    });

    it("returns no results message when empty", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_recall", { query: "nothing" });
      assert.equal(result.isError, undefined);
      assert.ok(result.content[0].text.includes("No relevant memories found"));
    });

    it("returns error on missing query", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_recall", {});
      assert.equal(result.isError, true);
      assert.ok(result.content[0].text.includes("Query is required"));
    });

    it("returns error on empty string query", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_recall", { query: "" });
      assert.equal(result.isError, true);
    });

    it("fail-open on network error", async () => {
      mock.method(globalThis, "fetch", () => Promise.reject(new Error("connection refused")));
      pluginEntry(api);
      const result = await runTool(api, "memory_recall", { query: "test" });
      assert.equal(result.isError, true);
      assert.ok(result.content[0].text.includes("connection refused"));
    });

    it("fail-open on server error", async () => {
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: false,
          status: 502,
          json: () => Promise.resolve({}),
          text: () => Promise.resolve("Bad Gateway"),
        }),
      );

      pluginEntry(api);
      const result = await runTool(api, "memory_recall", { query: "test" });
      assert.equal(result.isError, true);
      assert.ok(result.content[0].text.includes("continues without memory"));
    });
  });

  describe("memory_store tool", () => {
    it("stores fact on success", async () => {
      let capturedBody;
      mock.method(globalThis, "fetch", (url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ key: "test/key" }),
          text: () => Promise.resolve('{"key":"test/key"}'),
        });
      });

      pluginEntry(api);
      const result = await runTool(api, "memory_store", {
        key: "test/key",
        value: "test value",
        tags: ["test"],
      });

      assert.equal(capturedBody.key, "test/key");
      assert.equal(capturedBody.value, "test value");
      assert.deepEqual(capturedBody.tags, ["test"]);
      assert.equal(result.content[0].text, 'Stored: "test/key" → saved.');
    });

    it("returns error on missing key", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_store", { key: "", value: "v" });
      assert.equal(result.isError, true);
    });

    it("returns error on missing value", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_store", { key: "k", value: "" });
      assert.equal(result.isError, true);
    });

    it("fail-open on server error", async () => {
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: false,
          status: 500,
          json: () => Promise.resolve({}),
          text: () => Promise.resolve("Internal Server Error"),
        }),
      );

      pluginEntry(api);
      const result = await runTool(api, "memory_store", { key: "k", value: "v" });
      assert.equal(result.isError, true);
    });
  });

  describe("memory_forget tool", () => {
    it("deletes fact on success", async () => {
      let capturedMethod;
      mock.method(globalThis, "fetch", (url, opts) => {
        capturedMethod = opts.method;
        return Promise.resolve({
          ok: true,
          status: 204,
          json: () => Promise.resolve(null),
          text: () => Promise.resolve(""),
        });
      });

      pluginEntry(api);
      const result = await runTool(api, "memory_forget", { key: "test/key" });
      assert.equal(capturedMethod, "DELETE");
      assert.equal(result.content[0].text, 'Forgotten: "test/key"');
    });

    it("returns error on missing key", async () => {
      pluginEntry(api);
      const result = await runTool(api, "memory_forget", { key: "" });
      assert.equal(result.isError, true);
    });

    it("fail-open on server error", async () => {
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: false,
          status: 500,
          json: () => Promise.resolve({}),
          text: () => Promise.resolve("error"),
        }),
      );

      pluginEntry(api);
      const result = await runTool(api, "memory_forget", { key: "bad" });
      assert.equal(result.isError, true);
    });
  });

  describe("before_prompt_build hook", () => {
    it("returns null and does not inject when no results", async () => {
      pluginEntry(api);
      const handler = api._hooks()["before_prompt_build"];
      const result = await handler({ prompt: "test prompt", messages: [] });
      assert.equal(result, undefined);
    });

    it("returns prepended context when results found", async () => {
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              results: [{ text: "User prefers dark mode", score: 0.95 }],
              top_score: 0.95,
            }),
          text: () => Promise.resolve('{"results":[{"text":"User prefers dark mode","score":0.95}],"top_score":0.95}'),
        }),
      );

      pluginEntry(api);
      const handler = api._hooks()["before_prompt_build"];
      const result = await handler({ prompt: "tell me about preferences", messages: [] });

      assert.ok(result);
      assert.ok(result.prependContext.includes("relevant-memories"));
      assert.ok(result.prependContext.includes("dark mode"));
    });

    it("does not throw on network error (fail-open)", async () => {
      mock.method(globalThis, "fetch", () => Promise.reject(new Error("timeout")));
      pluginEntry(api);
      const handler = api._hooks()["before_prompt_build"];
      const result = await handler({ prompt: "test", messages: [] });
      assert.equal(result, undefined);
    });

    it("skips injection when prompt is too short", async () => {
      // Should not even call fetch for very short prompts
      let fetchCalled = false;
      mock.method(globalThis, "fetch", () => {
        fetchCalled = true;
        return Promise.reject(new Error("should not be called"));
      });

      pluginEntry(api);
      const handler = api._hooks()["before_prompt_build"];
      await handler({ prompt: "hi", messages: [] });
      assert.equal(fetchCalled, false);
    });
  });

  describe("agent_end hook (auto-capture)", () => {
    beforeEach(() => {
      api.pluginConfig.autoCapture = true;
    });

    it("stores important user messages as facts", async () => {
      let capturedBody;
      mock.method(globalThis, "fetch", (url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ key: capturedBody.key }),
          text: () => Promise.resolve("{}"),
        });
      });

      pluginEntry(api);
      const handler = api._hooks()["agent_end"];
      await handler({
        success: true,
        messages: [
          { role: "user", content: "I prefer dark mode for all my apps" },
          { role: "assistant", content: "I'll remember that" },
        ],
      });

      assert.ok(capturedBody);
      assert.equal(capturedBody.value, "I prefer dark mode for all my apps");
    });

    it("skips non-important messages", async () => {
      let fetchCalled = false;
      mock.method(globalThis, "fetch", () => {
        fetchCalled = true;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({}),
          text: () => Promise.resolve("{}"),
        });
      });

      pluginEntry(api);
      const handler = api._hooks()["agent_end"];
      await handler({
        success: true,
        messages: [{ role: "user", content: "What time is it?" }],
      });

      assert.equal(fetchCalled, false);
    });

    it("does not crash on store failure (best-effort)", async () => {
      mock.method(globalThis, "fetch", () => Promise.reject(new Error("timeout")));

      pluginEntry(api);
      const handler = api._hooks()["agent_end"];
      // Should not throw despite store failure
      await handler({
        success: true,
        messages: [
          { role: "user", content: "I need to remember this important thing" },
        ],
      });
      // If we get here without throwing, the test passes
    });
  });
});
