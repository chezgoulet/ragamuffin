# @chezgoulet/ragamuffin-client

Ragamuffin MCP client for Node.js — connects to any Ragamuffin server via the
Model Context Protocol (JSON-RPC 2.0 over HTTP). Provides tool discovery and
invocation for all 30+ server tools.

## Install

```bash
npm install @chezgoulet/ragamuffin-client
```

## Usage

```js
import { MCPClient } from "@chezgoulet/ragamuffin-client";

const client = new MCPClient({
  endpoint: "http://ragamuffin:8000",
  authToken: process.env.RAGAMUFFIN_AUTH_TOKEN,
});

// Initialize
await client.initialize();

// Discover tools
const tools = await client.listTools();
console.log(tools.map((t) => t.name));

// Call a tool
const result = await client.call("memory.recall", {
  query: "how does Qdrant isolation work?",
  vault: "agent::dev",
});
console.log(result.results);
```

## API

### `new MCPClient({ endpoint, authToken, vaultPrefix })`

Create a new client. All params optional with sensible defaults.

### `client.initialize()`

Initialize the MCP session. Returns protocol version and capabilities.

### `client.listTools()`

Fetch and cache all available tools. Returns an array of `{ name, description, inputSchema }`.

### `client.call(toolName, args)`

Call a tool with named arguments. Returns the tool's result object.

### `client.vaultName(agentIdentity)`

Format a vault name: `{vaultPrefix}{agentIdentity || "default"}`.
