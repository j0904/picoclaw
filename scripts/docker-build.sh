#!/bin/sh
# Trigger the docker-build GitHub Actions workflow remotely.
#
# Usage:
#   ./scripts/docker-build.sh <tag> [image-tag]
#
#   <tag>        Git ref to checkout (branch, tag, or SHA)
#   [image-tag]  Docker image tag (defaults to <tag>)
#
# Requirements: gh CLI authenticated against j0904/picoclaw.
set -e

usage() {
  cat <<EOF
Usage: $(basename "$0") <tag> [image-tag]

Trigger the remote docker-build.yml workflow on GitHub Actions.

Arguments:
  <tag>        Git ref to checkout (e.g. main, v0.2.4)
  [image-tag]  Docker image tag (defaults to <tag>; e.g. 1.0.2-codeserver)

Requires 'gh' CLI authenticated for the picoclaw repository.
EOF
  exit 0
}

[ $# -lt 1 ] && usage
TAG="$1"
IMAGE_TAG="${2:-$TAG}"

REMOTE="${GITHUB_REPOSITORY:-$(git remote get-url origin 2>/dev/null | sed -n 's|.*[:/]\(.*/picoclaw\)\.git|\1|p')}"
REMOTE="${REMOTE:-j0904/picoclaw}"

echo "Triggering docker-build workflow: git ref='${TAG}' image tag='${IMAGE_TAG}' on ${REMOTE}..."

if [ "$IMAGE_TAG" = "$TAG" ]; then
  gh workflow run docker-build.yml \
    --repo "$REMOTE" \
    --field "tag=$TAG"
else
  gh workflow run docker-build.yml \
    --repo "$REMOTE" \
    --field "tag=$TAG" \
    --field "image_tag=$IMAGE_TAG"
fi

echo ""
echo "Done. View at:"
echo "  https://github.com/${REMOTE}/actions/workflows/docker-build.yml"
