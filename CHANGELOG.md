# Changelog

## [v0.4] - 2026-10-30

### Added

- Log the effective upstream download speed (MB/s and Mbit/s) for every item fetched from upstream (`UPSTREAM_DOWNLOAD` log line)

### Fixed

- Downloads without a `Content-Length` (chunked upstream responses, `contentLength == -1`) were always put into the in-memory cache, even with `max_cache_item_size_in_mb: 0`, which ran multi-GB files out of memory and crashed the service. The decision is now based on the bytes actually written, and `max_cache_item_size_in_mb: 0` never buffers in memory
- A download that failed midway (upstream cut the connection or sent fewer bytes than announced) was answered with the upstream's `200` and an empty or truncated body. The proxy now answers `502` with `X-Pkgproxy-Error: upstream-download-failed` and removes the partial cache file so it is never served as a cache hit
- The in-memory cache debug log printed the limit in bytes under the label `max_cache_item_size_in_mb`

### Changed

- Updated vendor dependencies: `prometheus/client_golang` v1.24.1 → v1.25.0, `prometheus/common` v0.71.0 → v0.72.1, `gookit/color` v1.6.1 → v1.6.3, `mattn/go-colorable` v0.1.15 → v0.1.16, `xo/terminfo` v1.0.0 → v1.2.0, `golang.org/x/sys` v0.48.0 → v0.49.0, `golang.org/x/term` v0.46.0 → v0.47.0
- `/metrics` responses now carry a `Vary: Accept, Accept-Encoding` header (from `client_golang` v1.25.0)

## [v0.3]

### Changed

- Updated Go toolchain from 1.25.0 to 1.26.7
- Simplified module path from `github.com/xorpaul/pkgproxy` to `pkgproxy`
- Updated vendor dependencies: `prometheus/client_golang` v1.23.2 → v1.24.1, `prometheus/common` v0.67.5 → v0.71.0, `prometheus/procfs` v0.20.1 → v0.22.0, `prometheus/client_model` v0.6.2 → v0.6.3, `mattn/go-colorable` v0.1.14 → v0.1.15, `mattn/go-isatty` v0.0.22 → v0.0.24, `xo/terminfo` v0.0.0 → v1.0.0, `golang.org/x/sys` v0.45.0 → v0.48.0, `golang.org/x/term` v0.43.0 → v0.46.0, `google.golang.org/protobuf` v1.36.11 → v1.36.12
- Removed `go.yaml.in/yaml/v2` indirect dependency (replaced by `gopkg.in/yaml.v2`)

## [v0.2]

### Added

- Negative caching for 404 responses: non-existent paths are cached and served with informative `X-Cache-Status`, `X-Cache-Expires`, and `X-Negative-Cache` headers
- ETag/Last-Modified passthrough: conditional GET revalidation now uses upstream `ETag` and `Last-Modified` values so clients can revalidate efficiently
- Range request and correct `Content-Length` support via `http.ServeContent`
- GitHub Actions workflow to run tests on pull requests ([#26](https://github.com/xorpaul/pkgproxy/pull/26))

### Changed

- Pre-compile `CachingRules` regexes at startup instead of per-request for better performance ([#27](https://github.com/xorpaul/pkgproxy/pull/27))

### Fixed

- Goroutine leak and missing flush in cache writer ([#27](https://github.com/xorpaul/pkgproxy/pull/27))
- `PATCH` requests on non-cached items now return 404 instead of silently doing nothing ([#28](https://github.com/xorpaul/pkgproxy/pull/28))
- Added helpful `X-Pkgproxy-Error` response header and body when the remote URL is unreachable ([#26](https://github.com/xorpaul/pkgproxy/pull/26))

## [v0.1.2]

### Changed

- Updated vendor dependencies

### Fixed

- Removed unused code; cleaned up build artifacts from `.gitignore`

## [v0.1.1]

### Added

- New `prom_listen` config setting to configure the Prometheus metrics HTTP endpoint address separately from the main listener

### Fixed

- Print statement formatting

## [v0.1]

### Added

- Renamed user-agent to `pkgproxy`
- Updated vendor dependencies

## [v0.0.13]

### Changed

- Restricted TLS cipher suite to a more conservative, secure set

## [v0.0.12]

### Added

- Example systemd unit file

### Changed

- Project renamed to `pkgproxy`
- Print IP address in use on startup

### Fixed

- Various print format issues
- Disk cache fix for items not being served correctly

## [v0.0.11]

### Added

- `no_ssl` config setting to disable the TLS listener

### Fixed

- Fixed cache prefill for disk-only items larger than `max_cache_item_size_in_mb`
- Fixed deprecated function usage

## [v0.0.10]

### Fixed

- Fixed already-existing HTTP client reuse for download requests

## [v0.0.9]

### Fixed

- Fixed requests with no timeout leading to too many open file descriptors and service outage

## [v0.0.8]

### Changed

- Disabled CGO to remove libc dependency (fully static binary)

## [v0.0.7]

### Fixed

- Use complete request URL including query parameters so metalink-style requests (e.g. CentOS) work correctly

## [v0.0.6]

### Added

- HTTP `PATCH` method support to invalidate a cached item

### Fixed

- Fixed GET timing issues

## [v0.0.5]

### Added

- Prefill in-memory cache on startup (`prefill_cache_on_startup` config option)
- `HEAD` request support

### Fixed

- Fixed in-memory cache prefill for small files
- Stopped using `io.TeeReader` for large downloads to prevent OOM

## [v0.0.4]

### Added

- Custom `User-Agent` header on upstream requests

## [v0.0.3]

### Fixed

- Fixed empty cache files when the `io.Reader` was already consumed by the in-memory cache write

## [v0.0.2]

### Added

- Initial public release
- HTTP reverse-proxy with configurable caching rules (in-memory and disk)
- TLS listener support
- Prometheus metrics endpoint
