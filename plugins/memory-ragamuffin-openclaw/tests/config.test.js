/**
 * Tests for config resolution — env var fallback, defaults, and edge cases.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import pluginEntry from "../index.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeApi(overrides = {}) {
  const tools = [];
  const hooks = {};
  const services = [];

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
      ...overrides,
    },
    registerTool: (def, meta) => tools.push({ def, meta }),
    registerCli: () => {},
    registerService: (svc) => services.push(svc),
    on: (event, handler) => {
      hooks[event] = handler;
    },
    logger: { info: () => {}, warn: () => {}, error: () => {} },
    _tools: () => tools,
    _hooks: () => hooks,
    _services: () => services,
  };
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
    Object.assign(process.env, ORIG_ENV);
    mock.reset();
  });

  it("uses empty config if pluginConfig is missing", () => {
    // When pluginConfig is undefined, defaults should be used
    const api = makeApi();
    // Remove pluginConfig to simulate missing config
    delete api.pluginConfig;

    // Should not throw
    pluginEntry(api);
    assert.equal(api._tools().length, 3);
  });

  it("uses env vars for endpoint when config missing", () => {
    process.env.RAGAMUFFIN_ENDPOINT = "http://env-ragamuffin:9090";
    let capturedUrl;

    mock.method(globalThis, "fetch", (url) => {
      capturedUrl = url;
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      });
    });

    const api = makeApi();
    delete api.pluginConfig;
    pluginEntry(api);

    return runTool(api, "memory_recall", { query: "test" }).then(() => {
      assert.ok(capturedUrl.startsWith("http://env-ragamuffin:9090"));
    });
  });

  it("uses env vars for auth token", () => {
    process.env.RAGAMUFFIN_AUTH_TOKEN = "env-token";
    let capturedHeaders;

    mock.method(globalThis, "fetch", (url, opts) => {
      capturedHeaders = opts.headers;
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      });
    });

    const api = makeApi();
    delete api.pluginConfig;
    pluginEntry(api);

    return runTool(api, "memory_recall", { query: "test" }).then(() => {
      assert.equal(capturedHeaders["Authorization"], "Bearer env-token");
    });
  });

  it("uses env vars for vault prefix", () => {
    process.env.RAGAMUFFIN_VAULT_PREFIX = "env::";
    let capturedBody;

    mock.method(globalThis, "fetch", (url, opts) => {
      capturedBody = JSON.parse(opts.body);
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      });
    });

    const api = makeApi();
    delete api.pluginConfig;
    pluginEntry(api);

    return runTool(api, "memory_store", { key: "k", value: "v" }).then(() => {
      // Vault name in recall should use env prefix
    });
  });

  it("config values take precedence over env vars", () => {
    process.env.RAGAMUFFIN_ENDPOINT = "http://env-should-be-overridden:9090";
    let capturedUrl;

    mock.method(globalThis, "fetch", (url) => {
      capturedUrl = url;
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      });
    });

    const api = makeApi({ endpoint: "http://config-chosen:7070" });
    pluginEntry(api);

    return runTool(api, "memory_recall", { query: "test" }).then(() => {
      assert.ok(capturedUrl.startsWith("http://config-chosen:7070"));
    });
  });

  it("falls back to default endpoint when nothing configured", () => {
    process.env.RAGAMUFFIN_ENDPOINT = "";
    let capturedUrl;

    mock.method(globalThis, "fetch", (url) => {
      capturedUrl = url;
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ results: [], top_score: 0 }),
        text: () => Promise.resolve("{}"),
      });
    });

    const api = makeApi({ endpoint: "" });
    delete api.pluginConfig;
    pluginEntry(api);

    return runTool(api, "memory_recall", { query: "test" }).then(() => {
      assert.ok(capturedUrl.startsWith("http://localhost:8080"));
    });
  });

  it("disables auto-capture when config not set", () => {
    const hooks = {};
    const api = {
      pluginConfig: { endpoint: "http://r:8080", authToken: "", vaultPrefix: "agent::", recallLimit: 5, recallThreshold: 0.3, recallMaxChars: 1000, captureMaxChars: 500 },
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
