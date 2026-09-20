# sekolah-crawler

Crawls the public school database at `sekolah.data.kemendikdasmen.go.id`
(Kemendikdasmen "Sekolah Kita") via its underlying JSON API. The site is an
Angular SPA — this API is undocumented, reverse-engineered by inspecting the
app's network calls, not a published/stable contract. Endpoints or fields may
change without notice.

## What it fetches

- **Listing**: `POST /v1/sekolah-service/sekolah/cari-sekolah` — paginated
  (max page size 100, server-enforced), filterable by keyword,
  kabupaten/kota, bentuk_pendidikan (SD/SMP/SMA/SMK/TK/MI/MTs/MA/PKBM/...),
  status_sekolah (NEGERI/SWASTA). As of writing, ~558,000 schools nationwide.
- **Detail**: `GET /v1/sekolah-service/sekolah/full-detail/{sekolah_id}` —
  one call per school. No bulk/batch endpoint exists. Returns NPSN, website,
  email, yayasan (foundation), all curriculum records, facility/utility
  inventory (classrooms, library, labs — condition-rated), and school
  statistics (student/teacher counts and ratios).

## Why two phases

The listing endpoint is cheap (~5,600 requests for the whole country at
page size 100). The detail endpoint is not: it's one request *per school*,
with no way to batch. Crawling full detail for all ~558k schools is
~558k sequential requests — at a considerate rate that's many hours, and
it's a live government server, not a bulk-data API. Splitting into two
phases lets you:

- Get the full national listing quickly (minutes).
- Decide how much detail you actually need — a filtered subset (one
  province, one education level) finishes in a reasonable time; the full
  national detail crawl is a genuinely long-running job you should expect
  to run for hours, ideally overnight, and can safely interrupt and resume.

## A hard limit: the API can never page past 10,000 results for one query

The backend is Elasticsearch with its default `max_result_window`: any
query where `page * size + size > 10000` fails outright with a 500
(`Result window is too large`) — not a transient error, retrying never
helps. So an unfiltered listing crawl, or any single `bentuk_pendidikan`
category over 10,000 (SD, TK, KB, SMP, MI, MTs, SMA, SMK, MA, Kursus, PKBM,
SPS all are), can *never* be paged through directly — you'd silently get
stuck at the first 10,000 of 558,071 schools.

`list` handles this automatically: whenever a query's total exceeds
10,000, it shards the query — first by `kabupaten_kota` (discovering the
real set of ~525 kabupaten/kota strings straight from the data itself,
since there's no endpoint that lists them and the autocomplete endpoint is
capped to a handful of suggestions per query — see `internal/crawler/discover.go`),
then by `status_sekolah`, then by `bentuk_pendidikan`, recursing until every
shard is small enough to page safely (empirically, splitting by kabupaten
alone is already enough — the single largest, Kab. Bogor, tops out around
9,173 combined across all school types). The discovered kabupaten list is
cached to `kabupaten_master.txt` so it's only computed once. After a run,
`list` re-queries the true total and reports whether the row count in your
output file actually matches it, e.g.:

```
coverage verified: 10412/10412 schools captured.
```

If a `-max-records` cap keeps a run at or under 10,000 rows, `list` skips
all of this and just pages directly — no point paying for kabupaten
discovery on a quick sample.

## Build

```
go build -o sekolah-crawler .
```

No external dependencies — standard library only.

## Usage

### 1. List schools into a CSV of ids

```
./sekolah-crawler list -out ids.csv \
  -bentuk-pendidikan SMA \
  -kabupaten-kota "Kab. Bogor" \
  -rate 3
```

Flags: `-keyword`, `-kabupaten-kota`, `-bentuk-pendidikan`, `-status-sekolah`
(all optional filters, empty = no filter), `-out`, `-kabupaten-cache`
(default `kabupaten_master.txt` — see the 10,000-result-limit section
above), `-rate` (req/s, default 3), `-max-records` (0 = unlimited),
`-restart` (ignore checkpoint, start over).

Progress is checkpointed at the shard level to `<out>.shards` (a shard is
one fully-fetched, safely-sized query — see above). A shard is only
written to `<out>` and marked done once it's fetched in full, so
interrupting mid-shard never leaves partial/duplicate rows; re-running the
identical command skips every finished shard and resumes the one that was
in progress.

### 2. Fetch full detail for each id

```
./sekolah-crawler detail -ids ids.csv -out sekolah_detail.csv \
  -concurrency 5 -rate 5 \
  -raw-jsonl sekolah_detail.raw.jsonl
```

Flags: `-ids` (input from step 1), `-out` (flattened CSV), `-checkpoint`
(done-id log, default `detail_done.txt`), `-error-log` (failed ids, default
`detail_errors.tsv` — these are automatically retried on the next run since
they're never added to the checkpoint), `-raw-jsonl` (optional: also append
the complete, untouched JSON response per school — use this if you need
fields beyond what's flattened into the CSV, e.g. `sekolah_sekitar` /
nearby-school data or photo URLs), `-concurrency` (parallel workers,
default 5), `-rate` (req/s shared across all workers, default 5),
`-max-records`, `-restart`.

Interrupt with Ctrl+C at any time — it finishes in-flight requests and
exits cleanly. Re-run the same command to continue from the last
successfully written school.

### One part at a time

`split` divides `ids.csv` into parts that `detail` can then process one at
a time — pause between parts, check results, stop after N, or retry just
one that failed, all without touching the others. Two ways to define
"part":

**Fixed-size chunks (recommended).** Every part has the same number of
rows, so every part takes roughly the same, predictable amount of time:

```
./sekolah-crawler list  -out ids.csv
./sekolah-crawler split -ids ids.csv -chunk-size 10000 -out-dir parts
```

This writes `parts/part_0001.csv`, `parts/part_0002.csv`, ... (56 parts of
10,000 for the full ~558k-school dataset). Then work through them:

```
for f in parts/*.csv; do
  name=$(basename "$f" .csv)
  ./sekolah-crawler detail -ids "$f" \
    -out "detail_${name}.csv" -checkpoint "done_${name}.txt" \
    -error-log "err_${name}.tsv" -concurrency 5 -rate 5
done
```

or run just one part by hand (`-ids parts/part_0007.csv ...`) whenever
you're ready for the next one, rather than looping through all of them.

**By province (not recommended as the primary unit).** We initially
considered `-by provinsi`, but Indonesia's schools are extremely unevenly
distributed across its 38 provinces (Java's provinces alone hold a large
share) — the API also has no province filter of its own to test this
against directly (confirmed: querying `kabupaten_kota` with a province
name, or any partial string, returns zero results; it only matches an
exact kabupaten/kota string like `Kab. Bogor`). So province-sized parts
finish in wildly different times — some in minutes, others in many hours —
which defeats the point of predictable, one-at-a-time chunks. Grouping is
still there if you want parts that mean something geographically rather
than evenly sized:

```
./sekolah-crawler split -ids ids.csv -by provinsi -out-dir by_provinsi
# or: -by kabupaten, -by bentuk_pendidikan
```

You can combine both: group by province first, then re-split an oversized
province's file again with `-chunk-size` to break it into manageable parts:

```
./sekolah-crawler split -ids by_provinsi/prov_jawa_barat.csv \
  -chunk-size 10000 -out-dir parts_jawa_barat
```

Each loop/command only starts once the previous part's `detail` run
returns, so this always processes parts sequentially (never in parallel) —
if interrupted mid-part, re-running the same command resumes it via its
checkpoint; already-finished parts are skipped almost instantly (their
pending count will be 0).

### 3. Quick single-school check

```
./sekolah-crawler test CD43726E-9646-446B-83BB-BB422822C7E9
```

Prints the raw JSON for one school — useful for sanity-checking before a
big run.

## Output CSV columns

`sekolah_id, npsn, nama, bentuk_pendidikan, status_sekolah, akreditasi,
provinsi, kabupaten, kecamatan, nama_dusun, alamat_jalan, rt, rw, kode_pos,
lintang, bujur, nomor_telepon, email, website, yayasan_id, yayasan,
waktu_penyelenggaraan, semester_id, semester_keterangan, luas_tanah_milik,
luas_tanah_bukan_milik, daya_listrik, sumber_listrik, akses_internet,
akses_internet_2`, then 40 facility/utility columns (`ruang_kelas_*`,
`ruang_perpustakaan_*`, `laboratorium_{bahasa,ipa,ips,komputer,multimedia,
fisika,kimia,biologi}_*`, each x `baik/rusak_ringan/rusak_sedang/rusak_berat`),
then `kurikulum_terbaru, kurikulum_semester_terbaru, kurikulum_all` (all
curriculum records across semesters, semicolon-joined), then statistics:
`ptk_guru_l, ptk_guru_p, jml_pd, jml_pd_l, jml_pd_p, jml_rombel,
rasio_siswa_rombel, rasio_rombel_ruang_kelas, rasio_siswa_guru,
persentase_guru_klasifikasi, persentase_guru_sertifikasi,
persentase_guru_asn, persentase_ruang_kelas_layak`.

Not flattened into CSV (available via `-raw-jsonl` instead): `foto_sekolah`
(photo URLs), `sekolah_sekitar` (nearby schools), `instansi_sekitar` (nearby
government offices), `daya_tampung`, `file_operasional`.

## Being a good citizen about it

This hits a live public-sector server, not a bulk-export API. Defaults are
deliberately conservative (3-5 req/s). Don't crank `-concurrency`/`-rate`
up for a full national run — a few hours saved isn't worth being the reason
the site falls over for actual citizens trying to look up their kid's
school. If you get sustained 429s or 5xxs, back off further, not less.
