# ragamuffin-client

Ragamuffin MCP client for Python — connects to any Ragamuffin server via the
Model Context Protocol (JSON-RPC 2.0 over HTTP). Provides tool discovery and
invocation for all 30+ server tools.

## Install

```bash
pip install ragamuffin-client
```

## Usage

```python
from memory.client import MCPClient

client = MCPClient(
    endpoint="http://ragamuffin:8000",
    auth_token="sk-...",  # optional
)

# Initialize
client.initialize()

# Discover tools
tools = client.list_tools()
print([t["name"] for t in tools])

# Call a tool
result = client.call("memory.recall", {
    "query": "how does Qdrant isolation work?",
    "vault": "agent::dev",
})
print(result["results"])
```

## API

### `MCPClient(endpoint, auth_token, vault_prefix)`

Create a new client. All params optional with sensible defaults.

### `client.initialize()`

Initialize the MCP session. Returns protocol version and capabilities.

### `client.list_tools()`

Fetch and cache all available tools. Returns a list of `{name, description, inputSchema}`.

### `client.call(tool_name, args)`

Call a tool with named arguments. Returns the tool's result dict.

### `client.vault_name(agent_identity)`

Format a vault name: `{vault_prefix}{agent_identity or "default"}`.
