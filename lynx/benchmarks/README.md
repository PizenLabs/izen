# Benchmarks Help

This directory contains tools and documentation for running the project's performance benchmarks.

## Running the benchmarks

The project uses **Cargo benchmarks** (Rust). To execute all benchmarks and capture the output, use the provided helper script:

```bash
cd $(git rev-parse --show-toplevel)  # ensure you are at the repository root
./benchmarks/run_benchmarks.sh
```

The script will:
1. Run `cargo bench`.
2. Store the raw console output in `benchmarks/reports/<timestamp>.txt`.
3. Append a short summary (elapsed time) to `benchmarks/reports/summary.md`.

## Helper script (`run_benchmarks.sh`)

The script is located in this folder and is executable. It automatically creates the `benchmarks/reports` directory if it does not exist.

```bash
#!/usr/bin/env bash
set -euo pipefail

# Create reports directory if missing
mkdir -p "$(dirname "$0")/reports"

# Timestamp for the report file
TS=$(date +"%Y%m%d_%H%M%S")
REPORT_FILE="$(dirname "$0")/reports/${TS}.txt"

# Run benchmarks and capture output
echo "Running cargo bench..."
cargo bench 2>&1 | tee "$REPORT_FILE"

# Append a short summary
ELAPSED=$(grep -o "Finished `bench` profile \[.*\] target(s) in [0-9.]*s" "$REPORT_FILE" | tail -1 || echo "No summary line found")

cat <<EOF >> "$(dirname "$0")/reports/summary.md"
## ${TS}
- Report: [${TS}.txt](file://${REPORT_FILE})
- ${ELAPSED}

EOF

echo "Benchmark results saved to $REPORT_FILE"
```

## Viewing reports

Each benchmark run generates a timestamped `.txt` file inside `benchmarks/reports`. You can open them directly or view the consolidated `summary.md` for a quick overview.

## Adding new benchmarks

Add new benchmark functions in the `benches/` directory following Cargo's standard benchmark conventions. After adding them, run the helper script again to generate new reports.

---
