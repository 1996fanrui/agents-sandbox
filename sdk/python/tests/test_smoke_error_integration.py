from __future__ import annotations

import asyncio
from pathlib import Path
import shutil

import agents_sandbox
import pytest

from tests.smoke_support import (
    _ErrorSandboxService,
    _new_client,
    _running_server,
    _running_test_daemon,
    _wait_for_ping,
)


@pytest.mark.parametrize(
    ("reason", "expected_type"),
    [
        ("SANDBOX_CONFLICT", agents_sandbox.SandboxConflictError),
        ("SANDBOX_NOT_FOUND", agents_sandbox.SandboxNotFoundError),
        ("SANDBOX_NOT_READY", agents_sandbox.SandboxNotReadyError),
        ("SANDBOX_INVALID_STATE", agents_sandbox.SandboxInvalidStateError),
        ("EXEC_NOT_FOUND", agents_sandbox.ExecNotFoundError),
        ("EXEC_ALREADY_TERMINAL", agents_sandbox.ExecAlreadyTerminalError),
        ("EXEC_ID_ALREADY_EXISTS", agents_sandbox.SandboxConflictError),
        ("SANDBOX_EVENT_SEQUENCE_EXPIRED", agents_sandbox.SandboxSequenceExpiredError),
    ],
)
def test_sdk_maps_error_info_reasons_to_public_exceptions(
    tmp_path: Path,
    reason: str,
    expected_type: type[Exception],
) -> None:
    servicer = _ErrorSandboxService(reason=reason)

    with _running_server(tmp_path / f"{reason.lower()}.sock", servicer):
        async def run_test() -> None:
            client = _new_client(tmp_path / f"{reason.lower()}.sock")
            if reason in {"EXEC_NOT_FOUND", "EXEC_ALREADY_TERMINAL"}:
                await client.cancel_exec("exec-1")
            elif reason == "EXEC_ID_ALREADY_EXISTS":
                await client.create_exec("sandbox-1", ("echo",), exec_id="exec-1")
            elif reason == "SANDBOX_EVENT_SEQUENCE_EXPIRED":
                async for _event in client.subscribe_sandbox_events("sandbox-1"):
                    raise AssertionError("event stream should fail before yielding")
            else:
                await client.create_sandbox(image="python:3.12-slim", sandbox_id="sandbox-1")

        with pytest.raises(expected_type):
            asyncio.run(run_test())


def test_sdk_can_ping_real_agents_sandbox_over_temp_socket(tmp_path: Path) -> None:
    pytest.skip("real daemon fixed-path startup is covered by cmd package tests in stage 1")
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real AgentsSandbox smoke test")

    socket_path = tmp_path / "runtime" / "agents-sandbox" / "agents-sandbox.sock"
    socket_path.parent.mkdir(parents=True, exist_ok=True)
    with _running_test_daemon(repo_root, socket_path):
        asyncio.run(_wait_for_ping(socket_path))
