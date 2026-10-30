# BUG: proxy runs out of memory on large downloads without `Content-Length`

## Summary

A download whose upstream response has no `Content-Length` (reported as `contentLength == -1`) is always put into the in-memory cache, even with `max_cache_item_size_in_mb: 0` (which is meant to disable in-memory caching). A multi-GB file therefore gets loaded completely into RAM, the Go runtime dies with `fatal error: runtime: out of memory`, systemd restarts the service, and the client receives an empty response.

## Root cause

[`cache.go:387`](cache.go) decides on the declared length instead of the number of bytes actually written to disk:

```go
if contentLength <= config.MaxCacheItemSize*1024*1024 {
    // write to in-memory
    olo.Debug("Content size of %s is %d not larger than max_cache_item_size_in_mb %d so we write it also into a memory buffer", ...)
    err = c.fillInMemoryCacheWithFileContent(cacheFile, requestedURL)
```

With an unknown length `contentLength` is `-1`, and `-1 <= 0` is true for every limit, including `0`. The other two size checks (`cache.go:162` and `cache.go:302`) use `fi.Size()` and are correct.

## Evidence

Host `itohi-pkgproxy-live-bs02.server.lan`, `tiny-http-proxy.service`, `max_cache_item_size_in_mb: 0`, request from `10.76.24.67` on 2026-10-30:

```
15:07:05 Incoming request '/releases.rancher.com/harvester/v1.10.0-rc1/harvester-v1.10.0-rc1-amd64.iso'
15:07:05 CACHE_MISS ... GETing https://releases.rancher.com/... with proxy http://itproxy.1and1.org:3128
15:10:04 Wrote content size 8336965632 of entry ...-amd64.iso into file /data/tiny-http-proxy/cache-https/...
15:10:04 Content size of ...-amd64.iso is -1 not larger than max_cache_item_size_in_mb 0 so we write it also into a memory buffer
15:10:18 fatal error: runtime: out of memory
15:10:20 tiny-http-proxy.service: Main process exited, code=exited, status=2/INVALIDARGUMENT
15:10:21 tiny-http-proxy.service: Scheduled restart job, restart counter is at 1.
```

The same host had 23 GiB RAM (12 GiB free) afterwards. The client (`curl -f`) got HTTP 200 with an empty body and saved a 0-byte file. A 1.2 GB file (`...-rootfs-amd64.squashfs`) through the same path (also `-1`, in-memory add logged at 15:07:00) worked.

## Impact

Any large file without `Content-Length` crashes the shared proxy for all users and its in-flight requests, and the client sees an empty 200.

## Fix

- Decide on `written` (bytes written by `io.Copy` at `cache.go:378`) instead of `contentLength`: `if written <= config.MaxCacheItemSize*1024*1024`.
- Treat `max_cache_item_size_in_mb: 0` as "never buffer in memory".
- The log line at `cache.go:389` prints the limit in bytes under the label `max_cache_item_size_in_mb`; print the MB value.
- Optional: when the upstream response is cut short or the in-memory fill fails, do not answer 200 with an empty body.

## Test to add

`main_test.go:469` covers `MaxCacheItemSize=0` with a known size only. Add a case where the upstream sends no `Content-Length` (chunked, `contentLength == -1`) and assert that the item is served from disk and not added to the in-memory cache, for `MaxCacheItemSize=0` and for a small non-zero limit with a body larger than the limit.

## Workaround until fixed

Download large files (several GB, e.g. ISOs) directly from the origin through the system proxy and not through pkgproxy.
