/**
 * Tests for config resolution — env var fallback, defaults, and edge cases.
 * Plugin uses MCP; mocks return JSON-RPC 2.0 responses.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import pluginEntry from "../index.js";

const MOCK_TOOLS = [
  { name: "memory.recall", description: "Search", inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] } },
];

function jsonRpcResult(result) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ jsonrpc: "2.0", id: 1, result }),
    text: () => Promise.resolve(""),
  });
}

function makeApi(overrides = {}) {
  const tools = [];
  const hooks = {};
  const services = [];

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
      ...overrides,
    },
    registerTool: (def, meta) => tools.push({ def, meta }),
    registerCli: () => {},
    registerService: (svc) => services.push(svc),
    on: (event, handler) => { hooks[event] = handler; },
    logger: { info: () => {}, warn: () => {}, error: () => {} },
    _tools: () => tools,
    _hooks: () => hooks,
    _services: () => services,
  };
}

/** Start the plugin's service to trigger MCP tool registration. */
async function startPlugin(api) {
  const svc = api._services()[0];
  if (svc && svc.start) {
    await svc.start();
  }
}

async function runTool(api, toolName, params) {
  const tools = api._tools();
  const tool = tools.find((t) => t.meta && t.meta.name === toolName);
  if (!tool) throw new Error(`Tool "${toolName}" not registered`);
  return tool.def.execute("call-1", params);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("config resolution (env vars)", () => {
  const ORIG_ENV = { ...process.env };

  beforeEach(() => {
    mock.method(globalThis, "fetch", () => jsonRpcResult({ tools: MOCK_TOOLS }));
  });

  afterEach(() => {
    Object.assign(process.env, ORIG_ENV);
    mock.reset();
  });

  it("uses empty config if pluginConfig is missing", async () => {
    const api = makeApi();
    delete api.pluginConfig;

    pluginEntry(api);
    await startPlugin(api);
    assert.equal(api._tools().length, 1);
  });

  it("uses env vars for endpoint when config missing", async () => {
    process.env.RAGAMUFFIN_ENDPOINT = "http://env-ragamuffin:9090";
    let capturedUrl;

    mock.reset();
    mock.method(globalThis, "fetch", (url, opts) => {
      capturedUrl = url;
      return jsonRpcResult({ tools: MOCK_TOOLS });
    });

    const api = makeApi();
    delete api.pluginConfig;
    pluginEntry(api);
    await startPlugin(api);

    // The first MCP call uses the endpoint
    assert.ok(capturedUrl.startsWith("http://env-ragamuffin:9090"));
  });

  it("uses env vars for auth token", async () => {
    process.env.RAGAMUFFIN_AUTH_TOKEN = "env-token";
    let capturedHeaders;

    mock.reset();
    mock.method(globalThis, "fetch", (url, opts) => {
      capturedHeaders = opts.headers;
      return jsonRpcResult({ tools: MOCK_TOOLS });
    });

    const api = makeApi();
    delete api.pluginConfig;
    pluginEntry(api);
    await startPlugin(api);

    assert.equal(capturedHeaders["Authorization"], "Bearer env-token");
  });

  it("config values take precedence over env vars", async () => {
    process.env.RAGAMUFFIN_ENDPOINT = "http://env-should-be-overridden:9090";
    let capturedUrl;

    mock.reset();
    mock.method(globalThis, "fetch", (url) => {
      capturedUrl = url;
      return jsonRpcResult({ tools: MOCK_TOOLS });
    });

    const api = makeApi({ endpoint: "http://config-chosen:7070" });
    pluginEntry(api);
    await startPlugin(api);

    assert.ok(capturedUrl.startsWith("http://config-chosen:7070"));
  });

  it("falls back to default endpoint when nothing configured", async () => {
    process.env.RAGAMUFFIN_ENDPOINT = "";
    let capturedUrl;

    mock.reset();
    mock.method(globalThis, "fetch", (url) => {
      capturedUrl = url;
      return jsonRpcResult({ tools: MOCK_TOOLS });
    });

    const api = makeApi({ endpoint: "" });
    delete api.pluginConfig;
    pluginEntry(api);
    await startPlugin(api);

    assert.ok(capturedUrl.startsWith("http://localhost:8000"));
  });

  it("disables auto-capture when config not set", () => {
    const hooks = {};
    const api = {
      pluginConfig: { endpoint: "http://r:8000", authToken: "", vaultPrefix: "agent::", recallLimit: 5, recallThreshold: 0.3, recallMaxChars: 1000, captureMaxChars: 500 },
      registerTool: () => {},
      registerCli: () => {},
      registerService: () => {},
      on: (event, handler) => { hooks[event] = handler; },
      logger: { info: () => {}, warn: () => {}, error: () => {} },
    };

    pluginEntry(api);
    assert.equal(hooks["agent_end"], undefined);
  });
});
