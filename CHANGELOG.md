# Changelog

## [v0.2]

### Added

- Negative caching for 404 responses: non-existent paths are cached and served with informative `X-Cache-Status`, `X-Cache-Expires`, and `X-Negative-Cache` headers
- ETag/Last-Modified passthrough: conditional GET revalidation now uses upstream `ETag` and `Last-Modified` values so clients can revalidate efficiently
- Range request and correct `Content-Length` support via `http.ServeContent`
- GitHub Actions workflow to run tests on pull requests

### Changed

- Pre-compile `CachingRules` regexes at startup instead of per-request for better performance

### Fixed

- Goroutine leak and missing flush in cache writer
- `PATCH` requests on non-cached items now return 404 instead of silently doing nothing
- Added helpful `X-Pkgproxy-Error` response header and body when the remote URL is unreachable

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
