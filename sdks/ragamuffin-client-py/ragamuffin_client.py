"""Ragamuffin MCP Client for Python.

Universal client for the Model Context Protocol (JSON-RPC 2.0 over HTTP).
Connects to any Ragamuffin server's /mcp endpoint for tool discovery
and invocation. No external dependencies beyond the standard library.

Usage:
    client = MCPClient(endpoint="http://ragamuffin:8000")
    client.initialize()
    tools = client.list_tools()
    result = client.call("ragamuffin_recall", {"query": "...", "vault": "agent::dev"})
"""

import json
import os
from typing import Any, Dict, List, Optional
from urllib.request import Request, urlopen
from urllib.error import URLError


class MCPError(Exception):
    """Raised when the MCP server returns a JSON-RPC error response."""


class MCPClient:
    """MCP client for Ragamuffin. Thread-safe per-call (no shared state in flight)."""

    def __init__(
        self,
        endpoint: str = "http://localhost:8000",
        auth_token: str = "",
        vault_prefix: str = "agent::",
    ):
        self._endpoint = endpoint.rstrip("/")
        self._auth_token = auth_token
        self._vault_prefix = vault_prefix
        self._tools: Optional[List[Dict[str, Any]]] = None
        self._id_counter = 0

    def vault_name(self, agent_identity: str = "") -> str:
        return f"{self._vault_prefix}{agent_identity or 'default'}"

    def _headers(self) -> Dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self._auth_token:
            h["Authorization"] = f"Bearer {self._auth_token}"
        return h

    def _next_id(self) -> int:
        self._id_counter += 1
        return self._id_counter

    def _request(self, method: str, params: dict = None) -> Any:
        if params is None:
            params = {}
        body = json.dumps({
            "jsonrpc": "2.0",
            "id": self._next_id(),
            "method": method,
            "params": params,
        }).encode()
        req = Request(
            f"{self._endpoint}/mcp",
            data=body,
            headers=self._headers(),
            method="POST",
        )
        try:
            with urlopen(req, timeout=30) as resp:
                data = json.loads(resp.read().decode())
        except URLError as e:
            raise MCPError(f"Connection failed: {e.reason}") from e
        except json.JSONDecodeError as e:
            raise MCPError(f"Invalid JSON response: {e}") from e

        if "error" in data and data["error"] is not None:
            err = data["error"]
            msg = err.get("message", str(err))
            raise MCPError(f"MCP {method}: {msg}")
        return data.get("result")

    def initialize(self) -> dict:
        """Initialize MCP session. Returns protocol version and capabilities."""
        return self._request("initialize")

    def list_tools(self) -> List[Dict[str, Any]]:
        """Fetch and cache all available tools."""
        if self._tools is None:
            result = self._request("tools/list")
            self._tools = result.get("tools", [])
        return self._tools

    @property
    def tools(self) -> List[Dict[str, Any]]:
        """Cached tool list, or empty if list_tools not called yet."""
        return self._tools or []

    def call(self, tool_name: str, args: dict = None) -> Any:
        """Call a tool with named arguments. Returns the tool's result."""
        if args is None:
            args = {}
        return self._request("tools/call", {"name": tool_name, "arguments": args})
