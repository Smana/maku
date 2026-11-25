"""Generate least-privilege OpenCode 2.4.1 V2 configuration per Maku role (design §7.1, §7.4)."""

DEFAULT_ALIASES = (
    "agent-default",
    "tier-light",
    "tier-standard",
    "tier-frontier",
    "kimi-frontier",
    "mistral-standard",
    "mistral-light",
)

EFFORT_VARIANTS = ("low", "medium", "high")

# Rules denied for every role unless a dedicated future spec enables them.
BASE_DENY_RULES = (
    {"action": "subagent", "resource": "*", "effect": "deny"},
    {"action": "skill", "resource": "*", "effect": "deny"},
    {"action": "webfetch", "resource": "*", "effect": "deny"},
    {"action": "websearch", "resource": "*", "effect": "deny"},
    {"action": "external_directory", "resource": "*", "effect": "deny"},
)


def _model_entry(alias: str) -> dict:
    return {
        "modelID": alias,
        "name": alias,
        "variants": [
            {"id": effort, "body": {"reasoning_effort": effort}}
            for effort in EFFORT_VARIANTS
        ],
    }


def _role_permissions(role: str, *, test_paths: list[str] | None = None, always_ask: bool = False) -> list[dict]:
    """Build ordered OpenCode V2 permission rules (last matching rule wins)."""
    if role == "implementer":
        rules = [
            {"action": "read", "resource": "*", "effect": "allow"},
            {"action": "glob", "resource": "*", "effect": "allow"},
            {"action": "grep", "resource": "*", "effect": "allow"},
            {"action": "list", "resource": "*", "effect": "allow"},
            {"action": "edit", "resource": "*", "effect": "allow"},
            {"action": "write", "resource": "*", "effect": "allow"},
            {"action": "patch", "resource": "*", "effect": "allow"},
            {"action": "shell", "resource": "*", "effect": "ask" if always_ask else "allow"},
            {"action": "shell", "resource": "git push*", "effect": "ask"},
            {"action": "shell", "resource": "gh pr*", "effect": "ask"},
        ]
        return rules + list(BASE_DENY_RULES)

    if role in ("reviewer", "triager"):
        rules = [
            {"action": "read", "resource": "*", "effect": "allow"},
            {"action": "glob", "resource": "*", "effect": "allow"},
            {"action": "grep", "resource": "*", "effect": "allow"},
            {"action": "list", "resource": "*", "effect": "allow"},
            {"action": "edit", "resource": "*", "effect": "deny"},
            {"action": "write", "resource": "*", "effect": "deny"},
            {"action": "patch", "resource": "*", "effect": "deny"},
            {"action": "shell", "resource": "*", "effect": "ask"},
            {"action": "shell", "resource": "agent-checks*", "effect": "allow"},
            {"action": "shell", "resource": "git diff*", "effect": "allow"},
            {"action": "shell", "resource": "git log*", "effect": "allow"},
            {"action": "shell", "resource": "git status*", "effect": "allow"},
            {"action": "shell", "resource": "git show*", "effect": "allow"},
        ]
        return rules + list(BASE_DENY_RULES)

    if role == "tester":
        rules = [
            {"action": "read", "resource": "*", "effect": "allow"},
            {"action": "glob", "resource": "*", "effect": "allow"},
            {"action": "grep", "resource": "*", "effect": "allow"},
            {"action": "list", "resource": "*", "effect": "allow"},
            {"action": "edit", "resource": "*", "effect": "deny"},
            {"action": "write", "resource": "*", "effect": "deny"},
            {"action": "patch", "resource": "*", "effect": "deny"},
            {"action": "shell", "resource": "*", "effect": "ask" if always_ask else "allow"},
            {"action": "shell", "resource": "git push*", "effect": "deny"},
        ]
        for pattern in test_paths or ():
            clean = str(pattern).strip()
            if clean:
                rules.append({"action": "edit", "resource": clean, "effect": "allow"})
                rules.append({"action": "write", "resource": clean, "effect": "allow"})
                rules.append({"action": "patch", "resource": clean, "effect": "allow"})
        return rules + list(BASE_DENY_RULES)

    if role == "search":
        return [
            {"action": "read", "resource": "*", "effect": "allow"},
            {"action": "glob", "resource": "*", "effect": "allow"},
            {"action": "grep", "resource": "*", "effect": "allow"},
            {"action": "list", "resource": "*", "effect": "allow"},
            {"action": "edit", "resource": "*", "effect": "deny"},
            {"action": "write", "resource": "*", "effect": "deny"},
            {"action": "patch", "resource": "*", "effect": "deny"},
            {"action": "shell", "resource": "*", "effect": "deny"},
            *BASE_DENY_RULES,
        ]

    # Fail closed for any unrecognized role.
    return [
        {"action": "*", "resource": "*", "effect": "deny"},
        *BASE_DENY_RULES,
    ]


def role_config(
    env: dict,
    *,
    rules: str = "",
    mcp_proxy_url: str | None = None,
    test_paths: list[str] | None = None,
) -> dict:
    """Build a fresh OpenCode 2.3.0 V2 opencode.jsonc dictionary.

    - No secret appears anywhere in the generated config (credentials flow via env).
    - No personal config, auto-update, share, or unapproved plugins are enabled.
    - Internal runs enable Anthropic prompt-cache compatibility; public runs omit it.
    """
    role = (env.get("ROLE") or "implementer").strip()
    model = (env.get("MODEL") or "agent-default").strip()
    effort = (env.get("LLM_REASONING_EFFORT") or "high").strip().lower()
    if effort not in EFFORT_VARIANTS:
        effort = "high"
    base_url = (env.get("LLM_BASE_URL") or "http://127.0.0.1:4000/v1").strip()
    data_class = (env.get("DATA_CLASS") or "public").strip().lower()
    always_ask = env.get("OPENCODE_ALWAYS_ASK") == "1"

    aliases = list(DEFAULT_ALIASES)
    for extra in (model, (env.get("LIGHT_MODEL") or "").strip()):
        if extra and extra not in aliases:
            aliases.append(extra)

    provider: dict = {
        "name": "Maku Gateway",
        "package": "@ai-sdk/openai-compatible",
        "env": ["MAKU_LLM_API_KEY"],
        "settings": {"baseURL": base_url},
        "models": {alias: _model_entry(alias) for alias in aliases},
    }
    if data_class == "internal":
        provider["compatibility"] = {"cacheFormat": "anthropic"}

    providers = {"maku": provider}

    light_base = (env.get("LIGHT_BASE_URL") or "").strip()
    light_model = (env.get("LIGHT_MODEL") or "").strip()
    if light_base and light_model:
        light_provider: dict = {
            "name": "Maku Light Gateway",
            "package": "@ai-sdk/openai-compatible",
            "env": ["MAKU_LIGHT_API_KEY" if env.get("LIGHT_API_KEY_FILE") else "MAKU_LLM_API_KEY"],
            "settings": {"baseURL": light_base},
            "models": {light_model: _model_entry(light_model)},
        }
        if data_class == "internal":
            light_provider["compatibility"] = {"cacheFormat": "anthropic"}
        providers["maku_light"] = light_provider
        search_model = f"maku_light/{light_model}#low"
    else:
        search_model = f"maku/{model}#low"

    agents: dict = {}
    for agent_name in ("implementer", "reviewer", "tester", "triager"):
        entry: dict = {
            "mode": "primary",
            "permissions": _role_permissions(
                agent_name,
                test_paths=test_paths if agent_name == "tester" else None,
                always_ask=always_ask,
            ),
        }
        if agent_name == role and rules:
            entry["system"] = rules
        agents[agent_name] = entry

    agents["search"] = {
        "mode": "primary",
        "model": search_model,
        "steps": 40,
        "permissions": _role_permissions("search"),
    }

    cfg: dict = {
        "update": "disable",
        "share": "disabled",
        "automations": False,
        "model": f"maku/{model}#{effort}",
        "default_agent": role if role in agents else "implementer",
        "providers": providers,
        "agents": agents,
        "plugins": [],
    }
    if mcp_proxy_url:
        cfg["mcp"] = {
            "servers": {
                "platform": {
                    "type": "remote",
                    "url": mcp_proxy_url,
                    "oauth": False,
                    "codemode": False,
                }
            }
        }
    return cfg
