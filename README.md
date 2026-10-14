# introdb: IntroDB community intro data plugin

Owner: A4. Official plugin (closed source, change request 0117), implementing the protocol v1 `SegmentProvider` service with `github.com/kryspal/plugin-sdk/sdk` (change request 0122 part A). It declares the capabilities `segments.provider` and `segments.submit` and no feature code: the host's capability layer decides whether community segment data is licensed, so the plugin itself is never locked. Paid features are now gated by the host's capability layer instead of the plugin (1.0.3). **Disabled by default** (it sends IMDb IDs to a third-party service): it is bundled in the image and reserved as an official ID; after the administrator enables it on the plugin page, they then tick it under "Segment providers" in the library settings.

## Data source and terms

IntroDB (introdb.app): the terms allow reasonable usage for commercial applications, forbid reselling the data, scraping the whole database and building competing services, and recommend attribution (the research in change request 0122). This plugin follows those conditions:

- **Each user's own server queries directly**, without going through any central service.
- **Cached only locally** (in memory, 24 hours, including results with no data), never exported.
- **No bulk access**: it queries only the episode the host asks for (the host asks only about episodes without community segments, and asks again about an episode with no result only after a week); the plugin throttles itself: only one request at a time (`SuggestedConcurrency` 1, at least 1 second between two requests); a 429 returns `RESOURCE_EXHAUSTED` (with Retry-After), and the host pauses this plugin.
- **Attribution**: "Intro data provided by IntroDB (introdb.app)" goes in the manifest's attribution (the source line on the plugin page, with a link to introdb.app); the three-language descriptions do not repeat this sentence.
- **Candidates are always verified**: IntroDB does not distinguish versions and its responses carry no duration, so `duration_ms` is always 0; the host (introdetect) adopts a candidate only after spot-checking it with the audio fingerprint, and discards those that do not match.

## API

- Query: `GET {base}/segments?imdb_id=tt…&season=&episode=` (movies use `is_movie=true`), no key needed. Returns `intro`, `recap`, `outro`, `post_credits` (`null` or `{start_ms, end_ms, start_sec, end_sec, confidence, submission_count}`). Converted to candidates: intro, recap, outro (`post_credits` has no matching kind and is not used); a `confidence` outside 0-1 is treated as not provided; `submission_count` goes into the vote count. A missing IMDb ID (format `tt` plus 7 to 8 digits), an episode without season and episode numbers, 404 and other 4xx all return an empty list, with no request sent or not treated as an error.
- Submit: `POST {base}/submit`, header `X-API-Key`, with only the body fields `segment_type` (intro/recap/outro), `imdb_id`, `start_sec`, `end_sec`, `is_movie`, `season`, `episode`, `tmdb_id` (when available); **file names, paths and durations are not sent**. Next-episode previews have no matching kind and are not submitted.

## Settings

| Key | Type | Description |
| --- | --- | --- |
| `api_key` | secret | The IntroDB API key (starts with `idb_`, the key of the administrator's own account); needed only for submitting, not for queries |
| `submit` | bool, default off | Submit segments the administrator marked manually; turning it on requires a key (`Configure` blocks it otherwise) |
| `base_url` | string (advanced) | Defaults to `https://api.introdb.app`; use only with a trusted HTTPS proxy |

`Configure` does not verify the key over the network; a rejected key (401) is reported at submit time. Submitting sends only the segments handed over by the caller (the host): the terms require manual timing, so the caller may hand over only segments the administrator marked manually, and fingerprint detections and times taken from other databases must not be submitted (this restriction is on the caller's side; the plugin cannot tell where a segment came from).

## Version and bundling

The version number is written only in `VERSION` (`go:embed`, also the `<version>` of the bundled directory). `deploy/plugins/build.sh` builds it; `CATALOG.json` is the display data for the plugin catalog.

## Tests

`go test ./plugins/introdb` (`-short` skips the process test):
- `segments_test.go` (a fake IntroDB server, made-up IDs): query parameters and response conversion (intro, recap, outro, `post_credits` unused, confidence scale, seconds-to-milliseconds conversion, implausible ranges), caching (including 404), movies, targets that cannot be queried send no request, 429/5xx and errors not being cached, request spacing and one at a time, submit conditions (off by default, no key) and body fields (only those listed), rejected/429/400/401.
- `plugin_test.go`: `TestDescribe` (three languages, capabilities, feature code, attribution only in the source line, concurrency 1), `TestConfigure`, `TestPluginProcess` (compiles the executable and starts it with the host's environment variables, checking the handshake, health check, `Describe`, `Configure`, `GetSegments`, the metadata service not being registered, and graceful exit on SIGTERM).
