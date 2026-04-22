#!/bin/bash
# --- ====================================================== ---
# ---            Build Script for Go Application           ---
# ---        Builds Go code and prepares a runtime         ---
# ---        directory ('build_app') with a run script.    ---
# --- ====================================================== ---
set -e # Exit immediately if a command exits with a non-zero status.

# --- User Configuration (Build-time) ---
# 1. Thư mục chứa code Go chain (tương đối so với vị trí script build.sh)
CHAIN_DIR="cmd/simple_chain"

# 2. Tên file thực thi sau khi build (Để trống sẽ dùng tên thư mục CHAIN_DIR)
GO_BINARY_NAME="" # Ví dụ: "my_chain_app"

# 3. Tên thư mục chứa kết quả build và runtime script
BUILD_DIR="build_app"

# 4. Tên các file cấu hình cần copy vào thư mục build
#    *** QUAN TRỌNG: Script sẽ tìm các file này BÊN TRONG THƯ MỤC CHAIN_DIR ***
CONFIG_FILES=(
    "config-master.json"
    "config-sub-write.json"
    "config-sub-write-2.json"
    "genesis.json"
)

# --- Configuration to be PASSED to run.sh ---
#    (Runtime configurations needed by nodes inside build_app/run.sh)
#    *** Paths shortened (no sample/simple/), assumes data dirs are siblings to build_app ***

# 3. Cấu hình Rsync
#    Path relative to build.sh's location (project root)
RUN_RSYNC_MASTER_DATA_SRC="data/data/"

# 5. Snapshot Mount Point Base
#    Path relative to build.sh's location (project root)
RUN_SNAPSHOT_NAME_BASE_PREFIX="chain_snap"
RUN_SNAPSHOT_MOUNT_POINT_BASE="data/data_snap"

# 7. Log Directory (Relative to run.sh's location - build_app)
RUN_LOG_DIR="./node_logs"
# --- End User Configuration ---


# --- Script Logic ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR_ABS="$SCRIPT_DIR/$BUILD_DIR"
CHAIN_DIR_ABS="$SCRIPT_DIR/$CHAIN_DIR"

echo "--- 🛠️ Starting Build Process ---"
echo "Project Root (Script Dir): $SCRIPT_DIR"
echo "Go Source Directory: $CHAIN_DIR_ABS"
echo "Target Build Directory: $BUILD_DIR_ABS"

# --- 1. Check Go Installation ---
echo -n "🔍 Checking for 'go' command... "
if ! command -v go &> /dev/null; then
    echo "❌ Error: 'go' command not found. Install Go (https://go.dev/doc/install)."
    exit 1
fi
echo "✅ Found."

# --- 2. Determine Go Binary Name ---
if [ -z "$GO_BINARY_NAME" ]; then
    GO_BINARY_NAME=$(basename "$CHAIN_DIR")
    echo "ℹ️ GO_BINARY_NAME not set, using directory name: '$GO_BINARY_NAME'"
fi
GO_BINARY_PATH_IN_BUILDDIR="./$GO_BINARY_NAME"

# --- 3. Build Go Application ---
echo "🚀 Building Go application from '$CHAIN_DIR_ABS'..."
if [ ! -d "$CHAIN_DIR_ABS" ]; then
    echo "⛔ Error: Go source directory not found: '$CHAIN_DIR_ABS'" >&2
    exit 1
fi
pushd "$CHAIN_DIR_ABS" > /dev/null # Go build often works best from within the source dir

TARGET_BINARY_FULL_PATH="$BUILD_DIR_ABS/$GO_BINARY_NAME"
# Ensure build dir exists *before* build attempts to write there
mkdir -p "$BUILD_DIR_ABS" || { echo "⛔ Error creating build directory '$BUILD_DIR_ABS'."; popd >/dev/null; exit 1; }

echo "   Running: CGO_ENABLED=1 go build -o \"$TARGET_BINARY_FULL_PATH\" ."
CGO_ENABLED=1 go build -o "$TARGET_BINARY_FULL_PATH" .
build_exit_code=$?

popd > /dev/null # Return to original directory

if [ $build_exit_code -ne 0 ]; then
    echo "⛔ Go build failed (exit code: $build_exit_code). See errors above." >&2
    exit 1
fi

if [ ! -f "$TARGET_BINARY_FULL_PATH" ]; then
    echo "⛔ Error: Build seemed successful, but executable not found at '$TARGET_BINARY_FULL_PATH'." >&2
    exit 1
fi
echo "✅ Go application built successfully: '$TARGET_BINARY_FULL_PATH'"

# --- 4. Copy Configuration Files ---
echo "📄 Copying configuration files from '$CHAIN_DIR_ABS'..."
for config_file in "${CONFIG_FILES[@]}"; do
    src_config_path="$CHAIN_DIR_ABS/$config_file"
    dest_config_path="$BUILD_DIR_ABS/$(basename "$config_file")"

    if [ -f "$src_config_path" ]; then
        cp "$src_config_path" "$dest_config_path" || {
            echo "⛔ Error copying '$src_config_path' to '$dest_config_path'." >&2
            rm -rf "$BUILD_DIR_ABS"; exit 1; # Clean up on error
        }
        echo "   Copied '$src_config_path' to '$dest_config_path'"
    else
        echo "⛔ Error: Required configuration file '$src_config_path' not found inside '$CHAIN_DIR_ABS'." >&2
        rm -rf "$BUILD_DIR_ABS"; exit 1; # Clean up on error
    fi
done
echo "✅ Configuration files copied."

# --- 5. Generate run.sh Script ---
RUN_SCRIPT_PATH="$BUILD_DIR_ABS/run.sh"
echo "⚙️ Generating runtime script '$RUN_SCRIPT_PATH'..."

# Calculate absolute paths needed by run.sh based on the location of build.sh (project root)
ORIGINAL_SCRIPT_DIR_FOR_RUN="$SCRIPT_DIR"
RUN_RSYNC_MASTER_DATA_SRC_ABS="$SCRIPT_DIR/$RUN_RSYNC_MASTER_DATA_SRC"
RUN_SNAPSHOT_MOUNT_POINT_BASE_ABS="$SCRIPT_DIR/$RUN_SNAPSHOT_MOUNT_POINT_BASE"

# Heredoc to write run.sh
cat << EOF > "$RUN_SCRIPT_PATH"
#!/bin/bash
# --- ====================================================== ---
# ---            Runtime Script for Go Application         ---
# ---   Sets up environment, runs nodes, and manages them. ---
# ---         *** Generated by build.sh *** ---
# ---         Paths assume data dirs are siblings to build_app ---
# --- ====================================================== ---
set -e # Exit immediately if a command exits with a non-zero status.

# --- Configuration (Passed from build.sh - Paths Shortened) ---
GO_BINARY_NAME="${GO_BINARY_NAME}"
RSYNC_MASTER_DATA_SRC_ABS="${RUN_RSYNC_MASTER_DATA_SRC_ABS}" # Absolute path from build.sh
SNAPSHOT_NAME_BASE_PREFIX="${RUN_SNAPSHOT_NAME_BASE_PREFIX}"
SNAPSHOT_MOUNT_POINT_BASE_ABS="${RUN_SNAPSHOT_MOUNT_POINT_BASE_ABS}" # Absolute path from build.sh

LOG_DIR="${RUN_LOG_DIR}" # Relative to this script (build_app)

CONFIG_MASTER="config-master.json"
CONFIG_WRITE1="config-sub-write.json"
CONFIG_WRITE2="config-sub-write-2.json"
# --- End Configuration ---


# --- Runtime Variables ---
PIDS=()
SCRIPT_DIR="\$(cd "\$(dirname "\${BASH_SOURCE[0]}")" && pwd)" # build_app dir
LOG_DIR_ABS="\$SCRIPT_DIR/\$LOG_DIR"
GO_BINARY_PATH="\$SCRIPT_DIR/\$GO_BINARY_NAME"

# --- Helper Functions ---
check_and_install_tools() {
    echo "🔍 Checking runtime tools..." >&2
    tools=("rsync")
    missing_tools=()
    for tool in "\${tools[@]}"; do
        if ! command -v "\$tool" &> /dev/null; then
             if ! dpkg-query -W -f='\${Status}' "\$tool" 2>/dev/null | grep -q "ok installed"; then
                missing_tools+=("\$tool")
             fi
        fi
    done
    if [ \${#missing_tools[@]} -gt 0 ]; then
        echo "📦 Runtime tools needed: \${missing_tools[*]}. Attempting install (sudo required)..." >&2
        if ! command -v apt-get &> /dev/null; then echo "⛔ apt-get not found. Install manually: \${missing_tools[*]}" >&2; exit 1; fi
        sudo apt-get update -qq && sudo apt-get install -y "\${missing_tools[@]}" || { echo "⛔ Install failed. Install manually: \${missing_tools[*]}" >&2; exit 1; }
    fi
    echo "✅ Runtime tools ready." >&2
}



cleanup() {
    echo -e "\n🧹 Starting cleanup (Stopping Go processes)..." >&2
    if [ \${#PIDS[@]} -gt 0 ]; then
        echo "🛑 Sending SIGTERM to Go nodes: \${PIDS[*]}" >&2; kill -TERM "\${PIDS[@]}" 2>/dev/null
        echo "⏳ Waiting up to 5s..." >&2; local wait_sec=5 end_time=\$((SECONDS + wait_sec)) pids_running=(" \${PIDS[@]} ")
        while [ \$SECONDS -lt \$end_time ]; do
            local any_left=false cur_running=()
            for pid in \${PIDS[@]}; do if [[ "\$pids_running" == *" \$pid "* ]]; then if ps -p "\$pid" > /dev/null; then cur_running+=("\$pid"); any_left=true; else pids_running=\${pids_running// \$pid / }; fi; fi; done; if ! \$any_left; then break; fi; sleep 0.5
        done; local still_running=(); for pid in \${PIDS[@]}; do if ps -p "\$pid" > /dev/null; then still_running+=("\$pid"); fi; done
        if [ \${#still_running[@]} -gt 0 ]; then echo "   ⚠️ Nodes \${still_running[*]} unresponsive. Sending SIGKILL." >&2; kill -KILL "\${still_running[@]}" 2>/dev/null; else echo "   ✅ All Go nodes stopped." >&2; fi; PIDS=()
    else echo "ℹ️ No Go node PIDs recorded." >&2; fi; echo "ℹ️ Go app should clean up its own snapshots (if configured and created)." >&2; echo "✅ Process cleanup finished." >&2
}
handle_exit_signals() {
    local signal_type=\$1; echo -e "\n🚨 Received signal \$signal_type..." >&2; if [[ -z "\$CLEANUP_CALLED" ]]; then export CLEANUP_CALLED=true; cleanup; fi
    echo "✅ Runtime script finished." >&2; if [[ "\$signal_type" == "INT" || "\$signal_type" == "TERM" ]]; then exit 130; fi
}

# --- Main Logic ---
trap 'handle_exit_signals INT' INT; trap 'handle_exit_signals TERM' TERM
cd "\$SCRIPT_DIR"; echo "🏠 Running from directory: \$SCRIPT_DIR"
check_and_install_tools
mkdir -p "\$LOG_DIR_ABS" || { echo "⛔ Error creating log dir '\$LOG_DIR_ABS'." >&2; exit 1; }; echo "   Log dir ready: '\$LOG_DIR_ABS'" >&2
if [ ! -f "\$GO_BINARY_PATH" ]; then echo "⛔ Go executable not found: '\$GO_BINARY_PATH'!" >&2; exit 1; fi
if [ ! -x "\$GO_BINARY_PATH" ]; then echo "ℹ️ Making '\$GO_BINARY_PATH' executable..." >&2; chmod +x "\$GO_BINARY_PATH" || { echo "⛔ Failed setting executable permission." >&2; exit 1; }; fi

# --- Prepare Snapshot Environment Variables ---
echo -e "\n--- ⚙️ Preparing Snapshot Environment (Rsync) for Go ---" >&2
FINAL_COMPRESS_ENABLE_SNAPSHOT="false"; FINAL_COMPRESS_SNAPSHOT_CREATE_CMD=""; FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD=""; FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT=""
FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD=""; FINAL_SNAPSHOT_NAME_BASE=""; FINAL_RSYNC_MASTER_DATA_SRC_ABS=""
TIMESTAMP=\$(date +%Y%m%d_%H%M%S); FINAL_SNAPSHOT_NAME_BASE="\${SNAPSHOT_NAME_BASE_PREFIX}_\${TIMESTAMP}"

echo "📦 Configuring rsync snapshot templates." >&2
echo "   Source Dir: '\$RSYNC_MASTER_DATA_SRC_ABS'" >&2
echo "   Destination Base: '\$SNAPSHOT_MOUNT_POINT_BASE_ABS'" >&2
if [ ! -d "\$RSYNC_MASTER_DATA_SRC_ABS" ]; then
    echo "⚠️ Rsync source directory '\$RSYNC_MASTER_DATA_SRC_ABS' not found. Snapshots DISABLED." >&2;
    FINAL_COMPRESS_ENABLE_SNAPSHOT="false";
else
    CRT='echo "⏳[Rsync] Copying data to \$MOUNT_POINT..." >&2 && rm -rf "\$MOUNT_POINT" && mkdir -p "\$MOUNT_POINT" && rsync -a --delete "\$RSYNC_MASTER_DATA_SRC_ABS/" "\$MOUNT_POINT/" || { echo "⛔FATAL: Rsync copy failed!" >&2; exit 1; }'
    MNT='echo "ℹ️[Rsync] Copy completed to \$MOUNT_POINT." >&2'
    CLN='echo "🧹[Rsync] Cleaning up rsync snapshot directory \$MOUNT_POINT..." >&2 && rm -rf "\$MOUNT_POINT" && echo "✅[Rsync] Cleanup finished." >&2'
    FINAL_COMPRESS_ENABLE_SNAPSHOT="true"
    FINAL_COMPRESS_SNAPSHOT_CREATE_CMD="\$CRT"
    FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD="\$MNT"
    FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT="\$SNAPSHOT_MOUNT_POINT_BASE_ABS"
    FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD="\$CLN";
    FINAL_RSYNC_MASTER_DATA_SRC_ABS="\$RSYNC_MASTER_DATA_SRC_ABS"
fi

# --- Export ENV Vars ---
echo "📦 Exporting ENV VARS for Go application..." >&2;
export COMPRESS_ENABLE_SNAPSHOT="\$FINAL_COMPRESS_ENABLE_SNAPSHOT"
export COMPRESS_SNAPSHOT_CREATE_CMD="\$FINAL_COMPRESS_SNAPSHOT_CREATE_CMD"
export COMPRESS_SNAPSHOT_MOUNT_CMD="\$FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD"
export COMPRESS_SNAPSHOT_MOUNT_POINT="\$FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT" # <-- ADDED THIS LINE
export COMPRESS_SNAPSHOT_CLEANUP_CMD="\$FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD"
export SNAPSHOT_NAME_BASE="\$FINAL_SNAPSHOT_NAME_BASE"

if [ "\$FINAL_COMPRESS_ENABLE_SNAPSHOT" = "true" ]; then
    export RSYNC_MASTER_DATA_SRC_ABS="\$FINAL_RSYNC_MASTER_DATA_SRC_ABS"
    echo "   Exported Rsync specific variables." >&2
else
    echo "   Snapshots are disabled." >&2
fi
echo "✅ ENV VARS exported." >&2

# --- Start Go Nodes ---
echo -e "\n--- 🚀 Starting Go Nodes in Background ---" >&2
start_node() {
    local node_name=\$1 config_file_rel=\$2 log_file_abs=\$3
    echo "   🚀 Starting \$node_name Node (Config: \$config_file_rel, Log: \$log_file_abs)..." >&2;
    touch "\$log_file_abs" || { echo "⛔ Error creating log file '\$log_file_abs'!" >&2; return 1; }
    (
        echo "--- \$node_name Log --- (\$(date +'%Y-%m-%d %H:%M:%S'))" > "\$log_file_abs"
        echo "Starting from: \$SCRIPT_DIR" >> "\$log_file_abs"
        echo "Executable: \$GO_BINARY_PATH" >> "\$log_file_abs"
        echo "Config: \$config_file_rel" >> "\$log_file_abs"
        # Xapian path read from Databases.XapianPath in config.json — no env var needed
        echo "Snapshot Enabled: \$COMPRESS_ENABLE_SNAPSHOT" >> "\$log_file_abs"
        echo "Snapshot Mount Point: \$COMPRESS_SNAPSHOT_MOUNT_POINT" >> "\$log_file_abs" # Log exported value
        local cmd="\$GO_BINARY_PATH -config=\$config_file_rel"
        echo "Executing: \$cmd" >> "\$log_file_abs"
        exec \$cmd >> "\$log_file_abs" 2>&1
        echo "⛔FATAL: Failed to execute command: \$cmd" >> "\$log_file_abs"; exit 1;
    ) &
    local node_pid=\$!; PIDS+=(\$node_pid);
    if [ \$? -ne 0 ]; then echo "   ⛔ Launching \$node_name node in background failed!" >&2; return 1; fi;
    echo "   PID \$node_name: \$node_pid" >&2
}
start_node "MASTER"  "\$CONFIG_MASTER"  "\$LOG_DIR_ABS/master.log"; sleep 1
start_node "WRITE 1" "\$CONFIG_WRITE1" "\$LOG_DIR_ABS/write1.log"; sleep 1
start_node "WRITE 2" "\$CONFIG_WRITE2" "\$LOG_DIR_ABS/write2.log"


# --- Monitor Nodes ---
echo -e "\n--- ✅ Runtime Script Active ---" >&2;
echo "   Go executable: \$GO_BINARY_PATH" >&2
echo "   Nodes running from: \$SCRIPT_DIR" >&2
echo "   Node PIDs: \${PIDS[*]}" >&2
if [ "\$FINAL_COMPRESS_ENABLE_SNAPSHOT" = "true" ]; then
    echo "   Snapshotting ON (Rsync)." >&2;
    echo "   Go app uses Name Base: '\$FINAL_SNAPSHOT_NAME_BASE'" >&2
    echo "   Go app uses Mount Point: '\$FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT'" >&2
else
    echo "   Snapshotting OFF." >&2;
fi
echo -e "\n👀 Monitor logs:" >&2;
echo "   tail -f \$LOG_DIR_ABS/master.log" >&2
echo "   tail -f \$LOG_DIR_ABS/write1.log" >&2
echo "   tail -f \$LOG_DIR_ABS/write2.log" >&2
echo -e "\n👉 Press Ctrl+C to stop all nodes and cleanup." >&2
while true; do
    all_ok=true
    pids_chk=(" \${PIDS[@]} ")
    cur_pids=()
    if [[ "\$pids_chk" == "  " ]]; then
        echo -e "\n--- (\$(date +'%Y-%m-%d %H:%M:%S')) No managed nodes running. Exiting monitor loop." >&2
        break
    fi
    for idx in "\${!PIDS[@]}"; do
        pid=\${PIDS[\$idx]}
        if ps -p "\$pid" > /dev/null; then
            cur_pids+=("\$pid")
        else
            node="PID \$pid"
            if [ "\$idx" -eq 0 ]; then node="MASTER";
            elif [ "\$idx" -eq 1 ]; then node="WRITE 1";
            elif [ "\$idx" -eq 2 ]; then node="WRITE 2"; fi;
            echo -e "\n--- ⚠️ Warning (\$(date +'%Y-%m-%d %H:%M:%S')) --- ❌ Node \$node (PID \$pid) stopped unexpectedly!" >&2
            all_ok=false
            pids_chk=\${pids_chk// \$pid / }
        fi
    done
    PIDS=(" \${cur_pids[@]} ")
    PIDS=(\${PIDS})
    if ! \$all_ok; then
        echo "   Monitoring remaining nodes: \${PIDS[*]}" >&2
        echo "   Press Ctrl+C to stop all." >&2
    fi
    sleep 60
done
echo "Exiting run.sh script." >&2
exit 0

EOF

# --- 6. Make run.sh Executable ---
chmod +x "$RUN_SCRIPT_PATH" || { echo "⛔ Error setting execute permission on '$RUN_SCRIPT_PATH'." >&2; rm -rf "$BUILD_DIR_ABS"; exit 1; }
echo "✅ Generated and made '$RUN_SCRIPT_PATH' executable."

# --- 7. Final Instructions ---
echo -e "\n--- 🎉 Build Complete! ---"
echo "Build artifacts are in: $BUILD_DIR_ABS"
echo "Config files copied from '$CHAIN_DIR_ABS' to '$BUILD_DIR_ABS'"
echo "Runtime script generated: $RUN_SCRIPT_PATH"
echo ""
echo "Assumed project structure for build & runtime:"
echo "./"
echo "├── build.sh"
echo "├── build_app/       <-- Generated Build Directory"
echo "│   ├── run.sh         <-- Runtime Script"
echo "│   ├── ${GO_BINARY_NAME} <-- Compiled Go App"
echo "│   ├── config-master.json  <-- Copied Config"
echo "│   ├── config-sub-write.json"
echo "│   └── config-sub-write-2.json"
echo "├── data/            <-- Example Data Directory (sibling to build_app)"
echo "│   ├── data/          <-- Rsync Source / Xapian Location"
echo "│   └── data_snap/     <-- Snapshot Mount Base Location"
echo "├── data-write/"
echo "│   └── data/          <-- Xapian Location"
echo "├── data-write-2/"
echo "│   └── data/          <-- Xapian Location"
echo "├── cmd/"
echo "│   └── simple_chain/  <-- Go Source Code"
echo "│       ├── ... (go files)"
echo "│       ├── config-master.json  <-- EXPECTED LOCATION"
echo "│       ├── config-sub-write.json"
echo "│       └── config-sub-write-2.json"
echo "└── ... (other project files)"
echo ""
echo "To run the application:"
echo "1. Ensure data directories (data/, data-write/, etc.) exist relative to the project root if needed."
echo "2. Ensure config files exist inside '$CHAIN_DIR_ABS'."
echo "3. Run build: ./build.sh"
echo "4. Change directory: cd $BUILD_DIR"
echo "5. Execute the run script: ./run.sh"
echo "   (May require sudo permissions if rsync snapshotting creates directories)."
echo "---------------------------------"

exit 0