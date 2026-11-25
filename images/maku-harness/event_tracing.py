"""Metadata-only OpenTelemetry spans generated from normalized harness events (§7.6)."""

ALLOWED_USAGE_KEYS = (
    ("input", "gen_ai.usage.input_tokens"),
    ("output", "gen_ai.usage.output_tokens"),
    ("reasoning", "gen_ai.usage.reasoning_tokens"),
)


class _RecordedSpan:
    """Lightweight in-memory span record used when no OTel SDK provider is wired (tests)."""

    def __init__(self, name: str, attributes: dict):
        self.name = name
        self.attributes = dict(attributes)
        self.ended = False

    def set_attribute(self, key: str, value) -> None:
        self.attributes[key] = value

    def end(self) -> None:
        self.ended = True


class EventTracer:
    """Consumes normalized events and emits metadata-only child spans under root_span.

    Never records prompts, completions, shell commands, tool inputs, or tool outputs.
    """

    def __init__(self, root_span=None, provider=None):
        self.root_span = root_span
        self.provider = provider
        self.finished: list = []
        self._active_tools: dict[str, object] = {}

    def _start_child(self, name: str, attrs: dict):
        if self.provider is not None and self.root_span is not None:
            from opentelemetry import trace

            tracer = self.provider.get_tracer("agent-run.opencode")
            ctx = trace.set_span_in_context(self.root_span)
            span = tracer.start_span(name, context=ctx)
            for k, v in attrs.items():
                span.set_attribute(k, v)
            return span
        return _RecordedSpan(name, attrs)

    def _finish_span(self, span, error_type: str | None = None) -> None:
        if error_type:
            span.set_attribute("error.type", str(error_type)[:64])
            if self.provider is not None:
                from opentelemetry.trace import Status, StatusCode

                span.set_status(Status(StatusCode.ERROR))
        span.end()
        self.finished.append(span)

    def consume(self, event: dict) -> None:
        """Emit metadata-only spans for model steps, tool executions, and errors."""
        if not isinstance(event, dict):
            return
        kind = event.get("kind") or event.get("type") or ""
        if kind in ("step_finish", "MessageEvent") and (event.get("tokens") or event.get("usage")):
            tokens = event.get("tokens") or event.get("usage") or {}
            if not isinstance(tokens, dict):
                return
            attrs: dict = {}
            for src, dst in ALLOWED_USAGE_KEYS:
                val = tokens.get(src)
                if isinstance(val, (int, float)) and val >= 0:
                    attrs[dst] = int(val)
            cache = tokens.get("cache")
            if isinstance(cache, dict):
                if isinstance(cache.get("read"), (int, float)):
                    attrs["gen_ai.usage.cache_read_input_tokens"] = int(cache["read"])
                if isinstance(cache.get("write"), (int, float)):
                    attrs["gen_ai.usage.cache_creation_input_tokens"] = int(cache["write"])
            if isinstance(event.get("finish"), str) and event["finish"]:
                attrs["gen_ai.response.finish_reason"] = event["finish"][:32]
            span = self._start_child("gen_ai.step", attrs)
            self._finish_span(span)
            return

        if kind == "ActionEvent":
            call_id = str(event.get("tool_call_id") or "")
            tool_name = str(event.get("tool_name") or "unknown")[:64]
            span = self._start_child("gen_ai.tool", {"gen_ai.tool.name": tool_name})
            if call_id:
                self._active_tools[call_id] = span
            else:
                self._finish_span(span)
            return

        if kind in ("ObservationEvent", "AgentErrorEvent"):
            call_id = str(event.get("tool_call_id") or "")
            span = self._active_tools.pop(call_id, None)
            if span is None:
                tool_name = str(event.get("tool_name") or "unknown")[:64]
                span = self._start_child("gen_ai.tool", {"gen_ai.tool.name": tool_name})
            err_type = None
            if kind == "AgentErrorEvent" or (event.get("observation") or {}).get("is_error"):
                err_type = "tool_error"
            self._finish_span(span, error_type=err_type)
            return

        if kind == "ConversationErrorEvent":
            code = str(event.get("code") or "conversation_error")[:64]
            span = self._start_child("gen_ai.error", {"error.type": code})
            self._finish_span(span, error_type=code)

    def flush(self) -> None:
        for span in list(self._active_tools.values()):
            self._finish_span(span, error_type="interrupted")
        self._active_tools.clear()
