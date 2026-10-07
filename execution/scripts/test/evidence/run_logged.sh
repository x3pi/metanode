#!/usr/bin/env bash
# ==============================================================================
# Helper to execute a command, record raw stdout/stderr, and update MANIFEST.json
# Usage:
#   run_logged.sh [--dir <evidence_dir>] <id> -- <command...>
# Or with env:
#   EVIDENCE_DIR=note/evidence/foo run_logged.sh <id> -- <command...>
# ==============================================================================
set -euo pipefail

EVIDENCE_DIR="${EVIDENCE_DIR:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dir)
            EVIDENCE_DIR="$2"
            shift 2
            ;;
        *)
            break
            ;;
    esac
done

if [ -z "$EVIDENCE_DIR" ]; then
    echo "❌ [ERROR] Evidence directory not specified. Use --dir <path> or set EVIDENCE_DIR" >&2
    exit 1
fi

if [ $# -lt 2 ] || [ "$2" != "--" ]; then
    echo "❌ [ERROR] Usage: run_logged.sh [--dir <dir>] <id> -- <command...>" >&2
    exit 1
fi

ENTRY_ID="$1"
shift 2 # remove <id> and "--"
COMMAND_STR="$*"

mkdir -p "$EVIDENCE_DIR"
MANIFEST_PATH="$EVIDENCE_DIR/MANIFEST.json"
RAW_LOG_REL="${ENTRY_ID}.log"
RAW_LOG_PATH="$EVIDENCE_DIR/$RAW_LOG_REL"

GIT_COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "unknown")
GIT_STATUS=$(git status --porcelain 2>/dev/null | tr '\n' ';' || echo "")
CWD="$(pwd)"
STARTED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo "=================================================================="
echo "📝 [RUN_LOGGED] ID:          $ENTRY_ID"
echo "📝 [RUN_LOGGED] Evidence:    $RAW_LOG_PATH"
echo "📝 [RUN_LOGGED] Command:     $COMMAND_STR"
echo "=================================================================="

# Execute command capturing stdout and stderr
EXIT_CODE=0
set +e
"$@" > "$RAW_LOG_PATH" 2>&1
EXIT_CODE=$?
set -e

FINISHED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
FILE_SHA256=$(sha256sum "$RAW_LOG_PATH" | awk '{print $1}')
FILE_BYTES=$(stat -c %s "$RAW_LOG_PATH")

echo "📝 [RUN_LOGGED] Exit Code:   $EXIT_CODE (Bytes: $FILE_BYTES, SHA256: ${FILE_SHA256:0:16}...)"

# Update MANIFEST.json atomically using Python
python3 -I -c "
import json, os, sys

manifest_path = sys.argv[1]
entry = {
    'id': sys.argv[2],
    'command': sys.argv[3],
    'cwd': sys.argv[4],
    'git_commit': sys.argv[5],
    'git_status': sys.argv[6],
    'started_at': sys.argv[7],
    'finished_at': sys.argv[8],
    'exit_code': int(sys.argv[9]),
    'files': [
        {
            'path': sys.argv[10],
            'sha256': sys.argv[11],
            'bytes': int(sys.argv[12])
        }
    ]
}

data = {'manifest_version': '1.0', 'entries': []}
if os.path.exists(manifest_path):
    try:
        with open(manifest_path, 'r', encoding='utf-8') as f:
            data = json.load(f)
    except Exception:
        data = {'manifest_version': '1.0', 'entries': []}

# Update existing entry or append
entries = data.get('entries', [])
updated = False
for i, e in enumerate(entries):
    if e.get('id') == entry['id']:
        entries[i] = entry
        updated = True
        break
if not updated:
    entries.append(entry)

data['entries'] = entries
tmp_manifest = manifest_path + '.tmp'
with open(tmp_manifest, 'w', encoding='utf-8') as f:
    json.dump(data, f, indent=2)
os.replace(tmp_manifest, manifest_path)
" "$MANIFEST_PATH" "$ENTRY_ID" "$COMMAND_STR" "$CWD" "$GIT_COMMIT" "$GIT_STATUS" "$STARTED_AT" "$FINISHED_AT" "$EXIT_CODE" "$RAW_LOG_REL" "$FILE_SHA256" "$FILE_BYTES"

exit "$EXIT_CODE"
