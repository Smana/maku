"""The opt-in browser sidecar's harness side (v5 Q16): one MCP server, image input and the rules.

The run-controller sets BROWSER_MCP_URL (the gate on the pod's loopback) on a run whose Repository
opted in, and SCREENSHOTS_VISION=1 when the run's model accepts images. Both drivers read them here.
"""
from urllib.parse import urlparse

_LOOPBACK = {"127.0.0.1", "localhost", "::1"}


def browser_url(env) -> "str | None":
    """The gate's MCP URL, or None: only a plain-http loopback URL is ever used."""
    url = (env.get("BROWSER_MCP_URL") or "").strip()
    if not url:
        return None
    try:
        u = urlparse(url)
    except ValueError:
        return None
    if u.scheme != "http" or u.username is not None or u.password is not None or u.hostname not in _LOOPBACK:
        return None
    return url


def vision_on(env) -> bool:
    """Whether the run's model receives screenshots: the browser is on and the controller said so."""
    return browser_url(env) is not None and env.get("SCREENSHOTS_VISION") == "1"


def mcp_server(env) -> "dict | None":
    """The OpenHands mcp_config entry named browser, or None."""
    url = browser_url(env)
    return {"url": url, "transport": "http"} if url else None


def browser_rules(env) -> str:
    """The rules section for the browser, empty when the run has none."""
    if browser_url(env) is None:
        return ""
    lines = [
        "",
        "Browser (loopback only):",
        "- A headless browser can open pages served inside this sandbox, and nothing else. Start the dev",
        "  server bound to loopback, for example `hugo server --bind 127.0.0.1 --port 1313` or",
        "  `npm run dev -- --host 127.0.0.1`, then open http://127.0.0.1:<port>/ with browser_navigate.",
        "- Use browser_snapshot (the page's accessibility tree) to check structure and text first.",
        "- Use browser_take_screenshot to check layout, colour or styling; browser_emulate_media switches",
        "  dark mode. You have a small fixed number of screenshots. Each one is also shown to the humans",
        "  in the room, so take one of the result you want them to see.",
    ]
    if not vision_on(env):
        lines.append("- Your model cannot see images: a screenshot answers with text only. Rely on browser_snapshot.")
    lines.append("- Stop the dev server before you finish.")
    return "\n".join(lines) + "\n"
