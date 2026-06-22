"""CLI commands for Ragamuffin integration management.

Handles: hermes ragamuffin setup | status | doctor
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Any, Dict, Optional


# ---------------------------------------------------------------------------
# Config path helpers
# ---------------------------------------------------------------------------


def _config_path() -> Path:
    """Return the path to the profile-scoped ``ragamuffin.json``."""
    hermes_home = os.environ.get("HERMES_HOME", str(Path.home() / ".hermes"))
    return Path(hermes_home) / "ragamuffin.json"


def _read_config() -> dict:
    """Read the current ragamuffin config (empty dict if missing)."""
    path = _config_path()
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text())
    except (json.JSONDecodeError, OSError):
        return {}


def _write_config(cfg: dict) -> None:
    """Atomically write config via temp+rename."""
    path = _config_path()
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(cfg, indent=2, sort_keys=True) + "\n")
    tmp.chmod(0o600)
    tmp.replace(path)


# ---------------------------------------------------------------------------
# CLI subcommand registration
# ---------------------------------------------------------------------------


def register_cli(subparser) -> None:
    """Register the ``hermes ragamuffin`` subcommand group.

    Called by Hermes plugin loader via ``register()``.
    """
    parser = subparser.add_parser(
        "ragamuffin",
        help="Configure and manage Ragamuffin memory",
        description="Ragamuffin-backed agent memory setup, status checks, and diagnostics.",
    )
    parser.set_defaults(func=lambda args: parser.print_help())

    subs = parser.add_subparsers(title="subcommands", dest="subcommand")

    # --- setup ---
    setup_parser = subs.add_parser(
        "setup",
        help="Configure Ragamuffin connection",
        description=(
            "Write connection settings to $HERMES_HOME/ragamuffin.json. "
            "Any values provided here override defaults but are themselves "
            "overridden by environment variables at runtime."
        ),
    )
    setup_parser.add_argument(
        "--endpoint",
        default="http://ragamuffin:8000",
        help="Ragamuffin server URL (default: http://ragamuffin:8000)",
    )
    setup_parser.add_argument(
        "--auth-token",
        default="",
        help="Ragamuffin auth token (optional)",
    )
    setup_parser.add_argument(
        "--vault-prefix",
        default="agent::",
        help="Prefix for agent vault names (default: agent::)",
    )
    setup_parser.add_argument(
        "--recall-mode",
        default="hybrid",
        choices=["hybrid", "context", "tools"],
        help="Recall mode: hybrid, context, or tools (default: hybrid)",
    )
    setup_parser.add_argument(
        "--save-messages",
        default=True,
        type=lambda x: x.lower() in ("true", "1", "yes"),
        help="Save messages to memory (default: true)",
    )
    setup_parser.add_argument(
        "--context-cadence",
        default=3,
        type=int,
        help="Refresh base context every N turns (default: 3, 0=disable)",
    )
    setup_parser.add_argument(
        "--dialectic-cadence",
        default=5,
        type=int,
        help="Refresh dialectic every N turns (default: 5, 0=disable)",
    )
    setup_parser.set_defaults(func=cmd_setup)

    # --- status ---
    status_parser = subs.add_parser(
        "status",
        help="Check Ragamuffin connection status",
        description="Display current config and health check result.",
    )
    status_parser.set_defaults(func=cmd_status)

    # --- doctor ---
    doctor_parser = subs.add_parser(
        "doctor",
        help="Run diagnostics on Ragamuffin setup",
        description="Validate config, check connectivity, and report issues.",
    )
    doctor_parser.set_defaults(func=cmd_doctor)


# ---------------------------------------------------------------------------
# Command handlers
# ---------------------------------------------------------------------------


def cmd_setup(args) -> None:
    """Write Ragamuffin config to ``ragamuffin.json``."""
    cfg = _read_config()
    cfg["endpoint"] = args.endpoint
    if args.auth_token:
        cfg["auth_token"] = args.auth_token
    cfg["vault_prefix"] = args.vault_prefix
    cfg["recall_mode"] = args.recall_mode
    cfg["save_messages"] = args.save_messages
    cfg["context_cadence"] = args.context_cadence
    cfg["dialectic_cadence"] = args.dialectic_cadence

    _write_config(cfg)
    config_path = _config_path()
    print(f"  Ragamuffin configuration saved.")
    print(f"  File: {config_path}")
    print()
    print(f"  endpoint:        {cfg['endpoint']}")
    print(f"  auth_token:      {'****' if cfg.get('auth_token') else '(not set)'}")
    print(f"  vault_prefix:    {cfg['vault_prefix']}")
    print(f"  recall_mode:     {cfg['recall_mode']}")
    print(f"  save_messages:   {cfg['save_messages']}")
    print(f"  context_cadence: {cfg['context_cadence']}")
    print(f"  dialectic_cadence: {cfg['dialectic_cadence']}")
    print()
    print("  Environment variables (RAGAMUFFIN_*) override these values at runtime.")


def cmd_status(args) -> None:
    """Display current config and health."""
    cfg = _read_config()
    config_path = _config_path()

    print("  Ragamuffin Status")
    print(f"  {'─' * 40}")
    print()

    if not cfg:
        print("  ⚠  No ragamuffin.json found. Run 'hermes ragamuffin setup' first.")
        print(f"  Expected at: {config_path}")
        return

    print(f"  Config file: {config_path}")
    print(f"  Endpoint:    {cfg.get('endpoint', '(not set)')}")
    print(f"  Vault prefix: {cfg.get('vault_prefix', '(not set)')}")
    print(f"  Recall mode: {cfg.get('recall_mode', '(not set)')}")
    print()

    # Check the env-var overrides
    env_vars = {
        "RAGAMUFFIN_ENDPOINT": os.environ.get("RAGAMUFFIN_ENDPOINT", ""),
        "RAGAMUFFIN_AUTH_TOKEN": "****" if os.environ.get("RAGAMUFFIN_AUTH_TOKEN") else "",
        "RAGAMUFFIN_VAULT_PREFIX": os.environ.get("RAGAMUFFIN_VAULT_PREFIX", ""),
    }
    active_overrides = {k: v for k, v in env_vars.items() if v}
    if active_overrides:
        print("  Active env overrides:")
        for k, v in active_overrides.items():
            print(f"    {k}={v}")
    else:
        print("  No env overrides active.")


def cmd_doctor(args) -> None:
    """Run validation checks on the Ragamuffin setup."""
    cfg = _read_config()
    issues: list[str] = []
    warnings: list[str] = []
    info: list[str] = []

    config_path = _config_path()

    # 1. Config file exists
    if not cfg:
        issues.append("No ragamuffin.json found. Run 'hermes ragamuffin setup'.")
    else:
        info.append(f"Config found at {config_path}")

    # 2. Endpoint configured
    endpoint = cfg.get("endpoint") or os.environ.get("RAGAMUFFIN_ENDPOINT", "")
    if not endpoint:
        issues.append("No Ragamuffin endpoint configured.")
    else:
        info.append(f"Endpoint: {endpoint}")

    # 3. Endpoint reachable (via DNS check since we can't make HTTP)
    if endpoint and "://" in endpoint:
        import urllib.parse
        hostname = urllib.parse.urlparse(endpoint).hostname
        import socket
        try:
            socket.getaddrinfo(hostname, 8000)
            info.append(f"DNS resolution OK for {hostname}")
        except socket.gaierror:
            warnings.append(f"Cannot resolve hostname: {hostname}")

    # 4. Vault prefix
    vault_prefix = cfg.get("vault_prefix") or os.environ.get("RAGAMUFFIN_VAULT_PREFIX", "agent::")
    info.append(f"Vault prefix: {vault_prefix}")

    # 5. Recall mode validation
    recall_mode = cfg.get("recall_mode") or os.environ.get("RAGAMUFFIN_RECALL_MODE", "hybrid")
    valid_modes = ["hybrid", "context", "tools"]
    if recall_mode not in valid_modes:
        warnings.append(f"Invalid recall_mode '{recall_mode}'. Valid: {', '.join(valid_modes)}")

    # 6. Cadence sanity
    context_cadence = cfg.get("context_cadence", 3)
    if context_cadence < 0:
        warnings.append(f"context_cadence ({context_cadence}) is negative. Using default 3.")
    dialectic_cadence = cfg.get("dialectic_cadence", 5)
    if dialectic_cadence < 0:
        warnings.append(f"dialectic_cadence ({dialectic_cadence}) is negative. Using default 5.")

    # 7. Known limitations check
    if recall_mode == "tools":
        warnings.append("Sessions API may be unavailable — 503 errors are a known limitation.")

    # Summary
    print(f"  Ragamuffin Doctor — Diagnostics")
    print(f"  {'─' * 50}")
    print()

    if issues:
        print(f"  ❌ ISSUES ({len(issues)}):")
        for issue in issues:
            print(f"     • {issue}")
        print()

    if warnings:
        print(f"  ⚠  WARNINGS ({len(warnings)}):")
        for w in warnings:
            print(f"     • {w}")
        print()

    if info:
        print(f"  ℹ  Info:")
        for i in info:
            print(f"     • {i}")
        print()

    if not issues and not warnings:
        print("  ✅ All checks passed.")
    elif not issues:
        print("  ⚠  Resolve warnings for optimal setup.")
    else:
        print("  ❌ Fix issues before use.")
