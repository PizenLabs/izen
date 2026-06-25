#!/usr/bin/env bash
set -euo pipefail

# Determine the directory of this script
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Create reports directory if missing
mkdir -p "${SCRIPT_DIR}/reports"

# Timestamp for the report file
TS=$(date +"%Y%m%d_%H%M%S")
REPORT_FILE="${SCRIPT_DIR}/reports/${TS}.txt"

# Run benchmarks and capture output
echo "Running cargo bench..."
# Ensure we are at the repository root for cargo
cd "$(git rev-parse --show-toplevel)"
cargo bench 2>&1 | tee "$REPORT_FILE"

# Append a short summary to summary.md
ELAPSED=$(grep -o "Finished `bench` profile \[.*\] target(s) in [0-9.]*s" "$REPORT_FILE" | tail -1 || echo "No summary line found")

cat <<EOF >> "${SCRIPT_DIR}/reports/summary.md"
## ${TS}
- Report: [${TS}.txt](file://${REPORT_FILE})
- ${ELAPSED}

EOF

echo "Benchmark results saved to $REPORT_FILE"
