#!/usr/bin/env bash
# Release a new coding-runtime image by tagging origin/main and pushing the
# tag, which triggers the publish-coding-runtime.yml workflow.
#
# Uses simple X.Y.Z versioning (no alpha convention).
#
# Usage:
#   ./scripts/release_image.sh            # auto bump patch (0.1.0 → 0.1.1)
#   ./scripts/release_image.sh 0.2.0      # explicit version override
#
# The script uses a temporary git worktree so it never touches the current
# working directory, branch, or uncommitted changes.

set -eo pipefail

# -- helpers ------------------------------------------------------------------

die() { echo "Error: $*" >&2; exit 1; }

bump_patch() {
  local major minor patch
  IFS='.' read -r major minor patch <<< "$1"
  echo "${major}.${minor}.$((patch + 1))"
}

latest_image_version() {
  git tag -l 'image-coding-v[0-9]*' | sed 's/^image-coding-v//' | sort -V | tail -1
}

# -- worktree management ------------------------------------------------------

WORKTREE_DIR=""

cleanup_worktree() {
  if [ -n "${WORKTREE_DIR}" ] && [ -d "${WORKTREE_DIR}" ]; then
    git worktree remove --force "${WORKTREE_DIR}" 2>/dev/null || true
  fi
}

trap cleanup_worktree EXIT

# -- main ---------------------------------------------------------------------

EXPLICIT_VERSION="$1"

if [ -n "${EXPLICIT_VERSION}" ]; then
  if ! echo "${EXPLICIT_VERSION}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    die "invalid version '${EXPLICIT_VERSION}'. Expected X.Y.Z (e.g. 0.2.0)"
  fi
fi

# Fetch to ensure tags and remote refs are up-to-date.
git fetch origin main --quiet --tags

LATEST="$(latest_image_version)"

if [ -n "${EXPLICIT_VERSION}" ]; then
  NEW_VERSION="${EXPLICIT_VERSION}"
elif [ -z "${LATEST}" ]; then
  NEW_VERSION="0.1.0"
else
  NEW_VERSION="$(bump_patch "${LATEST}")"
fi

TAG="image-coding-v${NEW_VERSION}"

if git rev-parse "${TAG}" >/dev/null 2>&1; then
  die "tag ${TAG} already exists"
fi

# Create worktree at origin/main HEAD.
WORKTREE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/release-image-worktree.XXXXXX")"
git worktree add --quiet --detach "${WORKTREE_DIR}" origin/main

COMMIT_SHA="$(git -C "${WORKTREE_DIR}" rev-parse --short HEAD)"

# -- confirmation -------------------------------------------------------------

echo ""
if [ -n "${LATEST}" ]; then
  echo "  Latest image tag : image-coding-v${LATEST}"
fi
echo "  New version      : ${TAG}"
echo "  Commit           : ${COMMIT_SHA} (origin/main)"
echo ""
read -r -p "Proceed? [y/N] " confirm
if [ "${confirm}" != "y" ] && [ "${confirm}" != "Y" ]; then
  echo "Aborted."
  exit 0
fi

# -- create and push tag ------------------------------------------------------

git -C "${WORKTREE_DIR}" tag "${TAG}"
git -C "${WORKTREE_DIR}" push origin "${TAG}"

echo ""
echo "Tag ${TAG} pushed successfully."
echo ""
echo "Monitor the image build:"
echo "  gh run list --workflow=publish-coding-runtime.yml --limit=1"
