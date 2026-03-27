#!/usr/bin/env bash
# Usage:
#   bash scripts/generate_proto.sh
#
# Generates Go and Python protobuf/gRPC bindings from api/proto/service.proto.
#
# Pinned tool versions:
#   protoc:              v6.31.1 (release tag v31.1)
#   protoc-gen-go:       v1.36.11
#   protoc-gen-go-grpc:  v1.6.1
#   grpcio-tools:        from sdk/python dev dependencies (managed by uv)
#
# The script downloads protoc into .local/protoc/ (project-local cache) and
# installs Go plugins into .local/go-bin/ via 'go install'.
# Works on Linux (x86_64/aarch64) and macOS (x86_64/arm64).
# Requires: go, uv, unzip, curl.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# --- Pinned versions ---
PROTOC_VERSION="31.1"
PROTOC_GEN_GO_VERSION="v1.36.11"
PROTOC_GEN_GO_GRPC_VERSION="v1.6.1"

# --- Derived paths ---
PROTOC_DIR="${PROJECT_ROOT}/.local/protoc"
PROTOC_BIN="${PROTOC_DIR}/bin/protoc"
PROTOC_INCLUDE="${PROTOC_DIR}/include"
GOBIN_DIR="${PROJECT_ROOT}/.local/go-bin"
PROTO_SRC="${PROJECT_ROOT}/api/proto/service.proto"
GO_OUT="${PROJECT_ROOT}/api/generated/agboxv1"
PY_OUT="${PROJECT_ROOT}/sdk/python/src/agents_sandbox/_generated"
SDK_PYTHON_DIR="${PROJECT_ROOT}/sdk/python"

# --- Detect OS and arch ---
detect_platform() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "${os}" in
    Linux)  os="linux" ;;
    Darwin) os="osx" ;;
    *)
      echo "Unsupported OS: ${os}" >&2
      exit 1
      ;;
  esac

  case "${arch}" in
    x86_64)  arch="x86_64" ;;
    aarch64) arch="aarch_64" ;;
    arm64)   arch="aarch_64" ;;
    *)
      echo "Unsupported architecture: ${arch}" >&2
      exit 1
      ;;
  esac

  echo "${os}-${arch}"
}

# --- Download and cache protoc ---
ensure_protoc() {
  if [ -x "${PROTOC_BIN}" ]; then
    local current_version
    current_version="$("${PROTOC_BIN}" --version 2>/dev/null | sed 's/libprotoc //')"
    if [ "${current_version}" = "${PROTOC_VERSION}" ]; then
      return 0
    fi
  fi

  local platform zip_name url tmp_dir
  platform="$(detect_platform)"
  zip_name="protoc-${PROTOC_VERSION}-${platform}.zip"
  url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${zip_name}"

  echo "Downloading protoc ${PROTOC_VERSION} for ${platform}..."
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "${tmp_dir}"' EXIT

  curl -fsSL -o "${tmp_dir}/${zip_name}" "${url}"
  unzip -q -o "${tmp_dir}/${zip_name}" -d "${tmp_dir}/protoc"

  # Replace the entire protoc cache directory
  rm -rf "${PROTOC_DIR}"
  mkdir -p "${PROTOC_DIR}/bin"
  cp "${tmp_dir}/protoc/bin/protoc" "${PROTOC_BIN}"
  chmod +x "${PROTOC_BIN}"

  # Copy well-known proto includes (e.g. google/protobuf/timestamp.proto)
  cp -r "${tmp_dir}/protoc/include" "${PROTOC_INCLUDE}"

  rm -rf "${tmp_dir}"
  trap - EXIT

  echo "protoc ${PROTOC_VERSION} installed to ${PROTOC_BIN}"
}

# --- Install Go protoc plugins ---
ensure_go_plugins() {
  echo "Installing protoc-gen-go ${PROTOC_GEN_GO_VERSION} and protoc-gen-go-grpc ${PROTOC_GEN_GO_GRPC_VERSION}..."
  mkdir -p "${GOBIN_DIR}"
  GOBIN="${GOBIN_DIR}" go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
  GOBIN="${GOBIN_DIR}" go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"
}

# Add local GOBIN to PATH so protoc can find the Go plugins.
export PATH="${GOBIN_DIR}:${PATH}"

# --- Generate Go bindings ---
generate_go() {
  echo "Generating Go bindings..."
  mkdir -p "${GO_OUT}"
  "${PROTOC_BIN}" \
    --proto_path="${PROJECT_ROOT}/api/proto" \
    --proto_path="${PROTOC_INCLUDE}" \
    --go_out="${GO_OUT}" \
    --go_opt=paths=source_relative \
    --go-grpc_out="${GO_OUT}" \
    --go-grpc_opt=paths=source_relative \
    "${PROTO_SRC}"
}

# --- Generate Python bindings ---
generate_python() {
  echo "Generating Python bindings..."
  mkdir -p "${PY_OUT}"
  (
    cd "${SDK_PYTHON_DIR}"
    uv run python -m grpc_tools.protoc \
      --proto_path="${PROJECT_ROOT}/api/proto" \
      --proto_path="${PROTOC_INCLUDE}" \
      --python_out="${PY_OUT}" \
      --grpc_python_out="${PY_OUT}" \
      --pyi_out="${PY_OUT}" \
      "${PROTO_SRC}"
  )

  # grpc_tools.protoc generates absolute imports (e.g. "import service_pb2"),
  # but the _generated package requires relative imports ("from . import ...").
  sed -i.bak 's/^import service_pb2/from . import service_pb2/' "${PY_OUT}/service_pb2_grpc.py"
  rm -f "${PY_OUT}/service_pb2_grpc.py.bak"
}

# --- Main ---
ensure_protoc
ensure_go_plugins
generate_go
generate_python

echo "Proto generation complete."
echo "  Go:     ${GO_OUT}"
echo "  Python: ${PY_OUT}"
