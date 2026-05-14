#!/bin/sh
# Trigger the docker-build GitHub Actions workflow remotely.
#
# Usage:
#   ./scripts/docker-build.sh <tag>
#
# Requirements: gh CLI authenticated against j0904/picoclaw.
set -e

usage() {
  cat <<EOF
Usage: $(basename "$0") <tag>

Trigger the remote docker-build.yml workflow on GitHub Actions.

Arguments:
  <tag>   Git tag to build (e.g. v0.2.4)

Requires 'gh' CLI authenticated for the picoclaw repository.
EOF
  exit 0
}

[ $# -ne 1 ] && usage
TAG="$1"

# Derive the remote from git, defaulting to the origin of this checkout.
REMOTE="${GITHUB_REPOSITORY:-$(git remote get-url origin 2>/dev/null | sed -n 's|.*[:/]\(.*/picoclaw\)\.git|\1|p')}"
REMOTE="${REMOTE:-j0904/picoclaw}"

echo "Triggering docker-build workflow for tag '${TAG}' on ${REMOTE}..."
gh workflow run docker-build.yml \
  --repo "$REMOTE" \
  --field "tag=$TAG"

echo ""
echo "Done. View at:"
echo "  https://github.com/${REMOTE}/actions/workflows/docker-build.yml"
