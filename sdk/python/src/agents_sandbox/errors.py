"""SDK error types."""

from __future__ import annotations


class SandboxClientError(RuntimeError):
    """Base error for SDK failures."""


class SandboxConflictError(SandboxClientError):
    """Raised when a caller-provided ID already exists."""

    def __init__(self, sandbox_id: str | None = None):
        if sandbox_id is None:
            message = "Sandbox already exists."
        else:
            message = sandbox_id
        super().__init__(message)
        self.sandbox_id = sandbox_id


class SandboxNotFoundError(SandboxClientError):
    """Raised when a sandbox does not exist."""

    def __init__(self, sandbox_id: str):
        if " " in sandbox_id:
            message = sandbox_id
            self.sandbox_id = None
        else:
            message = f"Sandbox {sandbox_id} not found."
            self.sandbox_id = sandbox_id
        super().__init__(message)


class SandboxNotReadyError(SandboxClientError):
    """Raised when a sandbox is not ready for exec creation."""

    def __init__(self, sandbox_id: str):
        if " " in sandbox_id:
            message = sandbox_id
            self.sandbox_id = None
        else:
            message = f"Sandbox {sandbox_id} is not ready."
            self.sandbox_id = sandbox_id
        super().__init__(message)


class SandboxInvalidStateError(SandboxClientError):
    """Raised when an operation is invalid for the current sandbox state."""


class ExecNotFoundError(SandboxClientError):
    """Raised when an exec does not exist."""

    def __init__(self, exec_id: str):
        if " " in exec_id:
            message = exec_id
            self.exec_id = None
        else:
            message = f"Exec {exec_id} not found."
            self.exec_id = exec_id
        super().__init__(message)


class ExecAlreadyTerminalError(SandboxClientError):
    """Raised when an exec is already terminal."""

    def __init__(self, exec_id: str):
        if " " in exec_id:
            message = exec_id
            self.exec_id = None
        else:
            message = f"Exec {exec_id} is already terminal."
            self.exec_id = exec_id
        super().__init__(message)


class ExecNotRunningError(SandboxInvalidStateError):
    """Raised when an exec is no longer running."""

    def __init__(self, exec_id: str):
        if " " in exec_id:
            message = exec_id
            self.exec_id = None
        else:
            message = f"Exec {exec_id} is not running."
            self.exec_id = exec_id
        super().__init__(message)


class SandboxSequenceExpiredError(SandboxClientError):
    """Raised when an event sequence anchor has expired."""

    def __init__(self, sandbox_id: str, from_sequence: int | None = None, oldest_sequence: int | None = None):
        if from_sequence is None or oldest_sequence is None:
            message = sandbox_id
            self.sandbox_id = None
            self.from_sequence = None
            self.oldest_sequence = None
        else:
            message = (
                f"Sandbox {sandbox_id} event sequence {from_sequence} expired; "
                f"oldest retained sequence is {oldest_sequence}."
            )
            self.sandbox_id = sandbox_id
            self.from_sequence = from_sequence
            self.oldest_sequence = oldest_sequence
        super().__init__(message)
