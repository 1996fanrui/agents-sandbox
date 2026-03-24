from __future__ import annotations

import os
import subprocess
import time
from pathlib import Path

import pytest

from agents_sandbox import SandboxClient, SandboxConflictError
from agents_sandbox._generated import service_pb2


def test_sdk_smoke_against_local_daemon(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    socket_path = tmp_path / "agboxd.sock"
    lock_root = Path("/run/agbox")
    if lock_root.exists():
        if not os.access(lock_root, os.W_OK):
            pytest.skip(f"host lock directory is not writable: {lock_root}")
    elif not os.access(lock_root.parent, os.W_OK):
        pytest.skip(f"host lock directory parent is not writable: {lock_root.parent}")
    process = subprocess.Popen(
        ["go", "run", "./cmd/agboxd", "--socket", str(socket_path)],
        cwd=repo_root,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    try:
        wait_for_daemon(process, socket_path)
        with SandboxClient(str(socket_path)) as client:
            ping = client.ping()
            assert ping.version == "0.1.0"
            assert ping.daemon == "agboxd"

            create_response = client.create_sandbox(
                service_pb2.CreateSandboxRequest(
                    sandbox_owner=service_pb2.SandboxOwner(
                        product="aihub",
                        owner_type="session",
                        owner_id="session-1",
                    ),
                    create_spec=service_pb2.CreateSpec(
                        dependencies=[
                            service_pb2.DependencySpec(
                                dependency_name="db",
                                image="postgres:16",
                            )
                        ]
                    ),
                )
            )
            assert create_response.initial_state == service_pb2.SANDBOX_STATE_PENDING

            events = client.subscribe_sandbox_events(create_response.sandbox_id)
            ready_event = None
            for _ in range(4):
                event = next(events)
                ready_event = event
                if event.event_type == service_pb2.SANDBOX_READY:
                    break
            assert ready_event is not None
            assert ready_event.event_type == service_pb2.SANDBOX_READY

            with pytest.raises(SandboxConflictError):
                client.create_sandbox(
                    service_pb2.CreateSandboxRequest(
                        sandbox_owner=service_pb2.SandboxOwner(
                            product="aihub",
                            owner_type="session",
                            owner_id="session-1",
                        )
                    )
                )

            exec_response = client.create_exec(
                service_pb2.CreateExecRequest(
                    sandbox_id=create_response.sandbox_id,
                    command=["echo", "hello"],
                    cwd="/workspace",
                )
            )
            client.start_exec(exec_response.exec_id)
            terminal_event = None
            for _ in range(3):
                event = next(events)
                terminal_event = event
                if event.event_type == service_pb2.EXEC_FINISHED:
                    break
            assert terminal_event is not None
            assert terminal_event.event_type == service_pb2.EXEC_FINISHED

            exec_status = client.get_exec(exec_response.exec_id)
            assert exec_status.exec.state == service_pb2.EXEC_STATE_FINISHED

            sandboxes = client.list_sandboxes()
            assert [sandbox.sandbox_id for sandbox in sandboxes.sandboxes] == [create_response.sandbox_id]
    finally:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)


def wait_for_daemon(process: subprocess.Popen[str], socket_path: Path) -> None:
    deadline = time.time() + 10
    while time.time() < deadline:
        if process.poll() is not None:
            _, stderr = process.communicate()
            raise AssertionError(
                f"daemon exited before creating socket {socket_path}: {stderr.strip()}"
            )
        if socket_path.exists():
            return
        time.sleep(0.1)
    raise AssertionError(f"daemon socket was not created: {socket_path}")
