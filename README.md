# Traefik SSI Plugin

A high-performance [Server-Side Includes (SSI)](https://en.wikipedia.org/wiki/Server_Side_Includes) middleware plugin for Traefik, utilizing the Yaegi interpreter.

## Features

- **Standard SSI Support**: `<!--#include virtual="..." -->` and `file="..."`.
- **Concurrent Processing**: Fetches includes in parallel using a worker pool (limited concurrency per request).
- **In-Memory Caching**: Caches include responses for a configurable TTL to ensure low latency.
- **Circuit Breaker**: Protects against cascading failures from upstream include errors.
- **Streaming Architecture**: Processes HTML streams with low memory footprint, avoiding full-response buffering where possible.
- **Production Ready**: Includes unit tests, integration tests, and performance benchmarks.

## Configuration

This plugin is configured via Traefik's dynamic configuration (middleware).

### Docker Compose Example
```yaml
labels:
  - "traefik.http.middlewares.my-ssi.plugin.ssi.includeTimeoutSeconds=5"
  - "traefik.http.middlewares.my-ssi.plugin.ssi.cacheTTLSeconds=300"
  - "traefik.http.middlewares.my-ssi.plugin.ssi.maxConcurrency=8"
```

### Static Config (traefik.yml)
To enable the plugin locally:
```yaml
experimental:
  localPlugins:
    ssi:
      moduleName: github.com/username/traefik-plugin-ssi
```

## Architecture

1.  **Parser**: A streaming token scanner that identifies `<!--#include` tags and splits the content into "Static Segments" and "Include Segments".
2.  **Fetcher**: Handles identifying and fetching the include resources (via sub-requests through Traefik). It manages Caching and Circuit Breaking.
3.  **Processor**: Orchestrates the scanning and fetching. It maintains the order of provisions (Static -> Include -> Static) while fetching includes concurrently in the background.

## Performance

Target functionality includes:
-   **Fast Path**: Bypasses non-HTML content immediately.
-   **Zero-Copy Parsing**: Minimizes allocations during scanning (naive implementation verified).
-   **Benchmark Results**:
    -   ~9,000 RPS on local Docker setup (Apple Silicon).
    -   P95 Latency: ~20ms.

## Development

### Running Tests
```bash
go test -v ./tests/...
```

### Integration Test
Start the environment:
```bash
docker compose up -d
```
Visit `http://localhost/index.html` to see SSI in action.
```bash
curl http://localhost/index.html
```

### Benchmarking
Requires `hey` or `wrk`.
```bash
hey -n 5000 -c 100 http://localhost/index.html
```
