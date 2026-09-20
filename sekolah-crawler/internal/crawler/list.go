package crawler

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"os"

	"context"

	"sekolah-crawler/internal/api"
	"sekolah-crawler/internal/ratelimit"
)

type ListOptions struct {
	Keyword            string
	KabupatenKota      string
	BentukPendidikan   string
	StatusSekolah      string
	OutPath            string
	KabupatenCachePath string
	RatePerSec         float64
	MaxRecords         int // 0 = unlimited
	Restart            bool
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func shardLogPath(outPath string) string { return outPath + ".shards" }

var errMaxReached = errors.New("max-records reached")

var idsCSVHeader = []string{"sekolah_id", "npsn", "nama", "bentuk_pendidikan", "status_sekolah", "provinsi", "kabupaten", "kecamatan"}

func itemToRow(item api.CariSekolahItem) []string {
	return []string{
		item.SekolahID, item.Npsn, item.Nama, item.BentukPendidikan,
		item.StatusSekolah, item.Provinsi, item.Kabupaten, item.Kecamatan,
	}
}

type listState struct {
	client     *api.Client
	limiter    *ratelimit.Limiter
	w          *csv.Writer
	shardLog   *os.File
	doneShards map[string]bool
	seenIDs    map[string]bool
	written    int
	opt        ListOptions
}

func loadShardsDone(path string) (map[string]bool, error) {
	done := map[string]bool{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k := sc.Text(); k != "" {
			done[k] = true
		}
	}
	return done, sc.Err()
}

// RunList fetches every school matching the given filters. The API is
// backed by Elasticsearch with the default max_result_window: any query
// (page*size + size) exceeding 10,000 fails outright, so a broad query
// (e.g. no filters, or a single popular bentuk_pendidikan like "SD" with
// 150k+ schools) can NEVER be paged through directly. RunList works around
// this by automatically sharding: if a query's total exceeds 10,000, it
// splits by kabupaten_kota (discovering the real kabupaten list from the
// data itself — see discover.go), then by status_sekolah, then by
// bentuk_pendidikan, recursing until every shard is small enough to page
// safely. Each shard is buffered in memory and only committed to disk (and
// marked done in the shard checkpoint) once it's fetched in full, so a
// mid-shard interruption never leaves partial/duplicate rows — resuming
// just redoes that one shard.
func RunList(ctx context.Context, client *api.Client, opt ListOptions) error {
	if opt.Restart {
		os.Remove(shardLogPath(opt.OutPath))
		os.Remove(opt.OutPath)
	}

	doneShards, err := loadShardsDone(shardLogPath(opt.OutPath))
	if err != nil {
		return fmt.Errorf("load shard checkpoint: %w", err)
	}

	outIsNew := !fileExists(opt.OutPath)
	outFile, err := os.OpenFile(opt.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer outFile.Close()
	w := csv.NewWriter(outFile)
	if outIsNew {
		if err := w.Write(idsCSVHeader); err != nil {
			return err
		}
		w.Flush()
	}

	shardLog, err := os.OpenFile(shardLogPath(opt.OutPath), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer shardLog.Close()

	baseFilters := api.CariSekolahRequest{
		Keyword: opt.Keyword, KabupatenKota: opt.KabupatenKota,
		BentukPendidikan: opt.BentukPendidikan, StatusSekolah: opt.StatusSekolah,
	}

	// Small, bounded requests never need sharding — skip the (otherwise
	// harmless but slow) kabupaten-discovery bootstrap entirely.
	if opt.MaxRecords > 0 && opt.MaxRecords <= api.MaxResultWindow {
		return runSimpleList(ctx, client, baseFilters, opt)
	}

	st := &listState{
		client: client, limiter: ratelimit.New(opt.RatePerSec), w: w, shardLog: shardLog,
		doneShards: doneShards, seenIDs: map[string]bool{}, opt: opt,
	}

	err = st.crawl(ctx, baseFilters, "root")
	w.Flush()
	if flushErr := w.Error(); flushErr != nil {
		return flushErr
	}
	if err != nil && !errors.Is(err, errMaxReached) {
		return err
	}

	totalInFile, countErr := countCSVDataRows(opt.OutPath)
	if countErr != nil {
		fmt.Printf("done: %d new rows written this run to %s (warning: could not verify total row count: %v)\n", st.written, opt.OutPath, countErr)
	} else {
		fmt.Printf("done: %d new rows written this run, %d total rows in %s\n", st.written, totalInFile, opt.OutPath)
	}

	if opt.MaxRecords == 0 && countErr == nil {
		if err := st.verifyCoverage(ctx, baseFilters, totalInFile); err != nil {
			fmt.Printf("warning: could not verify coverage: %v\n", err)
		}
	}
	return nil
}

// countCSVDataRows counts data rows (excluding the header) currently in
// path — used for coverage verification, since on a resumed run where every
// shard was already done, st.written (new rows written *this* run) is 0
// even though the file already holds every row from prior runs.
func countCSVDataRows(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	n := -1 // first record is the header
	for {
		_, err := r.Read()
		if err != nil {
			break
		}
		n++
	}
	if n < 0 {
		n = 0
	}
	return n, nil
}

func (st *listState) verifyCoverage(ctx context.Context, filters api.CariSekolahRequest, actualTotal int) error {
	f := filters
	f.Page, f.Size = 0, 1
	resp, err := st.client.SearchSchools(ctx, f)
	if err != nil {
		return err
	}
	if resp.Total != actualTotal {
		fmt.Printf("NOTE: grand total for this filter is %d but %d rows are in the output (gap: %d).\n"+
			"This can happen if a small number of schools have an unusual/missing kabupaten value\n"+
			"that never matched any discovered shard. Check %s for skipped-shard warnings.\n",
			resp.Total, actualTotal, resp.Total-actualTotal, shardLogPath(st.opt.OutPath))
	} else {
		fmt.Printf("coverage verified: %d/%d schools captured.\n", actualTotal, resp.Total)
	}
	return nil
}

// crawl fetches shardKey's first page to learn its total, then either pages
// it fully (total <= 10000) or recurses into a finer split (total > 10000).
func (st *listState) crawl(ctx context.Context, filters api.CariSekolahRequest, shardKey string) error {
	if st.doneShards[shardKey] {
		return nil
	}

	f := filters
	f.Page, f.Size = 0, api.MaxPageSize
	var first *api.CariSekolahResponse
	err := withRetry(ctx, 5, func() error {
		var e error
		first, e = st.client.SearchSchools(ctx, f)
		return e
	})
	if err != nil {
		return fmt.Errorf("shard %s: %w", shardKey, err)
	}

	if first.Total == 0 {
		st.markDone(shardKey)
		return nil
	}
	if first.Total > api.MaxResultWindow {
		return st.splitFurther(ctx, filters, shardKey)
	}
	return st.pageShard(ctx, filters, shardKey, first)
}

func (st *listState) pageShard(ctx context.Context, filters api.CariSekolahRequest, shardKey string, first *api.CariSekolahResponse) error {
	buffer := make([][]string, 0, first.Total)
	for _, item := range first.Data {
		buffer = append(buffer, itemToRow(item))
	}

	page := 1
	data := first.Data
	for len(data) == api.MaxPageSize && page*api.MaxPageSize < first.Total {
		if err := st.limiter.Wait(ctx); err != nil {
			return err
		}
		f := filters
		f.Page, f.Size = page, api.MaxPageSize
		var resp *api.CariSekolahResponse
		err := withRetry(ctx, 5, func() error {
			var e error
			resp, e = st.client.SearchSchools(ctx, f)
			return e
		})
		if err != nil {
			return fmt.Errorf("shard %s page %d: %w", shardKey, page, err)
		}
		for _, item := range resp.Data {
			buffer = append(buffer, itemToRow(item))
		}
		data = resp.Data
		page++
	}

	for _, row := range buffer {
		id := row[0]
		if st.seenIDs[id] {
			continue
		}
		st.seenIDs[id] = true
		if err := st.w.Write(row); err != nil {
			return err
		}
		st.written++
	}
	st.w.Flush()
	if err := st.w.Error(); err != nil {
		return err
	}

	st.markDone(shardKey)
	fmt.Printf("shard done: %-55s total=%-6d written_so_far=%d\n", shardKey, first.Total, st.written)

	if st.opt.MaxRecords > 0 && st.written >= st.opt.MaxRecords {
		return errMaxReached
	}
	return nil
}

func (st *listState) splitFurther(ctx context.Context, filters api.CariSekolahRequest, shardKey string) error {
	switch {
	case filters.KabupatenKota == "":
		kabList, err := LoadOrDiscoverKabupaten(ctx, st.client, st.opt.KabupatenCachePath, st.opt.RatePerSec)
		if err != nil {
			return err
		}
		for _, kb := range kabList {
			f := filters
			f.KabupatenKota = kb
			if err := st.crawl(ctx, f, shardKey+"|kb="+kb); err != nil {
				return err
			}
		}
		return nil

	case filters.StatusSekolah == "":
		for _, s := range []string{"NEGERI", "SWASTA"} {
			f := filters
			f.StatusSekolah = s
			if err := st.crawl(ctx, f, shardKey+"|st="+s); err != nil {
				return err
			}
		}
		return nil

	case filters.BentukPendidikan == "":
		cats, err := st.client.ListBentukPendidikan(ctx)
		if err != nil {
			return err
		}
		for _, c := range cats {
			f := filters
			f.BentukPendidikan = c.Nama
			if err := st.crawl(ctx, f, shardKey+"|bp="+c.Nama); err != nil {
				return err
			}
		}
		return nil

	default:
		fmt.Fprintf(os.Stderr,
			"WARNING: shard %s still exceeds the %d-result window even split by kabupaten, status, and bentuk_pendidikan — skipping it (needs manual handling, e.g. a further keyword split).\n",
			shardKey, api.MaxResultWindow)
		st.markDone(shardKey)
		return nil
	}
}

func (st *listState) markDone(shardKey string) {
	st.doneShards[shardKey] = true
	fmt.Fprintln(st.shardLog, shardKey)
}

// runSimpleList is the direct, unsharded pager used when -max-records caps
// the run at or below the 10,000-result window, so sharding would only add
// overhead (the kabupaten-discovery bootstrap) for no benefit.
func runSimpleList(ctx context.Context, client *api.Client, filters api.CariSekolahRequest, opt ListOptions) error {
	outIsNew := !fileExists(opt.OutPath)
	f, err := os.OpenFile(opt.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if outIsNew {
		if err := w.Write(idsCSVHeader); err != nil {
			return err
		}
		w.Flush()
	}

	limiter := ratelimit.New(opt.RatePerSec)
	written := 0
	page := 0
	for {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}
		req := filters
		req.Page, req.Size = page, api.MaxPageSize
		var resp *api.CariSekolahResponse
		reqErr := withRetry(ctx, 5, func() error {
			var e error
			resp, e = client.SearchSchools(ctx, req)
			return e
		})
		if reqErr != nil {
			return fmt.Errorf("page %d: %w", page, reqErr)
		}

		stop := false
		for _, item := range resp.Data {
			if err := w.Write(itemToRow(item)); err != nil {
				return err
			}
			written++
			if written >= opt.MaxRecords {
				stop = true
				break
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}

		fmt.Printf("page %d fetched (%d records, %d written / %d total)\n", page, len(resp.Data), written, resp.Total)

		if stop || len(resp.Data) < api.MaxPageSize {
			break
		}
		page++
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	fmt.Printf("done: %d schools written to %s\n", written, opt.OutPath)
	return nil
}
