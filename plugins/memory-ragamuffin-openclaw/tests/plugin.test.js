/**
 * Tests for the MCP-based OpenClaw plugin entry function.
 * Mocks the `api` object and the MCP server responses.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import pluginEntry from "../index.js";

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
      endpoint: "http://ragamuffin:8000",
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
    _tools: () => tools,
    _hooks: () => hooks,
    _clis: () => clis,
    _services: () => services,
    _logs: () => logLines,
  };
}

function jsonRpcResponse(result) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ jsonrpc: "2.0", id: 1, result }),
    text: () => Promise.resolve(""),
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("pluginEntry (MCP)", () => {
  let api;

  beforeEach(() => {
    api = makeApi();
    // Default mock: return a small tool list
    mock.method(globalThis, "fetch", () =>
      jsonRpcResponse({
        tools: [
          { name: "ragamuffin_recall", description: "Search", inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } },
          { name: "ragamuffin_fact_put", description: "Write fact", inputSchema: { type: "object", properties: { key: { type: "string" }, value: { type: "string" } }, required: ["key", "value"] } },
          { name: "ragamuffin_fact_delete", description: "Delete fact", inputSchema: { type: "object", properties: { key: { type: "string" } }, required: ["key"] } },
        ],
      }),
    );
  });

  afterEach(() => {
    mock.reset();
  });

  it("registers tools dynamically from MCP tools/list", async () => {
    pluginEntry(api);
    // Tools are registered in the service start handler
    const svc = api._services()[0];
    if (svc.start) {
      await svc.start();
    }
    const tools = api._tools();
    assert.equal(tools.length, 3);
    const names = tools.map((t) => t.meta.name);
    assert.ok(names.includes("ragamuffin_recall"));
    assert.ok(names.includes("ragamuffin_fact_put"));
    assert.ok(names.includes("ragamuffin_fact_delete"));
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

  it("registers a service with start/stop", () => {
    pluginEntry(api);
    assert.equal(api._services().length, 1);
    assert.equal(api._services()[0].id, "memory-ragamuffin-openclaw");
    assert.ok(typeof api._services()[0].start === "function");
    assert.ok(typeof api._services()[0].stop === "function");
  });

  describe("MCP tool dispatch", () => {
    it("calls MCP and returns formatted result", async () => {
      pluginEntry(api);
      const svc = api._services()[0];
      if (svc.start) await svc.start();

      const tools = api._tools();
      const recallTool = tools.find((t) => t.meta.name === "ragamuffin_recall");
      assert.ok(recallTool);

      // Override fetch for this specific test
      mock.reset();
      mock.method(globalThis, "fetch", () =>
        jsonRpcResponse({
          content: [{ type: "text", text: '{"results":[{"text":"found it","score":0.9}]}' }],
        }),
      );

      const result = await recallTool.def.execute("call-1", { query: "test" });
      assert.ok(result.content[0].text.includes("found it"));
    });

    it("returns error on MCP failure", async () => {
      pluginEntry(api);
      const svc = api._services()[0];
      if (svc.start) await svc.start();

      const tools = api._tools();
      const recallTool = tools.find((t) => t.meta.name === "ragamuffin_recall");

      mock.reset();
      mock.method(globalThis, "fetch", () =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ jsonrpc: "2.0", id: 1, error: { message: "query is required", code: -32602 } }),
          text: () => Promise.resolve(""),
        }),
      );

      const result = await recallTool.def.execute("call-1", {});
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
      pluginEntry(api);
      // Override fetch to return MCP JSON-RPC with recall results
      mock.reset();
      mock.method(globalThis, "fetch", () =>
        jsonRpcResponse({
          results: [{ text: "User prefers dark mode", score: 0.95 }],
          top_score: 0.95,
        }),
      );

      const handler = api._hooks()["before_prompt_build"];
      const result = await handler({ prompt: "tell me about preferences", messages: [] });

      assert.ok(result);
      assert.ok(result.prependContext.includes("relevant-memories"));
      assert.ok(result.prependContext.includes("dark mode"));
    });

    it("does not throw on network error (fail-open)", async () => {
      pluginEntry(api);
      mock.reset();
      mock.method(globalThis, "fetch", () => Promise.reject(new Error("timeout")));

      const handler = api._hooks()["before_prompt_build"];
      const result = await handler({ prompt: "test", messages: [] });
      assert.equal(result, undefined);
    });
  });

  describe("agent_end hook (auto-capture)", () => {
    beforeEach(() => {
      api.pluginConfig.autoCapture = true;
    });

    it("stores important user messages as facts", async () => {
      let capturedArgs;
      pluginEntry(api);

      // Mock each fetch to track the MCP call
      mock.reset();
      let fetchIdx = 0;
      mock.method(globalThis, "fetch", (url, opts) => {
        fetchIdx++;
        const body = JSON.parse(opts.body);
        if (body.method === "tools/call") {
          capturedArgs = body.params.arguments;
        }
        return jsonRpcResponse({});
      });

      const handler = api._hooks()["agent_end"];
      await handler({
        success: true,
        messages: [
          { role: "user", content: "I prefer dark mode for all my apps" },
          { role: "assistant", content: "I'll remember that" },
        ],
      });

      assert.ok(capturedArgs);
      assert.equal(capturedArgs.value, "I prefer dark mode for all my apps");
    });

    it("skips non-important messages", async () => {
      let fetchCalled = false;
      pluginEntry(api);
      mock.reset();
      mock.method(globalThis, "fetch", () => {
        fetchCalled = true;
        return jsonRpcResponse({});
      });

      const handler = api._hooks()["agent_end"];
      await handler({
        success: true,
        messages: [{ role: "user", content: "What time is it?" }],
      });

      assert.equal(fetchCalled, false);
    });

    it("does not crash on store failure (best-effort)", async () => {
      pluginEntry(api);
      mock.reset();
      mock.method(globalThis, "fetch", () => Promise.reject(new Error("timeout")));

      const handler = api._hooks()["agent_end"];
      await handler({
        success: true,
        messages: [
          { role: "user", content: "I need to remember this important thing" },
        ],
      });
    });
  });
});
