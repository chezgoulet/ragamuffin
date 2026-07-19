/**
 * Tests for MCPClient — the MCP JSON-RPC client for the Ragamuffin API.
 * Uses mocked global fetch via node:test mock.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import { MCPClient } from "../index.js";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** Set fetch to return a specific JSON-RPC response. */
function mockRespond(result, error) {
  const body = { jsonrpc: "2.0", id: 1 };
  if (error) {
    body.error = typeof error === "string" ? { message: error, code: -32603 } : error;
  } else {
    body.result = result;
  }
  mock.method(globalThis, "fetch", () =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    }),
  );
}

function mockFetchHandler(handler) {
  mock.method(globalThis, "fetch", handler);
}

function mockFetchReject(error) {
  mock.method(globalThis, "fetch", () => Promise.reject(error));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("MCPClient", () => {
  let client;

  beforeEach(() => {
    client = new MCPClient({
      endpoint: "http://ragamuffin:8000",
      authToken: "",
      vaultPrefix: "agent::",
    });
  });

  afterEach(() => {
    mock.reset();
  });

  describe("vaultName", () => {
    it("returns prefixed name", () => {
      assert.equal(client.vaultName("dev"), "agent::dev");
    });

    it("falls back to 'default'", () => {
      assert.equal(client.vaultName(), "agent::default");
    });

    it("uses custom prefix", () => {
      const c = new MCPClient({
        endpoint: "http://r:8000",
        vaultPrefix: "tenant::",
      });
      assert.equal(c.vaultName("alice"), "tenant::alice");
    });
  });

  describe("initialize", () => {
    it("sends MCP initialize request", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({
            jsonrpc: "2.0",
            id: 1,
            result: { protocolVersion: "2024-11-05", capabilities: { tools: { listChanged: false } } },
          }),
          text: () => Promise.resolve(""),
        });
      });

      const result = await client.initialize();
      assert.equal(capturedBody.method, "initialize");
      assert.equal(capturedBody.jsonrpc, "2.0");
      assert.ok(result.protocolVersion);
    });
  });

  describe("listTools", () => {
    it("returns tools from server and caches them", async () => {
      const toolList = [{ name: "memory.recall", description: "Search" }];
      let callCount = 0;
      mockFetchHandler((url, opts) => {
        callCount++;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({
            jsonrpc: "2.0",
            id: 1,
            result: { tools: toolList },
          }),
          text: () => Promise.resolve(""),
        });
      });

      const t1 = await client.listTools();
      assert.equal(t1.length, 1);
      assert.equal(t1[0].name, "memory.recall");
      // Second call uses cache
      const t2 = await client.listTools();
      assert.equal(callCount, 1); // no extra fetch
    });
  });

  describe("call", () => {
    it("sends MCP tools/call request", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({
            jsonrpc: "2.0",
            id: 1,
            result: { content: [{ type: "text", text: "success" }] },
          }),
          text: () => Promise.resolve(""),
        });
      });

      const result = await client.call("memory.recall", { query: "test", vault: "agent::dev" });
      assert.equal(capturedBody.method, "tools/call");
      assert.equal(capturedBody.params.name, "memory.recall");
      assert.equal(capturedBody.params.arguments.query, "test");
      assert.ok(result.content);
    });

    it("throws on MCP error response", async () => {
      mockRespond(null, { message: "query is required", code: -32602 });
      await assert.rejects(
        () => client.call("memory.recall", {}),
        /query is required/,
      );
    });

    it("throws on network error", async () => {
      mockFetchReject(new Error("fetch failed"));
      await assert.rejects(
        () => client.call("memory.recall", { query: "test" }),
        /fetch failed/,
      );
    });

    it("increments request ids", async () => {
      let ids = [];
      mockFetchHandler((url, opts) => {
        const body = JSON.parse(opts.body);
        ids.push(body.id);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ jsonrpc: "2.0", id: body.id, result: {} }),
          text: () => Promise.resolve(""),
        });
      });

      await client.call("tool_a", {});
      await client.call("tool_b", {});
      assert.notEqual(ids[0], ids[1]);
    });
  });

  describe("auth headers", () => {
    it("includes bearer token when configured", async () => {
      const authed = new MCPClient({
        endpoint: "http://r:8000",
        authToken: "sekret",
      });

      let capturedHeaders;
      mockFetchHandler((url, opts) => {
        capturedHeaders = opts.headers;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ jsonrpc: "2.0", id: 1, result: {} }),
          text: () => Promise.resolve(""),
        });
      });

      await authed.call("memory.recall", { query: "test" });
      assert.equal(capturedHeaders["Authorization"], "Bearer sekret");
    });

    it("omits auth header when no token", async () => {
      let capturedHeaders;
      mockFetchHandler((url, opts) => {
        capturedHeaders = opts.headers;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ jsonrpc: "2.0", id: 1, result: {} }),
          text: () => Promise.resolve(""),
        });
      });

      await client.call("memory.recall", { query: "test" });
      assert.equal(capturedHeaders["Authorization"], undefined);
    });
  });

  describe("tools cache (getter)", () => {
    it("returns empty before listTools", () => {
      assert.deepEqual(client.tools, []);
    });

    it("returns cached tools after listTools", async () => {
      mockRespond({ tools: [{ name: "t1" }, { name: "t2" }] });
      await client.listTools();
      assert.equal(client.tools.length, 2);
    });
  });
});
