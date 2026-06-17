#!/bin/bash
# ============================================================================
# Git Auto-Rebuild & Deploy Daemon
# Periodically checks for new commits on remote and triggers deploy.
# ============================================================================

set -u

REMOTE="origin"
BRANCH="dev"
CHECK_INTERVAL=5
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ANSIBLE_DIR="${PROJECT_ROOT}/ansible"

echo "👀 Starting Git Auto-Deploy Watcher..."
echo "📍 Project Root: $PROJECT_ROOT"
echo "📍 Tracking remote: $REMOTE/$BRANCH"
echo "⏰ Check interval: ${CHECK_INTERVAL}s"

cd "$PROJECT_ROOT"

# Ensure we are tracking the branch correctly
git checkout "$BRANCH" 2>/dev/null || true

while true; do
    # Fetch from remote without merging
    if git fetch "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
        LOCAL_HASH=$(git rev-parse HEAD)
        REMOTE_HASH=$(git rev-parse "${REMOTE}/${BRANCH}")
        
        if [ "$LOCAL_HASH" != "$REMOTE_HASH" ]; then
            echo -e "\n🔔 [$(date '+%Y-%m-%d %H:%M:%S')] New commit detected on remote!"
            echo "   Local:  $LOCAL_HASH"
            echo "   Remote: $REMOTE_HASH"
            
            echo "🔄 Pulling updates..."
            git pull "$REMOTE" "$BRANCH"
            
            # Extract new commit details for the Telegram notification
            NEW_LOCAL_HASH=$(git rev-parse HEAD)
            COMMIT_MSG=$(git log -1 --pretty=%B | head -n 1)
            COMMIT_AUTHOR=$(git log -1 --pretty=%an)
            export DEPLOY_SOURCE="Auto-Deploy (Git Commit ${NEW_LOCAL_HASH::8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
            
            echo "🚀 Triggering build & deploy for $DEPLOY_SOURCE..."
            cd "$ANSIBLE_DIR"
            ./ansible_deploy.sh --start
            
            # Go back to root
            cd "$PROJECT_ROOT"
            echo "✅ Auto-deploy completed successfully. Resuming watch..."
        fi
    else
        echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Failed to fetch from git remote. Will retry..."
    fi
    
    sleep $CHECK_INTERVAL
done
