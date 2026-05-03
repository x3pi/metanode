#!/bin/bash

# Script to clean up generated test and debug reports

cd "$(dirname "$0")"

echo "🧹 Cleaning up report files in scripts directory..."
rm -f stability_report_*.md
rm -f debug_report_*.md
echo "✅ Cleanup complete."
