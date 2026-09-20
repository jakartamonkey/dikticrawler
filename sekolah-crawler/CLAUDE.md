# sekolah-crawler

Crawls sekolah.data.kemendikdasmen.go.id (Angular SPA, undocumented JSON API under `/v1/sekolah-service/...`, reverse-engineered — not a published contract).

- To find real API calls on this or similar gov SPA sites: `npm install playwright` + `npx playwright install chromium`, then `page.on('request'/'response')` — curl only returns the SPA shell, never the data.
- **Critical gotcha**: backend is Elasticsearch with default `max_result_window=10000`. Any query with `page*size+size > 10000` returns HTTP 500 ("Result window is too large") — a hard ceiling per query, not transient, retrying never helps.
  - `list`'s workaround (`internal/crawler/discover.go` + `list.go`): check `total` from page 0; if ≤10000 page directly, else shard the query so every sub-query stays under the window.
  - Sharding order: split by `kabupaten_kota` first, then `status_sekolah`, then `bentuk_pendidikan` if a shard is still too big (kabupaten alone has always been enough in practice — largest kabupaten, Kab. Bogor, tops out ~9,173 combined).
  - There's no endpoint listing all kabupaten/kota, so the real ~525-entry list is *discovered from the data itself*: page every `bentuk_pendidikan` category up to 10000 rows (safe, within the window) and collect distinct `kabupaten` values seen. Cached to `kabupaten_master.txt`.
  - Each shard is buffered in memory, only written + marked done (`<out>.shards`) once fully fetched — an interrupted run just redoes the one in-progress shard, never duplicates.
  - After a run, re-queries the true grand total and reports actual-vs-expected row count (`coverage verified: N/N`) — catches edge cases like schools with `provinsi="Luar Negeri"` (overseas Indonesian schools) that might not match any discovered shard.
  - Don't reintroduce flat pagination without this — it will silently cap at the first 10,000 results.
- `kabupaten_kota` filter requires an exact string match (e.g. `"Kab. Bogor"`, `"Kota Adm. Jakarta Timur"`) — no substring/province-level matching, and no province filter exists in the API at all.
- Stdlib only, no external Go deps by design (works offline) — keep it that way.
- See README.md for full usage/flags; it's kept in sync with actual behavior.
