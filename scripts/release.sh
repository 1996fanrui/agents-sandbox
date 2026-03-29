#!/usr/bin/env bash
# Fully automated release: auto-compute version, create tag, create GitHub
# Release, and trigger the CI workflow that builds Go binaries + publishes
# Python SDK.
#
# Usage:
#   ./scripts/release.sh alpha          # auto bump patch alpha (0.1.0 → 0.1.1-alpha.1 → 0.1.1-alpha.2)
#   ./scripts/release.sh stable         # promote current alpha to stable (0.1.1-alpha.2 → 0.1.1)
#   ./scripts/release.sh alpha 0.2.0    # minor/major: explicit base version (→ 0.2.0-alpha.1)
#
# `alpha` without a base version always bumps the patch number automatically.
# To release a new minor or major version, pass the target X.Y.Z explicitly.
#
# The script uses a temporary git worktree so it never touches the current
# working directory, branch, or uncommitted changes. The worktree is cleaned
# up automatically on exit.
#
# If the GitHub Release creation fails after the tag was pushed, the script
# prints a recovery command to delete the dangling tag.

set -e

# -- helpers ------------------------------------------------------------------

die() { echo "Error: $*" >&2; exit 1; }

base_version() { echo "$1" | sed 's/-alpha\.[0-9]*//'; }
alpha_num() { echo "$1" | grep -oE 'alpha\.[0-9]+' | grep -oE '[0-9]+'; }
is_alpha() { echo "$1" | grep -qE '\-alpha\.'; }

bump_patch() {
  local major minor patch
  IFS='.' read -r major minor patch <<< "$1"
  echo "${major}.${minor}.$((patch + 1))"
}

latest_release_version() {
  # Stable versions sort before their own pre-releases under sort -V (e.g.
  # 0.1.1-alpha.4 > 0.1.1), so we prefer the latest stable tag when one
  # exists, and fall back to the latest alpha only when there are no stables.
  local latest_stable latest_alpha
  latest_stable="$(git tag -l 'v[0-9]*' | grep -vE '\-alpha\.' | sed 's/^v//' | sort -V | tail -1)"
  if [ -n "${latest_stable}" ]; then
    # Check if any alpha exists beyond the latest stable (i.e. for a newer base).
    latest_alpha="$(git tag -l 'v[0-9]*' | grep -E '\-alpha\.' | sed 's/^v//' | sort -V | tail -1)"
    if [ -n "${latest_alpha}" ] && [ "$(printf '%s\n%s' "$(base_version "${latest_alpha}")" "${latest_stable}" | sort -V | tail -1)" != "${latest_stable}" ]; then
      # The alpha's base version is strictly newer than the latest stable.
      echo "${latest_alpha}"
    else
      echo "${latest_stable}"
    fi
  else
    git tag -l 'v[0-9]*' | sed 's/^v//' | sort -V | tail -1
  fi
}

# -- worktree management ------------------------------------------------------

WORKTREE_DIR=""

cleanup_worktree() {
  if [ -n "${WORKTREE_DIR}" ] && [ -d "${WORKTREE_DIR}" ]; then
    git worktree remove --force "${WORKTREE_DIR}" 2>/dev/null || true
  fi
}

trap cleanup_worktree EXIT

# Resolve the commit to tag for a given mode.
# - alpha: always tags origin/main HEAD (latest code).
# - stable: tags the same commit as the latest alpha tag (already verified).
resolve_release_ref() {
  local mode="$1"
  local latest="$2"

  if [ "${mode}" = "stable" ] && is_alpha "${latest}"; then
    echo "v${latest}"
  else
    echo "origin/main"
  fi
}

setup_worktree() {
  local ref="$1"

  WORKTREE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/release-worktree.XXXXXX")"
  git worktree add --quiet --detach "${WORKTREE_DIR}" "${ref}"
}

# -- compute next version ----------------------------------------------------

compute_next_version() {
  local mode="$1"
  local target_base="$2"
  local latest="$3"

  case "${mode}" in
    alpha)
      if [ -n "${target_base}" ]; then
        echo "${target_base}-alpha.1"
      elif [ -z "${latest}" ]; then
        echo "0.1.0-alpha.1"
      elif is_alpha "${latest}"; then
        local base num
        base="$(base_version "${latest}")"
        num="$(alpha_num "${latest}")"
        echo "${base}-alpha.$((num + 1))"
      else
        echo "$(bump_patch "${latest}")-alpha.1"
      fi
      ;;
    stable)
      if [ -z "${latest}" ]; then
        die "no previous release found. Run './scripts/release.sh alpha' first."
      elif is_alpha "${latest}"; then
        base_version "${latest}"
      else
        bump_patch "${latest}"
      fi
      ;;
    *)
      die "unknown mode '${mode}'. Use 'alpha' or 'stable'."
      ;;
  esac
}

# -- main ---------------------------------------------------------------------

if [ -z "$1" ]; then
  echo "Usage: $0 <alpha|stable> [base-version]" >&2
  echo "" >&2
  echo "Examples:" >&2
  echo "  $0 alpha            # auto-increment alpha (0.1.0 → 0.1.1-alpha.1)" >&2
  echo "  $0 alpha            # iterate alpha         (0.1.1-alpha.1 → 0.1.1-alpha.2)" >&2
  echo "  $0 stable           # promote to stable     (0.1.1-alpha.2 → 0.1.1)" >&2
  echo "  $0 alpha 0.2.0      # start new version     (→ 0.2.0-alpha.1)" >&2
  exit 1
fi

MODE="$1"
TARGET_BASE="$2"

if [ -n "${TARGET_BASE}" ]; then
  if ! echo "${TARGET_BASE}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    die "invalid base version '${TARGET_BASE}'. Expected X.Y.Z (e.g. 0.2.0)"
  fi
  if [ "${MODE}" != "alpha" ]; then
    die "base version argument is only allowed with 'alpha' mode"
  fi
fi

# Fetch first to ensure tags and remote refs are up-to-date before computing.
git fetch origin main --quiet --tags

# Compute version from existing tags.
LATEST="$(latest_release_version)"
NEW_VERSION="$(compute_next_version "${MODE}" "${TARGET_BASE}" "${LATEST}")"
TAG="v${NEW_VERSION}"

# Resolve which commit to tag and create worktree there.
RELEASE_REF="$(resolve_release_ref "${MODE}" "${LATEST}")"
setup_worktree "${RELEASE_REF}"

if git rev-parse "${TAG}" >/dev/null 2>&1; then
  die "tag ${TAG} already exists (this should not happen — check tag history)"
fi

PRERELEASE_FLAG=""
RELEASE_TYPE="STABLE"
if is_alpha "${NEW_VERSION}"; then
  PRERELEASE_FLAG="--prerelease"
  RELEASE_TYPE="ALPHA"
fi

# -- confirmation -------------------------------------------------------------

COMMIT_SHA="$(git -C "${WORKTREE_DIR}" rev-parse --short HEAD)"
echo ""
if [ -n "${LATEST}" ]; then
  echo "  Latest release : v${LATEST}"
fi
echo "  New version    : ${TAG}"
echo "  Release type   : ${RELEASE_TYPE}"
echo "  Commit         : ${COMMIT_SHA} (${RELEASE_REF})"
if [ "${RELEASE_TYPE}" = "ALPHA" ]; then
  PY_VERSION="$(echo "${NEW_VERSION}" | sed -E 's/-alpha\.?/a/')"
  echo "  PyPI version   : ${PY_VERSION} (pre-release, pip install ignores by default)"
else
  echo "  PyPI version   : ${NEW_VERSION}"
fi
echo ""
read -r -p "Proceed? [y/N] " confirm
if [ "${confirm}" != "y" ] && [ "${confirm}" != "Y" ]; then
  echo "Aborted."
  exit 0
fi

# -- create tag and release ---------------------------------------------------

# Tag the exact commit in the worktree
git -C "${WORKTREE_DIR}" tag "${TAG}"
git -C "${WORKTREE_DIR}" push origin "${TAG}"

if ! gh release create "${TAG}" \
  --title "${TAG}" \
  --generate-notes \
  ${PRERELEASE_FLAG}; then
  echo "" >&2
  echo "GitHub Release creation failed. The tag was already pushed." >&2
  echo "To recover, delete the dangling tag:" >&2
  echo "  git tag -d ${TAG} && git push origin :refs/tags/${TAG}" >&2
  exit 1
fi

echo ""
echo "Release ${TAG} created successfully."
echo ""
echo "The release workflow will automatically:"
echo "  - Build Go binaries (linux/darwin × amd64/arm64)"
echo "  - Publish Python SDK to PyPI"
echo ""
echo "Monitor progress:"
echo "  gh run list --workflow=release.yml --limit=1"
