"""Public async Python client built on top of the protobuf transport."""

from __future__ import annotations

import asyncio
import os
import platform
from collections.abc import AsyncIterator, Callable, Mapping, Sequence
from threading import Event as ThreadEvent
from threading import Thread
from typing import cast

from ._generated import service_pb2
from ._grpc_client import SandboxGrpcClient
from .conversions import (
    normalize_from_sequence,
    parse_event_sequence,
    to_exec_handle,
    to_exec_snapshot,
    to_ping_info,
    to_proto_create_exec_request,
    to_proto_create_sandbox_request,
    to_sandbox_event,
    to_sandbox_handle,
)
from .errors import (
    ExecNotRunningError,
    SandboxClientError,
    SandboxConflictError,
    SandboxInvalidStateError,
)
from .models import SandboxState
from .types import (
    CopySpec,
    CreateExecRequest,
    CreateSandboxRequest,
    CreateSandboxSpec,
    DeleteSandboxesResult,
    ExecHandle,
    MountSpec,
    PingInfo,
    SandboxEvent,
    SandboxHandle,
    ServiceSpec,
)


def _resolve_default_socket_path(
    *,
    system: str | None = None,
    lookup_env: Callable[[str], str | None] | None = None,
    home_dir: str | None = None,
) -> str:
    lookup = os.environ.get if lookup_env is None else lookup_env
    resolved_system = platform.system() if system is None else system
    if resolved_system == "Darwin":
        resolved_home = home_dir or os.path.expanduser("~")
        if resolved_home:
            return os.path.join(
                resolved_home,
                "Library",
                "Application Support",
                "agbox",
                "run",
                "agboxd.sock",
            )
    else:
        runtime_dir = lookup("XDG_RUNTIME_DIR")
        if runtime_dir:
            return os.path.join(runtime_dir, "agbox", "agboxd.sock")
        raise RuntimeError("XDG_RUNTIME_DIR is required to resolve the AgentsSandbox socket path on Linux")


def _validate_optional_id(field_name: str, value: str | None) -> str | None:
    if value == "":
        raise ValueError(f"{field_name} must not be empty")
    return value


class AgentsSandboxClient:
    """Async high-level client that exposes the northbound SDK surface."""

    def __init__(
        self,
        *,
        timeout_seconds: float = 5.0,
        stream_timeout_seconds: float | None = None,
        operation_timeout_seconds: float = 60.0,
    ) -> None:
        self.socket_path = _resolve_default_socket_path()
        self._timeout_seconds = timeout_seconds
        self._stream_timeout_seconds = stream_timeout_seconds
        self._operation_timeout_seconds = operation_timeout_seconds
        self._rpc_client = SandboxGrpcClient(self.socket_path, timeout_seconds=timeout_seconds)

    def close(self) -> None:
        self._rpc_client.close()

    async def __aenter__(self) -> AgentsSandboxClient:
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        del exc_type, exc, tb
        self.close()

    async def ping(self) -> PingInfo:
        response = await asyncio.to_thread(self._rpc_client.ping)
        return to_ping_info(response)

    async def create_sandbox(
        self,
        image: str,
        *,
        sandbox_id: str | None = None,
        mounts: tuple[MountSpec, ...] = (),
        copies: tuple[CopySpec, ...] = (),
        builtin_resources: tuple[str, ...] = (),
        required_services: tuple[ServiceSpec, ...] = (),
        optional_services: tuple[ServiceSpec, ...] = (),
        labels: Mapping[str, str] | None = None,
        wait: bool = True,
    ) -> SandboxHandle:
        request = CreateSandboxRequest(
            sandbox_id=_validate_optional_id("sandbox_id", sandbox_id),
            create_spec=CreateSandboxSpec(
                image=image,
                mounts=mounts,
                copies=copies,
                builtin_resources=builtin_resources,
                required_services=required_services,
                optional_services=optional_services,
                labels={} if labels is None else dict(labels),
            ),
        )
        try:
            response = await asyncio.to_thread(
                self._rpc_client.create_sandbox,
                to_proto_create_sandbox_request(request),
            )
        except SandboxConflictError as exc:
            if request.sandbox_id is None:
                raise
            raise SandboxConflictError(f"Sandbox already exists for id {request.sandbox_id}.") from exc
        current = await self.get_sandbox(response.sandbox_id)
        if not wait:
            return current
        return await self._wait_for_sandbox_state(
            sandbox_id=response.sandbox_id,
            baseline=current,
            target_state=SandboxState.READY,
            operation_name="create_sandbox",
        )

    async def get_sandbox(self, sandbox_id: str) -> SandboxHandle:
        response = await asyncio.to_thread(self._rpc_client.get_sandbox, sandbox_id)
        return to_sandbox_handle(response.sandbox)

    async def list_sandboxes(
        self,
        *,
        include_deleted: bool = False,
        label_selector: Mapping[str, str] | None = None,
    ) -> list[SandboxHandle]:
        response = await asyncio.to_thread(
            self._rpc_client.list_sandboxes,
            service_pb2.ListSandboxesRequest(
                include_deleted=include_deleted,
                label_selector={} if label_selector is None else dict(label_selector),
            ),
        )
        return [to_sandbox_handle(item) for item in response.sandboxes]

    async def resume_sandbox(
        self,
        sandbox_id: str,
        *,
        wait: bool = True,
    ) -> SandboxHandle:
        await asyncio.to_thread(self._rpc_client.resume_sandbox, sandbox_id)
        current = await self.get_sandbox(sandbox_id)
        if not wait:
            return current
        return await self._wait_for_sandbox_state(
            sandbox_id=sandbox_id,
            baseline=current,
            target_state=SandboxState.READY,
            operation_name="resume_sandbox",
        )

    async def stop_sandbox(
        self,
        sandbox_id: str,
        *,
        wait: bool = True,
    ) -> SandboxHandle:
        await asyncio.to_thread(self._rpc_client.stop_sandbox, sandbox_id)
        current = await self.get_sandbox(sandbox_id)
        if not wait:
            return current
        return await self._wait_for_sandbox_state(
            sandbox_id=sandbox_id,
            baseline=current,
            target_state=SandboxState.STOPPED,
            operation_name="stop_sandbox",
        )

    async def delete_sandbox(
        self,
        sandbox_id: str,
        *,
        wait: bool = True,
    ) -> SandboxHandle:
        await asyncio.to_thread(self._rpc_client.delete_sandbox, sandbox_id)
        current = await self.get_sandbox(sandbox_id)
        if not wait:
            return current
        return await self._wait_for_sandbox_state(
            sandbox_id=sandbox_id,
            baseline=current,
            target_state=SandboxState.DELETED,
            operation_name="delete_sandbox",
        )

    async def delete_sandboxes(
        self,
        label_selector: Mapping[str, str],
        *,
        wait: bool = True,
    ) -> DeleteSandboxesResult:
        response = await asyncio.to_thread(
            self._rpc_client.delete_sandboxes,
            service_pb2.DeleteSandboxesRequest(label_selector=dict(label_selector)),
        )
        result = DeleteSandboxesResult(
            deleted_sandbox_ids=tuple(response.deleted_sandbox_ids),
            deleted_count=response.deleted_count,
        )
        if not wait or not result.deleted_sandbox_ids:
            return result
        baselines = await asyncio.gather(*(self.get_sandbox(sandbox_id) for sandbox_id in result.deleted_sandbox_ids))
        await asyncio.gather(
            *(
                self._wait_for_sandbox_state(
                    sandbox_id=sandbox_id,
                    baseline=baseline,
                    target_state=SandboxState.DELETED,
                    operation_name="delete_sandboxes",
                )
                for sandbox_id, baseline in zip(result.deleted_sandbox_ids, baselines, strict=True)
            )
        )
        return result

    async def create_exec(
        self,
        sandbox_id: str,
        command: Sequence[str],
        *,
        exec_id: str | None = None,
        cwd: str = "/workspace",
        env_overrides: Mapping[str, str] | None = None,
        wait: bool = False,
    ) -> ExecHandle:
        request = CreateExecRequest(
            sandbox_id=sandbox_id,
            command=tuple(command),
            exec_id=_validate_optional_id("exec_id", exec_id),
            cwd=cwd,
            env_overrides={} if env_overrides is None else dict(env_overrides),
        )
        response = await asyncio.to_thread(
            self._rpc_client.create_exec,
            to_proto_create_exec_request(request),
        )
        current, last_event_sequence = await self._get_exec_snapshot(response.exec_id)
        if not wait:
            return current
        return await self._wait_for_exec_terminal(
            exec_id=response.exec_id,
            sandbox_id=sandbox_id,
            baseline=current,
            baseline_sequence=last_event_sequence,
            operation_name="create_exec",
        )

    async def run(
        self,
        sandbox_id: str,
        command: Sequence[str],
        *,
        cwd: str = "/workspace",
        env_overrides: Mapping[str, str] | None = None,
    ) -> ExecHandle:
        return await self.create_exec(
            sandbox_id,
            command,
            cwd=cwd,
            env_overrides=env_overrides,
            wait=True,
        )

    async def cancel_exec(
        self,
        exec_id: str,
        *,
        wait: bool = True,
    ) -> ExecHandle:
        try:
            await asyncio.to_thread(self._rpc_client.cancel_exec, exec_id)
        except SandboxInvalidStateError as exc:
            raise ExecNotRunningError(exec_id) from exc
        current, last_event_sequence = await self._get_exec_snapshot(exec_id)
        if not wait:
            return current
        return await self._wait_for_exec_terminal(
            exec_id=exec_id,
            sandbox_id=current.sandbox_id,
            baseline=current,
            baseline_sequence=last_event_sequence,
            operation_name="cancel_exec",
        )

    async def get_exec(self, exec_id: str) -> ExecHandle:
        handle, _ = await self._get_exec_snapshot(exec_id)
        return handle

    async def list_active_execs(
        self,
        sandbox_id: str | None = None,
    ) -> list[ExecHandle]:
        response = await asyncio.to_thread(
            self._rpc_client.list_active_execs,
            "" if sandbox_id is None else sandbox_id,
        )
        return [to_exec_handle(item) for item in response.execs]

    async def _get_exec_snapshot(self, exec_id: str) -> tuple[ExecHandle, int]:
        response = await asyncio.to_thread(self._rpc_client.get_exec, exec_id)
        return to_exec_snapshot(response)

    async def subscribe_sandbox_events(
        self,
        sandbox_id: str,
        *,
        from_sequence: int = 0,
        include_current_snapshot: bool = False,
    ) -> AsyncIterator[SandboxEvent]:
        normalized_sequence = normalize_from_sequence(from_sequence)
        loop = asyncio.get_running_loop()
        queue: asyncio.Queue[tuple[str, SandboxEvent | Exception | None]] = asyncio.Queue()
        stop_event = ThreadEvent()
        stream_client = SandboxGrpcClient(
            self.socket_path,
            timeout_seconds=(
                self._timeout_seconds if self._stream_timeout_seconds is None else self._stream_timeout_seconds
            ),
        )

        def publish(kind: str, payload: SandboxEvent | Exception | None) -> None:
            try:
                loop.call_soon_threadsafe(queue.put_nowait, (kind, payload))
            except RuntimeError:
                return

        def produce() -> None:
            try:
                for item in stream_client.subscribe_sandbox_events(
                    sandbox_id,
                    from_sequence=normalized_sequence,
                    include_current_snapshot=include_current_snapshot,
                ):
                    if stop_event.is_set():
                        break
                    publish("event", to_sandbox_event(item))
            except Exception as exc:  # noqa: BLE001
                if not stop_event.is_set():
                    publish("error", exc)
            finally:
                stream_client.close()
                publish("end", None)

        producer = Thread(target=produce, name=f"agents-sandbox-events-{sandbox_id}", daemon=True)
        producer.start()
        try:
            while True:
                kind, payload = await queue.get()
                if kind == "event":
                    yield cast(SandboxEvent, payload)
                    continue
                if kind == "error":
                    raise cast(Exception, payload)
                break
        finally:
            stop_event.set()
            stream_client.close()
            await asyncio.to_thread(producer.join, 1.0)

    async def _wait_for_sandbox_state(
        self,
        *,
        sandbox_id: str,
        baseline: SandboxHandle,
        target_state: SandboxState,
        operation_name: str,
    ) -> SandboxHandle:
        if baseline.state == target_state:
            return baseline
        self._raise_for_failed_sandbox(baseline, operation_name)
        baseline_sequence = parse_event_sequence(baseline.last_event_sequence)
        stream_sequence = baseline.last_event_sequence
        try:
            async with asyncio.timeout(self._operation_timeout_seconds):
                async for event in self.subscribe_sandbox_events(sandbox_id, from_sequence=stream_sequence):
                    if event.sequence <= baseline_sequence:
                        continue
                    current = await self.get_sandbox(sandbox_id)
                    if current.state == target_state:
                        return current
                    self._raise_for_failed_sandbox(current, operation_name)
        except TimeoutError as exc:
            raise TimeoutError(
                f"{operation_name} timed out while waiting for sandbox {sandbox_id} to reach {target_state.name}."
            ) from exc
        raise SandboxClientError(
            f"{operation_name} ended before sandbox {sandbox_id} reached {target_state.name}."
        )

    async def _wait_for_exec_terminal(
        self,
        *,
        exec_id: str,
        sandbox_id: str,
        baseline: ExecHandle,
        baseline_sequence: int,
        operation_name: str,
    ) -> ExecHandle:
        if baseline.state.is_terminal:
            return baseline
        baseline_sequence = parse_event_sequence(baseline_sequence)
        stream = self.subscribe_sandbox_events(sandbox_id, from_sequence=baseline_sequence)
        try:
            async with asyncio.timeout(self._operation_timeout_seconds):
                async for event in stream:
                    if event.sequence <= baseline_sequence:
                        continue
                    if event.exec_id != exec_id:
                        continue
                    current, _ = await self._get_exec_snapshot(exec_id)
                    if current.state.is_terminal:
                        return current
        except TimeoutError as exc:
            raise TimeoutError(
                f"{operation_name} timed out while waiting for exec {exec_id} to become terminal."
            ) from exc
        finally:
            await stream.aclose()
        raise SandboxClientError(
            f"{operation_name} event stream ended before exec {exec_id} reached a terminal state."
        )

    def _raise_for_failed_sandbox(self, handle: SandboxHandle, operation_name: str) -> None:
        if handle.state == SandboxState.FAILED:
            raise SandboxClientError(
                f"{operation_name} observed sandbox {handle.sandbox_id} in FAILED state."
            )


__all__ = ["AgentsSandboxClient"]
