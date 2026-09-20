# pddikti-crawler

Crawls pddikti.kemdiktisaintek.go.id (React SPA, undocumented JSON API under `/api/...` and `/api/v2/...`, reverse-engineered — not a published contract).

- API requires a `Referer` header matching the site (e.g. `https://pddikti.kemdiktisaintek.go.id/perguruan-tinggi`) — a WAF check, not auth. Omit it and every request gets a 403.
- No 10k-result-window problem here (unlike sekolah-crawler) — total institutions (~6,910) and the page-size cap (100, same server-enforced convention) never approach any pagination ceiling. `list` is a single unsharded pass.
- **The listing endpoint (`/api/v2/pt/search/filter`) throttles hard under sustained requests** — observed response times climbing 13s → 15s → 19s across just a handful of consecutive calls. Not a hang, not our bug — just be patient (client timeout is 90s to tolerate it) and don't raise `-rate` expecting it to help; the server's own slowness dominates regardless of our pacing. The detail endpoints (`/api/pt/detail/...`, `/api/pt/prodi/...`, etc.) are unaffected and respond in <1s.
- **`id_sp` is not a stable ID in the usual sense** — the same institution gets a different encrypted `id_sp` token every time you re-fetch the listing. Verified a token still resolves correctly after 40+ minutes, so these are just multiple valid encrypted encodings of one permanent underlying ID, not short-lived session tokens — the list-now/detail-later architecture is safe. Don't assume two different `id_sp` values are different institutions without checking `nama_pt`.
- Each institution's full detail costs ~7 requests (detail, prodi, rasio, mahasiswa, waktu-studi, graduation-rate, cost-range) — `name-histories` and `sarpras-file-name` are fetched by the site's UI but skipped here (both returned `null` in testing, low value).
- The `prodi` sub-endpoint needs a semester code (`-semester` flag, default `20251` as sniffed from the live frontend) — no discovery endpoint for it, may need bumping over time.
- Stdlib only, no external Go deps, single `main.go` (much simpler problem than sekolah-crawler — no sharding/discovery machinery needed, resist the urge to copy that structure over).
