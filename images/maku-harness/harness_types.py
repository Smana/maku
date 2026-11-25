"""Shared request and event types for Maku harness drivers (OpenCode design §6.1)."""
from dataclasses import dataclass, field


@dataclass
class ConversationRequest:
    """Inputs agent-run passes to HarnessDriver.create_conversation."""

    env: dict
    task: str
    rules: str
    span: object = None
    search: bool = False
    resumed: bool = False


@dataclass
class EventPage:
    """One page of normalized harness events and its continuation cursor."""

    items: list[dict] = field(default_factory=list)
    next_cursor: str = ""
