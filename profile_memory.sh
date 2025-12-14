#!/bin/bash
# Memory profiling script for load testing

echo "=== Memory Profiling Setup ==="
echo ""

# Clean profiles directory
rm -rf profiles/heap_*.prof
mkdir -p profiles

echo "Step 1: Baseline heap profile (before load test)..."
sleep 3
echo "  (Skipping - Traefik doesn't expose pprof by default)"

echo ""
echo "Step 2: Running load test with memory profiling..."
go test -bench=BenchmarkProcessor_LargePage \
    -benchmem \
    -memprofile=profiles/heap_during.prof \
    -cpuprofile=profiles/cpu_during.prof \
    -benchtime=10s \
    ./tests/

echo ""
echo "Step 3: Analyzing heap profile..."
echo "--- Top memory allocators ---"
go tool pprof -top -cum profiles/heap_during.prof > profiles/heap_analysis_top.txt
cat profiles/heap_analysis_top.txt

echo ""
echo "Step 4: Detailed allocation list..."
go tool pprof -list=".*" -cum profiles/heap_during.prof > profiles/heap_analysis_detailed.txt

echo ""
echo "Step 5: Check for memory leaks (in-use vs alloc)..."
go tool pprof -sample_index=inuse_space -top profiles/heap_during.prof > profiles/heap_inuse.txt
echo "--- Currently in-use memory (potential leaks) ---"
cat profiles/heap_inuse.txt | head -20

echo ""
echo "=== Analysis Complete ==="
echo ""
echo "Files created:"
echo "  - profiles/heap_during.prof (raw data)"
echo "  - profiles/heap_analysis_top.txt (top allocators)"
echo "  - profiles/heap_inuse.txt (currently in-use)"
echo ""
echo "Interactive viewing:"
echo "  go tool pprof -http=:8080 profiles/heap_during.prof"
echo ""
echo "Key metrics to check:"
cat profiles/heap_inuse.txt | head -5
