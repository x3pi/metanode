#!/bin/bash
# ============================================================================
# Git Auto-Rebuild & Deploy Daemon
# Periodically checks for new commits on remote and triggers deploy.
# ============================================================================

set -u

# Check for daemon flag
DAEMON_MODE=false
args=()
for arg in "$@"; do
    if [ "$arg" = "-d" ] || [ "$arg" = "--daemon" ]; then
        DAEMON_MODE=true
    else
        args+=("$arg")
    fi
done

if [ "$DAEMON_MODE" = true ]; then
    echo "🚀 Starting Git Auto-Deploy Watcher in background daemon mode..."
    nohup "$0" "${args[@]}" > auto_deploy.log 2>&1 &
    echo "✅ Watcher is now running in the background (PID: $!)."
    echo "📜 To view logs, run: tail -f auto_deploy.log"
    echo "🛑 To stop it, run: kill $!"
    exit 0
fi

REMOTE="origin"
BRANCH="dev"
CHECK_INTERVAL=5
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANSIBLE_DIR="${PROJECT_ROOT}/deploy/ansible"

echo "👀 Starting Git Auto-Deploy Watcher..."
echo "📍 Project Root: $PROJECT_ROOT"
echo "📍 Tracking remote: $REMOTE/$BRANCH"
echo "⏰ Check interval: ${CHECK_INTERVAL}s"

cd "$PROJECT_ROOT"

# Ensure we are tracking the branch correctly
git checkout "$BRANCH" 2>/dev/null || true

echo "🚀 Performing initial deployment before watching..."
cd "$ANSIBLE_DIR"
export DEPLOY_SOURCE="Auto-Deploy (Initial Run)"
if ! ./ansible_deploy.sh "$@"; then
    echo "❌ Initial deploy failed! Exiting auto-deploy watcher."
    exit 1
fi
echo "✅ Initial deploy successful."

# Update the hash so the watcher doesn't immediately re-trigger
if git fetch "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
    git rev-parse "${REMOTE}/${BRANCH}" > "${ANSIBLE_DIR}/.last_deployed_commit"
fi

cd "$PROJECT_ROOT"
echo "👀 Entering Watcher mode. Polling every ${CHECK_INTERVAL}s..."

while true; do
    # Fetch from remote without merging
    if git fetch "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
        REMOTE_HASH=$(git rev-parse "${REMOTE}/${BRANCH}")
        
        # Read the last deployed commit hash
        LAST_DEPLOYED_FILE="${ANSIBLE_DIR}/.last_deployed_commit"
        LAST_DEPLOYED=""
        if [ -f "$LAST_DEPLOYED_FILE" ]; then
            LAST_DEPLOYED=$(cat "$LAST_DEPLOYED_FILE" | xargs)
        fi
        
        if [ "$REMOTE_HASH" != "$LAST_DEPLOYED" ]; then
            echo -e "\n🔔 [$(date '+%Y-%m-%d %H:%M:%S')] New commit detected on remote!"
            echo "   Last Deployed: $LAST_DEPLOYED"
            echo "   Remote Commit: $REMOTE_HASH"
            
            echo "🔄 Pulling updates..."
            git pull "$REMOTE" "$BRANCH"
            
            # Extract new commit details for the Telegram notification
            NEW_LOCAL_HASH=$(git rev-parse HEAD)
            COMMIT_MSG=$(git log -1 --pretty=%B | head -n 1)
            COMMIT_AUTHOR=$(git log -1 --pretty=%an)
            export DEPLOY_SOURCE="Auto-Deploy (Branch: ${BRANCH}, Git Commit ${NEW_LOCAL_HASH::8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
            
            echo "🚀 Triggering build & deploy for $DEPLOY_SOURCE..."
            cd "$ANSIBLE_DIR"
            ./ansible_deploy.sh --start --fast
            
            # Go back to root
            cd "$PROJECT_ROOT"
            echo "✅ Auto-deploy task finished. Resuming watch..."
        fi
    else
        echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Failed to fetch from git remote. Will retry..."
    fi
    
    sleep $CHECK_INTERVAL
done
