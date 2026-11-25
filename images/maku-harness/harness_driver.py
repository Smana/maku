"""Abstract driver seam between agent-run's shared lifecycle and a harness server (§6.1)."""
from abc import ABC, abstractmethod

from harness_types import ConversationRequest, EventPage


class HarnessDriver(ABC):
    """Minimal loopback driver interface implemented by OpenHandsDriver and OpenCodeDriver."""

    @abstractmethod
    def start(self) -> None:
        """Start the loopback harness server process."""

    @abstractmethod
    def wait_ready(self, deadline_s: int = 120) -> None:
        """Wait until the loopback harness server answers readiness or raise if it exits."""

    @abstractmethod
    def create_conversation(self, request: ConversationRequest) -> str:
        """Create or reattach the run's conversation and return its conversation/session ID."""

    @abstractmethod
    def status(self, conversation_id: str) -> str:
        """Return the normalized execution status (idle, running, paused, waiting_for_confirmation, finished, error, stuck)."""

    @abstractmethod
    def events(self, conversation_id: str, cursor: str = "") -> EventPage:
        """Return the next page of normalized events after cursor."""

    @abstractmethod
    def send(self, conversation_id: str, text: str, run: bool = True) -> None:
        """Append a user message to the conversation and optionally start a turn."""

    @abstractmethod
    def interrupt(self, conversation_id: str, timeout: float = 10) -> None:
        """Interrupt the active turn and owned jobs."""

    @abstractmethod
    def close(self, conversation_id: str) -> None:
        """Close the conversation and flush harness-owned telemetry."""

    @abstractmethod
    def stop(self, timeout: float = 10) -> str:
        """Terminate the harness server within timeout, killing if needed."""

    def persisted(self) -> bool:
        """True when a suspendable run's conversation state already exists on /workspace."""
        return False

    def search_available(self) -> bool:
        """True when this driver can serve the run's bounded search tool."""
        return False
