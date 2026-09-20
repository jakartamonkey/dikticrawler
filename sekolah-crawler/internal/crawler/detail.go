package crawler

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"sekolah-crawler/internal/api"
	"sekolah-crawler/internal/ratelimit"
)

type DetailOptions struct {
	IDsPath      string // input CSV produced by RunList
	OutPath      string
	DoneLogPath  string // append-only log of completed sekolah_id, enables resume
	ErrorLogPath string
	RawJSONLPath string // optional: also append the untouched JSON body per school
	Concurrency  int
	RatePerSec   float64
	MaxRecords   int // 0 = unlimited
	Restart      bool
}

type idRecord struct {
	SekolahID, Npsn, Nama string
}

func loadDoneSet(path string) (map[string]bool, error) {
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
		if id := sc.Text(); id != "" {
			done[id] = true
		}
	}
	return done, sc.Err()
}

func readIDs(path string) ([]idRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) <= 1 {
		return nil, nil
	}
	out := make([]idRecord, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 3 {
			continue
		}
		out = append(out, idRecord{SekolahID: row[0], Npsn: row[1], Nama: row[2]})
	}
	return out, nil
}

// RunDetail fetches full-detail for every pending sekolah_id (those in
// IDsPath not already present in DoneLogPath) using Concurrency workers,
// all sharing one RatePerSec limiter. Every successful fetch is written to
// OutPath (CSV, flattened) and, if set, RawJSONLPath (untouched JSON line),
// then the id is appended to DoneLogPath — so killing the process and
// re-running the same command resumes right after the last completed id.
func RunDetail(ctx context.Context, client *api.Client, opt DetailOptions) error {
	if opt.Restart {
		os.Remove(opt.DoneLogPath)
		os.Remove(opt.OutPath)
		if opt.RawJSONLPath != "" {
			os.Remove(opt.RawJSONLPath)
		}
		if opt.ErrorLogPath != "" {
			os.Remove(opt.ErrorLogPath)
		}
	}

	ids, err := readIDs(opt.IDsPath)
	if err != nil {
		return fmt.Errorf("read ids (%s): %w", opt.IDsPath, err)
	}
	done, err := loadDoneSet(opt.DoneLogPath)
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}

	pending := make([]idRecord, 0, len(ids))
	for _, r := range ids {
		if !done[r.SekolahID] {
			pending = append(pending, r)
		}
	}
	fmt.Printf("total ids: %d, already done: %d, pending: %d\n", len(ids), len(done), len(pending))
	if opt.MaxRecords > 0 && len(pending) > opt.MaxRecords {
		pending = pending[:opt.MaxRecords]
	}
	if len(pending) == 0 {
		fmt.Println("nothing to do")
		return nil
	}

	outIsNew := !fileExists(opt.OutPath)
	outFile, err := os.OpenFile(opt.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer outFile.Close()
	csvW := csv.NewWriter(outFile)
	if outIsNew {
		if err := csvW.Write(DetailCSVHeader()); err != nil {
			return err
		}
		csvW.Flush()
	}

	doneFile, err := os.OpenFile(opt.DoneLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer doneFile.Close()

	var errFile *os.File
	if opt.ErrorLogPath != "" {
		errFile, err = os.OpenFile(opt.ErrorLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		defer errFile.Close()
	}

	var jsonlFile *os.File
	if opt.RawJSONLPath != "" {
		jsonlFile, err = os.OpenFile(opt.RawJSONLPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		defer jsonlFile.Close()
	}

	var writeMu sync.Mutex
	limiter := ratelimit.New(opt.RatePerSec)
	jobs := make(chan idRecord)
	var wg sync.WaitGroup
	var processed, failed int64

	worker := func() {
		defer wg.Done()
		for rec := range jobs {
			if ctx.Err() != nil {
				return
			}
			if err := limiter.Wait(ctx); err != nil {
				return
			}

			var resp *api.FullDetailResponse
			var raw []byte
			fetchErr := withRetry(ctx, 5, func() error {
				var e error
				resp, raw, e = client.FullDetail(ctx, rec.SekolahID)
				return e
			})

			writeMu.Lock()
			if fetchErr != nil {
				if errFile != nil {
					fmt.Fprintf(errFile, "%s\t%s\t%v\n", rec.SekolahID, rec.Nama, fetchErr)
				}
				atomic.AddInt64(&failed, 1)
				writeMu.Unlock()
				continue
			}

			row := FlattenDetail(rec.SekolahID, &resp.Data)
			_ = csvW.Write(row)
			csvW.Flush()
			if jsonlFile != nil {
				jsonlFile.Write(raw)
				jsonlFile.Write([]byte("\n"))
			}
			fmt.Fprintln(doneFile, rec.SekolahID)
			p := atomic.AddInt64(&processed, 1)
			writeMu.Unlock()

			if p%100 == 0 {
				fmt.Printf("processed %d/%d (failed %d)\n", p, len(pending), atomic.LoadInt64(&failed))
			}
		}
	}

	n := opt.Concurrency
	if n < 1 {
		n = 1
	}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go worker()
	}

feed:
	for _, rec := range pending {
		select {
		case jobs <- rec:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("done: processed=%d failed=%d\n", processed, failed)
	if failed > 0 && opt.ErrorLogPath != "" {
		fmt.Printf("see %s for failed ids (safe to re-run the same command to retry them)\n", opt.ErrorLogPath)
	}
	return ctx.Err()
}
