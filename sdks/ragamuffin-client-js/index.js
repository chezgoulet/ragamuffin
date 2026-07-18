/**
 * Ragamuffin MCP Client for Node.js
 *
 * Universal client for the Model Context Protocol (JSON-RPC 2.0 over HTTP).
 * Connects to any Ragamuffin server's /mcp endpoint for tool discovery
 * and invocation. No external dependencies — uses native fetch().
 */

let _idCounter = 0;
function nextId() {
  return ++_idCounter;
}

export class MCPClient {
  #endpoint;
  #authToken;
  #vaultPrefix;
  #tools;

  constructor(config = {}) {
    this.#endpoint = (config.endpoint || "http://localhost:8000").replace(/\/+$/, "");
    this.#authToken = config.authToken || "";
    this.#vaultPrefix = config.vaultPrefix || "agent::";
    this.#tools = null;
  }

  vaultName(agentIdentity) {
    return `${this.#vaultPrefix}${agentIdentity || "default"}`;
  }

  #headers() {
    const h = { "Content-Type": "application/json" };
    if (this.#authToken) {
      h["Authorization"] = `Bearer ${this.#authToken}`;
    }
    return h;
  }

  async #request(method, params = {}) {
    const id = nextId();
    const body = { jsonrpc: "2.0", id, method, params };
    const resp = await fetch(`${this.#endpoint}/mcp`, {
      method: "POST",
      headers: this.#headers(),
      body: JSON.stringify(body),
    });
    const data = await resp.json();
    if (data.error) {
      throw new Error(`MCP ${method}: ${data.error.message || JSON.stringify(data.error)}`);
    }
    return data.result;
  }

  async initialize() {
    return this.#request("initialize");
  }

  async listTools() {
    if (!this.#tools) {
      const result = await this.#request("tools/list");
      this.#tools = result.tools || [];
    }
    return this.#tools;
  }

  get tools() {
    return this.#tools || [];
  }

  async call(toolName, args = {}) {
    return this.#request("tools/call", { name: toolName, arguments: args });
  }
}
