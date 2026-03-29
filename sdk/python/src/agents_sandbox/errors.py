"""SDK error types."""

from __future__ import annotations


class SandboxClientError(RuntimeError):
    """Base error for SDK failures."""


class SandboxConflictError(SandboxClientError):
    """Raised when a caller-provided ID already exists."""

    def __init__(self, sandbox_id: str | None = None, *, message: str | None = None):
        self.sandbox_id = sandbox_id
        super().__init__(message or (f"Sandbox already exists for id {sandbox_id}." if sandbox_id else "Sandbox already exists."))


class SandboxNotFoundError(SandboxClientError):
    """Raised when a sandbox does not exist."""

    def __init__(self, sandbox_id: str | None = None, *, message: str | None = None):
        self.sandbox_id = sandbox_id
        super().__init__(message or (f"Sandbox {sandbox_id} not found." if sandbox_id else "Sandbox not found."))


class SandboxNotReadyError(SandboxClientError):
    """Raised when a sandbox is not ready for exec creation."""

    def __init__(self, sandbox_id: str | None = None, *, message: str | None = None):
        self.sandbox_id = sandbox_id
        super().__init__(message or (f"Sandbox {sandbox_id} is not ready." if sandbox_id else "Sandbox is not ready."))


class SandboxInvalidStateError(SandboxClientError):
    """Raised when an operation is invalid for the current sandbox state."""


class ExecNotFoundError(SandboxClientError):
    """Raised when an exec does not exist."""

    def __init__(self, exec_id: str | None = None, *, message: str | None = None):
        self.exec_id = exec_id
        super().__init__(message or (f"Exec {exec_id} not found." if exec_id else "Exec not found."))


class ExecAlreadyTerminalError(SandboxClientError):
    """Raised when an exec is already terminal."""

    def __init__(self, exec_id: str | None = None, *, message: str | None = None):
        self.exec_id = exec_id
        super().__init__(message or (f"Exec {exec_id} is already terminal." if exec_id else "Exec is already terminal."))


class ExecNotRunningError(SandboxInvalidStateError):
    """Raised when an exec is no longer running."""

    def __init__(self, exec_id: str | None = None, *, message: str | None = None):
        self.exec_id = exec_id
        super().__init__(message or (f"Exec {exec_id} is not running." if exec_id else "Exec is not running."))


class SandboxSequenceExpiredError(SandboxClientError):
    """Raised when an event sequence anchor has expired."""

    def __init__(self, sandbox_id: str | None = None, from_sequence: int | None = None, oldest_sequence: int | None = None, *, message: str | None = None):
        self.sandbox_id = sandbox_id
        self.from_sequence = from_sequence
        self.oldest_sequence = oldest_sequence
        if message:
            super().__init__(message)
        elif sandbox_id and from_sequence is not None:
            msg = f"Sandbox {sandbox_id} event sequence {from_sequence} expired"
            if oldest_sequence is not None:
                msg += f"; oldest retained sequence is {oldest_sequence}"
            msg += "."
            super().__init__(msg)
        else:
            super().__init__("Event sequence expired.")
