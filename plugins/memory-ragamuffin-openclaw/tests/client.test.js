/**
 * Tests for RagamuffinClient — the internal HTTP client for the Ragamuffin API.
 * Uses mocked global fetch via node:test mock to avoid real HTTP calls.
 */

import { describe, it, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import { RagamuffinClient } from "../index.js";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** Set fetch to return a specific response. */
function mockRespond(status, body, ok) {
  const response = {
    ok: ok !== undefined ? ok : status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () =>
      Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  };
  mock.method(globalThis, "fetch", () => Promise.resolve(response));
}

/** Accept a callback that receives (url, opts) and returns a response. */
function mockFetchHandler(handler) {
  mock.method(globalThis, "fetch", handler);
}

let callCount = 0;

/** Chain multiple fetch responses in call order. Returns call count fn. */
function mockFetchSequence(...responses) {
  callCount = 0;
  const fetchMock = mock.method(globalThis, "fetch", (url, opts) => {
    const idx = callCount++;
    const resp = responses[idx];
    if (!resp) {
      return Promise.reject(new Error(`Unexpected fetch call #${idx}: ${url}`));
    }
    const handler = resp.handler || ((_url, _opts) => {
      const ok = resp.ok !== undefined ? resp.ok : (resp.status >= 200 && resp.status < 300);
      return Promise.resolve({
        ok,
        status: resp.status,
        json: () => Promise.resolve(resp.body),
        text: () =>
          Promise.resolve(typeof resp.body === "string" ? resp.body : JSON.stringify(resp.body)),
      });
    });
    return handler(url, opts);
  });
  return () => callCount;
}

function mockFetchReject(error) {
  mock.method(globalThis, "fetch", () => Promise.reject(error));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("RagamuffinClient", () => {
  let client;

  beforeEach(() => {
    client = new RagamuffinClient({
      endpoint: "http://ragamuffin:8080",
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
      const c = new RagamuffinClient({
        endpoint: "http://r:8080",
        vaultPrefix: "tenant::",
      });
      assert.equal(c.vaultName("alice"), "tenant::alice");
    });
  });

  describe("recall", () => {
    it("returns results from server", async () => {
      mockRespond(200, {
        results: [
          { text: "User prefers dark mode", score: 0.95 },
          { text: "Uses Vim for editing", score: 0.72 },
        ],
        top_score: 0.95,
      });

      const results = await client.recall("agent::dev", "user preferences");
      assert.equal(results.length, 2);
      assert.equal(results[0].text, "User prefers dark mode");
      assert.equal(results[0].score, 0.95);
    });

    it("sends correct request body", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [] }),
          text: () => Promise.resolve("{}"),
        });
      });

      await client.recall("agent::alice", "test query", { limit: 3, threshold: 0.5 });
      assert.deepEqual(capturedBody, {
        query: "test query",
        top_k: 3,
        score_threshold: 0.5,
      });
    });

    it("uses default limit and threshold", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [] }),
          text: () => Promise.resolve("{}"),
        });
      });

      await client.recall("agent::default", "query");
      assert.equal(capturedBody.top_k, 5);
      assert.equal(capturedBody.score_threshold, 0.3);
    });

    it("returns empty array on empty results", async () => {
      mockRespond(200, { results: [], top_score: 0 });
      const results = await client.recall("agent::dev", "nothing");
      assert.deepEqual(results, []);
    });

    it("throws on server error", async () => {
      mockRespond(502, { error: "EMBEDDING_API_ERROR", message: "embedding failed" }, false);
      await assert.rejects(
        () => client.recall("agent::dev", "test"),
        /Ragamuffin POST.*502/,
      );
    });

    it("throws on network error", async () => {
      mockFetchReject(new Error("fetch failed"));
      await assert.rejects(
        () => client.recall("agent::dev", "test"),
        /fetch failed/,
      );
    });

    it("encodes vault name in URL", async () => {
      let capturedUrl;
      mockFetchHandler((url) => {
        capturedUrl = url;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [] }),
          text: () => Promise.resolve("{}"),
        });
      });

      await client.recall("agent::my agent", "query");
      assert.ok(capturedUrl.includes("/vault/agent%3A%3Amy%20agent/recall"));
    });
  });

  describe("storeFact", () => {
    it("sends fact to server", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ key: "user/prefers-dark", value: "dark mode" }),
          text: () => Promise.resolve('{"key":"user/prefers-dark","value":"dark mode"}'),
        });
      });

      const result = await client.storeFact("agent::dev", "user/prefers-dark", "dark mode");
      assert.equal(capturedBody.key, "user/prefers-dark");
      assert.equal(capturedBody.value, "dark mode");
      assert.equal(result.key, "user/prefers-dark");
    });

    it("accepts optional tags and source", async () => {
      let capturedBody;
      mockFetchHandler((url, opts) => {
        capturedBody = JSON.parse(opts.body);
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({}),
          text: () => Promise.resolve("{}"),
        });
      });

      await client.storeFact("agent::dev", "k", "v", {
        tags: ["preference", "ui"],
        source: "conversation#42",
        sourceType: "agent_observation",
      });
      assert.deepEqual(capturedBody.tags, ["preference", "ui"]);
      assert.equal(capturedBody.source, "conversation#42");
      assert.equal(capturedBody.source_type, "agent_observation");
    });
  });

  describe("deleteFact", () => {
    it("sends DELETE request", async () => {
      let capturedMethod, capturedUrl;
      mockFetchHandler((url, opts) => {
        capturedUrl = url;
        capturedMethod = opts.method;
        return Promise.resolve({
          ok: true,
          status: 204,
          json: () => Promise.resolve(null),
          text: () => Promise.resolve(""),
        });
      });

      await client.deleteFact("agent::dev", "user/old-preference");
      assert.equal(capturedMethod, "DELETE");
      assert.ok(capturedUrl.includes("key=user%2Fold-preference"));
    });
  });

  describe("auth headers", () => {
    it("includes bearer token when configured", async () => {
      const authed = new RagamuffinClient({
        endpoint: "http://r:8080",
        authToken: "sekret",
        vaultPrefix: "agent::",
      });

      let capturedHeaders;
      mockFetchHandler((url, opts) => {
        capturedHeaders = opts.headers;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [] }),
          text: () => Promise.resolve("{}"),
        });
      });

      await authed.recall("agent::dev", "test");
      assert.equal(capturedHeaders["Authorization"], "Bearer sekret");
    });

    it("omits auth header when no token", async () => {
      let capturedHeaders;
      mockFetchHandler((url, opts) => {
        capturedHeaders = opts.headers;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [] }),
          text: () => Promise.resolve("{}"),
        });
      });

      await client.recall("agent::dev", "test");
      assert.equal(capturedHeaders["Authorization"], undefined);
    });
  });
});
