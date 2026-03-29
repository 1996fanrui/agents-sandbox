"""Python client for the agents-sandbox daemon."""

from __future__ import annotations

from collections.abc import Iterator

import grpc
from google.rpc import error_details_pb2
from grpc_status import rpc_status

from . import errors
from ._generated import service_pb2, service_pb2_grpc


class SandboxGrpcClient:
    """Thin synchronous wrapper around the generated gRPC stub."""

    def __init__(self, socket_path: str, *, timeout_seconds: float | None = 5.0) -> None:
        self._timeout_seconds = timeout_seconds
        self._channel = grpc.insecure_channel(f"unix://{socket_path}")
        self._stub = service_pb2_grpc.SandboxServiceStub(self._channel)

    def close(self) -> None:
        self._channel.close()

    def __enter__(self) -> "SandboxGrpcClient":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        del exc_type, exc, tb
        self.close()

    def ping(self) -> service_pb2.PingResponse:
        return self._call(self._stub.Ping, service_pb2.PingRequest())

    def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
        return self._call(self._stub.CreateSandbox, request)

    def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
        return self._call(self._stub.GetSandbox, service_pb2.GetSandboxRequest(sandbox_id=sandbox_id))

    def list_sandboxes(self, request: service_pb2.ListSandboxesRequest | None = None) -> service_pb2.ListSandboxesResponse:
        return self._call(self._stub.ListSandboxes, request or service_pb2.ListSandboxesRequest())

    def resume_sandbox(self, sandbox_id: str) -> service_pb2.AcceptedResponse:
        return self._call(self._stub.ResumeSandbox, service_pb2.ResumeSandboxRequest(sandbox_id=sandbox_id))

    def stop_sandbox(
        self,
        sandbox_id: str,
    ) -> service_pb2.AcceptedResponse:
        return self._call(
            self._stub.StopSandbox,
            service_pb2.StopSandboxRequest(sandbox_id=sandbox_id),
        )

    def delete_sandbox(
        self,
        sandbox_id: str,
    ) -> service_pb2.AcceptedResponse:
        return self._call(
            self._stub.DeleteSandbox,
            service_pb2.DeleteSandboxRequest(sandbox_id=sandbox_id),
        )

    def delete_sandboxes(
        self,
        request: service_pb2.DeleteSandboxesRequest,
    ) -> service_pb2.DeleteSandboxesResponse:
        return self._call(self._stub.DeleteSandboxes, request)

    def subscribe_sandbox_events(
        self,
        sandbox_id: str,
        *,
        from_sequence: int = 0,
        include_current_snapshot: bool = False,
    ) -> Iterator[service_pb2.SandboxEvent]:
        request = service_pb2.SubscribeSandboxEventsRequest(
            sandbox_id=sandbox_id,
            from_sequence=from_sequence,
            include_current_snapshot=include_current_snapshot,
        )
        try:
            stream = self._stub.SubscribeSandboxEvents(request, timeout=self._timeout_seconds)
            for event in stream:
                yield event
        except grpc.RpcError as exc:
            raise _translate_rpc_error(exc) from exc

    def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
        return self._call(self._stub.CreateExec, request)

    def cancel_exec(
        self,
        exec_id: str,
    ) -> service_pb2.AcceptedResponse:
        return self._call(
            self._stub.CancelExec,
            service_pb2.CancelExecRequest(exec_id=exec_id),
        )

    def get_exec(self, exec_id: str) -> service_pb2.GetExecResponse:
        return self._call(self._stub.GetExec, service_pb2.GetExecRequest(exec_id=exec_id))

    def list_active_execs(self, sandbox_id: str | None = None) -> service_pb2.ListActiveExecsResponse:
        req = service_pb2.ListActiveExecsRequest()
        if sandbox_id is not None:
            req.sandbox_id = sandbox_id
        return self._call(self._stub.ListActiveExecs, req)

    def _call(self, rpc, request):
        try:
            return rpc(request, timeout=self._timeout_seconds)
        except grpc.RpcError as exc:
            raise _translate_rpc_error(exc) from exc


def _translate_rpc_error(exc: grpc.RpcError) -> Exception:
    reason = ""
    metadata: dict[str, str] = {}
    parsed_status = rpc_status.from_call(exc)
    if parsed_status is not None:
        for detail in parsed_status.details:
            error_info = error_details_pb2.ErrorInfo()
            if detail.Is(error_info.DESCRIPTOR):
                detail.Unpack(error_info)
                reason = error_info.reason
                metadata = dict(error_info.metadata)
                break

    sandbox_id = metadata.get("sandbox_id")
    exec_id = metadata.get("exec_id")

    match reason:
        case "SANDBOX_CONFLICT" | "SANDBOX_ID_ALREADY_EXISTS" | "EXEC_ID_ALREADY_EXISTS":
            return errors.SandboxConflictError(sandbox_id)
        case "SANDBOX_NOT_FOUND":
            return errors.SandboxNotFoundError(sandbox_id)
        case "SANDBOX_NOT_READY":
            return errors.SandboxNotReadyError(sandbox_id)
        case "SANDBOX_INVALID_STATE":
            return errors.SandboxInvalidStateError(exc.details() or reason or "RPC failed")
        case "EXEC_NOT_FOUND":
            return errors.ExecNotFoundError(exec_id)
        case "EXEC_ALREADY_TERMINAL":
            return errors.ExecAlreadyTerminalError(exec_id)
        case "EXEC_NOT_RUNNING":
            return errors.ExecNotRunningError(exec_id)
        case "SANDBOX_EVENT_SEQUENCE_EXPIRED":
            from_seq_str = metadata.get("from_sequence")
            from_seq = int(from_seq_str) if from_seq_str else None
            return errors.SandboxSequenceExpiredError(sandbox_id, from_seq)
        case _:
            return errors.SandboxClientError(exc.details() or reason or "RPC failed")
