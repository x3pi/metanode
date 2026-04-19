#!/bin/bash

# =================================================================
# Script to Increase System-wide File Descriptor Limit to 10 Million
# =================================================================
# This script must be run with root privileges (e.g., using sudo).

# Check for root privileges
if [ "$(id -u)" -ne 0 ]; then
   echo "🚫 This script must be run as root. Please use sudo." >&2
   exit 1
fi

echo "🚀 Starting to increase system limits to 10 million..."

# --- Step 1: Increase Kernel's System-Wide Limit ---
echo "[1/4] Configuring kernel parameters in /etc/sysctl.conf..."

# Set the new values
SYSCTL_CONFIGS=(
"fs.file-max = 10000000"
"fs.nr_open = 10000000"
)

# Apply configs if not already set
for config in "${SYSCTL_CONFIGS[@]}"; do
    if ! grep -qF "$config" /etc/sysctl.conf; then
        echo "$config" >> /etc/sysctl.conf
        echo "  + Added: $config"
    else
        echo "  ✓ Already set: $config"
    fi
done

# Apply changes immediately
sysctl -p > /dev/null
echo "  ✓ Kernel parameters applied."

# --- Step 2: Increase User-Specific Limits ---
echo "[2/4] Configuring user limits in /etc/security/limits.conf..."

LIMITS_CONFIGS=(
"* soft nofile 10000000"
"* hard nofile 10000000"
"root soft nofile 10000000"
"root hard nofile 10000000"
)

for config in "${LIMITS_CONFIGS[@]}"; do
    # Remove old limit for the same user/type before adding new one
    grep -vE "^$(echo $config | awk '{print $1" "$2" "$3}')" /etc/security/limits.conf > /tmp/limits.conf
    mv /tmp/limits.conf /etc/security/limits.conf
    
    # Add the new limit
    echo "$config" >> /etc/security/limits.conf
done
echo "  ✓ User limits have been set."


# --- Step 3: Ensure PAM limits module is enabled ---
echo "[3/4] Ensuring PAM limits module is active..."
PAM_CONFIG="/etc/pam.d/common-session"
PAM_LINE="session required pam_limits.so"

if ! grep -qF "$PAM_LINE" "$PAM_CONFIG"; then
    echo "$PAM_LINE" >> "$PAM_CONFIG"
    echo "  + Enabled pam_limits.so module."
else
    echo "  ✓ pam_limits.so module is already enabled."
fi


# --- Step 4: Configure Systemd Limits ---
echo "[4/4] Configuring systemd default limits..."
SYSTEMD_CONFIGS=(
"/etc/systemd/system.conf"
"/etc/systemd/user.conf"
)

for conf_file in "${SYSTEMD_CONFIGS[@]}"; do
    if [ -f "$conf_file" ]; then
        # Remove existing line if it exists
        sed -i '/^DefaultLimitNOFILE=/d' "$conf_file"
        # Add the new value at the end of the [Manager] section or file
        echo "DefaultLimitNOFILE=10000000" >> "$conf_file"
        echo "  ✓ Set DefaultLimitNOFILE in $conf_file"
    fi
done

echo "-----------------------------------------------------------"
echo "✅ Success! All configurations have been updated."
echo "🔥 IMPORTANT: A full system reboot is required for all changes to take effect."
echo "After rebooting, you can verify the new limits with 'ulimit -n'."
echo "-----------------------------------------------------------"

exit 0