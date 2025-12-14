#!/bin/bash
# Comprehensive profiling and benchmarking script

echo "=== SSI Plugin Profiling & Benchmarking ==="
echo ""

# Create profiles directory
mkdir -p profiles

echo "1. Running benchmarks with memory profiling..."
go test -bench=. -benchmem -memprofile=profiles/mem.prof -cpuprofile=profiles/cpu.prof ./tests/ 2>&1 | tee profiles/benchmark_results.txt

echo ""
echo "2. Analyzing memory profile..."
go tool pprof -top -cum profiles/mem.prof > profiles/mem_analysis.txt
echo "   Saved to: profiles/mem_analysis.txt"

echo ""
echo "3. Analyzing CPU profile..."
go tool pprof -top -cum profiles/cpu.prof > profiles/cpu_analysis.txt
echo "   Saved to: profiles/cpu_analysis.txt"

echo ""
echo "4. Generating allocation statistics..."
go test -bench=BenchmarkProcessor_LargePage -benchmem ./tests/ | grep -E "(Benchmark|allocs)" > profiles/alloc_stats.txt
echo "   Saved to: profiles/alloc_stats.txt"

echo ""
echo "=== Profile Analysis Complete ==="
echo ""
echo "View results:"
echo "  - Benchmark: cat profiles/benchmark_results.txt"
echo "  - Memory:    cat profiles/mem_analysis.txt"
echo "  - CPU:       cat profiles/cpu_analysis.txt"
echo "  - Allocs:    cat profiles/alloc_stats.txt"
echo ""
echo "Interactive analysis:"
echo "  - Memory: go tool pprof -http=:8081 profiles/mem.prof"
echo "  - CPU:    go tool pprof -http=:8082 profiles/cpu.prof"
