#!/bin/bash
# gitsync.sh — Sync remote repos and rebase onto main
#
# Usage:
#   ./config/gitsync.sh          # sync & rebase current branch onto main
#   ./config/gitsync.sh <branch> # sync & rebase <branch> onto main

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || dirname "$0")"
cd "$REPO_ROOT"

TARGET_BRANCH="${1:-main}"

echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo -e "${CYAN}  PicoClaw Git Sync & Rebase${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo ""

# --- Preflight checks ---
if ! git rev-parse --is-inside-work-tree &>/dev/null; then
    echo -e "${RED}Error: not inside a git repository.${NC}"
    exit 1
fi

CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$CURRENT_BRANCH" = "HEAD" ]; then
    echo -e "${RED}Error: HEAD is detached. Switch to a branch first.${NC}"
    exit 1
fi

echo -e "${YELLOW}Current branch:${NC}  $CURRENT_BRANCH"
echo -e "${YELLOW}Target base:  ${NC}  $TARGET_BRANCH"
echo ""

# --- Fetch all remotes ---
echo -e "${CYAN}[1/4] Fetching all remotes...${NC}"
git fetch --all --prune --quiet 2>&1 | sed 's/^/  /'
echo -e "${GREEN}  ✓ Fetch complete${NC}"
echo ""

# --- Verify target branch exists ---
if ! git rev-parse --verify "$TARGET_BRANCH" &>/dev/null; then
    echo -e "${RED}Error: branch '$TARGET_BRANCH' not found locally or remotely.${NC}"
    echo "  Available branches:"
    git branch -a | head -20
    exit 1
fi

# --- Stash local changes if dirty ---
DIRTY=false
if ! git diff-index --quiet HEAD -- 2>/dev/null; then
    DIRTY=true
    echo -e "${YELLOW}[2/4] Working tree is dirty — stashing changes...${NC}"
    git stash push -m "gitsync: $(date +%Y%m%d-%H%M%S)" --quiet
    echo -e "${GREEN}  ✓ Changes stashed${NC}"
else
    echo -e "${CYAN}[2/4] Working tree is clean — no stash needed${NC}"
fi
echo ""

# --- Fast-forward target branch onto upstream ---
echo -e "${CYAN}[3/4] Updating $TARGET_BRANCH from upstream...${NC}"
git checkout "$TARGET_BRANCH" --quiet 2>&1

# Merge upstream/<target> into local <target>
if git rev-parse --verify "upstream/$TARGET_BRANCH" &>/dev/null; then
    git merge "upstream/$TARGET_BRANCH" --no-edit --quiet 2>&1 && \
        echo -e "${GREEN}  ✓ $TARGET_BRANCH merged from upstream/$TARGET_BRANCH${NC}" || \
        echo -e "${YELLOW}  ⚠ Already up to date with upstream/$TARGET_BRANCH${NC}"
elif git rev-parse --verify "origin/$TARGET_BRANCH" &>/dev/null; then
    git merge --ff-only "origin/$TARGET_BRANCH" --quiet 2>&1 && \
        echo -e "${GREEN}  ✓ $TARGET_BRANCH fast-forwarded to origin/$TARGET_BRANCH${NC}" || \
        echo -e "${YELLOW}  ⚠ Already up to date with origin/$TARGET_BRANCH${NC}"
else
    echo -e "${RED}  ✗ No upstream/$TARGET_BRANCH or origin/$TARGET_BRANCH found${NC}"
fi
echo ""

# --- Rebase current feature branch onto updated target ---
if [ "$CURRENT_BRANCH" != "$TARGET_BRANCH" ]; then
    echo -e "${CYAN}[4/4] Rebasing $CURRENT_BRANCH onto $TARGET_BRANCH...${NC}"
    git checkout "$CURRENT_BRANCH" --quiet 2>&1
    git rebase "$TARGET_BRANCH" 2>&1 | sed 's/^/  /'
    echo -e "${GREEN}  ✓ $CURRENT_BRANCH rebased onto $TARGET_BRANCH${NC}"
else
    echo -e "${CYAN}[4/4] Already on $TARGET_BRANCH — skipping rebase${NC}"
fi
echo ""

# --- Restore stashed changes ---
if [ "$DIRTY" = true ]; then
    echo -e "${YELLOW}Restoring stashed changes...${NC}"
    git stash pop --quiet 2>&1
    echo -e "${GREEN}  ✓ Stash restored${NC}"
fi
echo ""

# --- Summary ---
echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Sync complete!${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo ""
echo "  Branch:   $(git rev-parse --abbrev-ref HEAD)"
echo "  Commit:   $(git rev-parse --short HEAD) $(git log -1 --format='%s')"
echo "  Behind:   $(git rev-list --count HEAD..origin/$(git rev-parse --abbrev-ref HEAD) 2>/dev/null || echo 'N/A')"
echo "  Ahead:    $(git rev-list --count origin/$(git rev-parse --abbrev-ref HEAD)..HEAD 2>/dev/null || echo 'N/A')"
echo ""
echo "  Run 'git push' to publish your rebased branch."