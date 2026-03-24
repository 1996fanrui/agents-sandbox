"""SDK error types."""


class SandboxClientError(RuntimeError):
    """Base error for SDK failures."""


class SandboxConflictError(SandboxClientError):
    """Raised when a sandbox owner already has an active sandbox."""


class SandboxNotFoundError(SandboxClientError):
    """Raised when a sandbox does not exist."""


class SandboxNotReadyError(SandboxClientError):
    """Raised when a sandbox is not ready for exec creation."""


class SandboxInvalidStateError(SandboxClientError):
    """Raised when an operation is invalid for the current sandbox state."""


class ExecNotFoundError(SandboxClientError):
    """Raised when an exec does not exist."""


class ExecAlreadyTerminalError(SandboxClientError):
    """Raised when an exec is already terminal."""


class SandboxCursorExpiredError(SandboxClientError):
    """Raised when an event cursor has expired."""
