// Command pddikti-crawler crawls pddikti.kemdiktisaintek.go.id (undocumented
// JSON API under /api/..., reverse-engineered from network calls — not a
// published contract). Two phases like the sibling sekolah-crawler tool,
// but no result-window sharding is needed here: total institutions
// (~6,910 as of writing) and the page-size cap (100) never come close to
// any pagination ceiling, so a single list pass covers everything.
//
//	pddikti-crawler list   -out ids.csv
//	pddikti-crawler detail -ids ids.csv -out detail.csv
//	pddikti-crawler test   <id_sp>
//
// The API rejects requests without a matching Referer header (a WAF check,
// not auth) — every request sets one.
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	baseURL     = "https://pddikti.kemdiktisaintek.go.id"
	listPath    = "/api/v2/pt/search/filter"
	refererPage = baseURL + "/perguruan-tinggi"
	maxPageSize = 100
)

// --- HTTP client -----------------------------------------------------------

type client struct{ http *http.Client }

// 90s timeout: the /api/v2/pt/search/filter (listing) endpoint gets
// noticeably and increasingly slow under sustained requests (observed
// 13s -> 19s across a handful of consecutive calls) — looks like
// server-side throttling specific to that endpoint, not a hang. The
// detail endpoints respond fast (<1s) regardless.
func newClient() *client { return &client{http: &http.Client{Timeout: 90 * time.Second}} }

type clientError struct {
	status int
	body   string
}

func (e *clientError) Error() string { return fmt.Sprintf("http %d: %s", e.status, e.body) }

func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; pddikti-crawler/1.0; research/export tool)")
	req.Header.Set("Referer", refererPage)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &clientError{status: resp.StatusCode, body: string(data)}
	}
	return data, nil
}

// withRetry retries transient failures (network errors, 5xx, 429) with
// exponential backoff. A 4xx clientError is permanent — never retried.
func withRetry(ctx context.Context, fn func() error) error {
	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		var ce *clientError
		if errors.As(err, &ce) && ce.status < 500 && ce.status != 429 {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 5 {
			break
		}
		wait := backoff + time.Duration(rand.Int63n(int64(backoff)/2+1))
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	return lastErr
}

// --- rate limiter (interval-spaced, shared across goroutines) --------------

type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(ratePerSec float64) *limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	return &limiter{interval: time.Duration(float64(time.Second) / ratePerSec)}
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	d := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- API types ---------------------------------------------------------

type listItem struct {
	IDSP          string `json:"id_sp"`
	KabKotaPT     string `json:"kab_kota_pt"`
	ProvinsiPT    string `json:"provinsi_pt"`
	Akreditasi    string `json:"akreditasi"`
	NamaSingkat   string `json:"nama_singkat"`
	StatusPT      string `json:"status_pt"`
	NamaPT        string `json:"nama_pt"`
	JenisPT       string `json:"jenis_pt"`
	JumlahProdi   int    `json:"jumlah_prodi"`
	RangeBiayaKul string `json:"range_biaya_kuliah"`
}

type listResponse struct {
	Status string `json:"status"`
	Data   struct {
		Data       []listItem `json:"data"`
		Page       int        `json:"page"`
		Limit      int        `json:"limit"`
		TotalItems int        `json:"totalItems"`
		TotalPages int        `json:"totalPages"`
	} `json:"data"`
}

func (c *client) searchPT(ctx context.Context, page, limit int) (*listResponse, error) {
	q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}
	raw, err := c.get(ctx, listPath+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var out listResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return &out, nil
}

type ptDetail struct {
	Kelompok         string  `json:"kelompok"`
	Pembina          string  `json:"pembina"`
	KodePT           string  `json:"kode_pt"`
	Email            string  `json:"email"`
	NoTel            string  `json:"no_tel"`
	NoFax            string  `json:"no_fax"`
	Website          string  `json:"website"`
	Alamat           string  `json:"alamat"`
	NamaPT           string  `json:"nama_pt"`
	NamaSingkat      string  `json:"nm_singkat"`
	KodePos          string  `json:"kode_pos"`
	ProvinsiPT       string  `json:"provinsi_pt"`
	KabKotaPT        string  `json:"kab_kota_pt"`
	KecamatanPT      string  `json:"kecamatan_pt"`
	LintangPT        float64 `json:"lintang_pt"`
	BujurPT          float64 `json:"bujur_pt"`
	TglBerdiriPT     string  `json:"tgl_berdiri_pt"`
	SKPendirianSP    string  `json:"sk_pendirian_sp"`
	StatusPT         string  `json:"status_pt"`
	AkreditasiPT     string  `json:"akreditasi_pt"`
	StatusAkreditasi string  `json:"status_akreditasi"`
}

type prodiItem struct {
	NamaProdi    string `json:"nama_prodi"`
	JenjangProdi string `json:"jenjang_prodi"`
	Akreditasi   string `json:"akreditasi"`
	StatusProdi  string `json:"status_prodi"`
}

type waktuStudiItem struct {
	Jenjang       string  `json:"jenjang"`
	MeanMasaStudi float64 `json:"mean_masa_studi"`
}

func getJSON[T any](ctx context.Context, c *client, path string) (T, error) {
	var out struct {
		Status string `json:"status"`
		Data   T      `json:"data"`
	}
	var zero T
	raw, err := c.get(ctx, path)
	if err != nil {
		return zero, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("decode json: %w", err)
	}
	return out.Data, nil
}

// fullDetail bundles every sub-endpoint's data for one institution.
type fullDetail struct {
	Detail         ptDetail
	Prodi          []prodiItem
	Rasio          string // often "Data belum tersedia" (not yet available) — kept as-is
	MeanLulus      float64
	MeanBaru       float64
	WaktuStudi     []waktuStudiItem
	GraduationRate float64
	CostRange      string
}

func (c *client) fetchFullDetail(ctx context.Context, id, semester string) (*fullDetail, error) {
	fd := &fullDetail{}
	var err error
	if fd.Detail, err = getJSON[ptDetail](ctx, c, "/api/pt/detail/"+id); err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}

	// Best-effort sub-resources: an institution missing one (common — many
	// have no ratio/cost data yet per the API itself) shouldn't fail the row.
	fd.Prodi, _ = getJSON[[]prodiItem](ctx, c, "/api/pt/prodi/"+id+"/"+semester)

	if r, err := getJSON[struct {
		Rasio string `json:"rasio"`
	}](ctx, c, "/api/pt/rasio/"+id); err == nil {
		fd.Rasio = r.Rasio
	}

	if m, err := getJSON[struct {
		MeanJumlahLulus float64 `json:"mean_jumlah_lulus"`
		MeanJumlahBaru  float64 `json:"mean_jumlah_baru"`
	}](ctx, c, "/api/pt/mahasiswa/"+id); err == nil {
		fd.MeanLulus, fd.MeanBaru = m.MeanJumlahLulus, m.MeanJumlahBaru
	}

	fd.WaktuStudi, _ = getJSON[[]waktuStudiItem](ctx, c, "/api/pt/waktu-studi/"+id)

	if g, err := getJSON[struct {
		GraduationRate float64 `json:"graduation_rate"`
	}](ctx, c, "/api/pt/graduation-rate/"+id); err == nil {
		fd.GraduationRate = g.GraduationRate
	}

	if cr, err := getJSON[struct {
		RangeBiayaKuliah string `json:"range_biaya_kuliah"`
	}](ctx, c, "/api/pt/cost-range/"+id); err == nil {
		fd.CostRange = cr.RangeBiayaKuliah
	}

	return fd, nil
}

// --- CSV schema --------------------------------------------------------

var idsHeader = []string{"id_sp", "nama_pt", "nama_singkat", "jenis_pt", "status_pt", "akreditasi", "provinsi_pt", "kab_kota_pt", "jumlah_prodi", "range_biaya_kuliah"}

func idsRow(it listItem) []string {
	return []string{it.IDSP, it.NamaPT, it.NamaSingkat, it.JenisPT, it.StatusPT, it.Akreditasi, it.ProvinsiPT, it.KabKotaPT, strconv.Itoa(it.JumlahProdi), it.RangeBiayaKul}
}

var detailHeader = []string{
	"id_sp", "kode_pt", "nama_pt", "nama_singkat", "kelompok", "pembina",
	"status_pt", "akreditasi_pt", "status_akreditasi",
	"email", "no_tel", "no_fax", "website", "alamat", "kode_pos",
	"provinsi_pt", "kab_kota_pt", "kecamatan_pt", "lintang_pt", "bujur_pt",
	"tgl_berdiri_pt", "sk_pendirian_sp",
	"jumlah_prodi", "prodi_all",
	"rasio", "mean_jumlah_lulus", "mean_jumlah_baru", "waktu_studi_all",
	"graduation_rate", "range_biaya_kuliah",
}

func flatten(id string, fd *fullDetail) []string {
	d := fd.Detail
	prodiParts := make([]string, 0, len(fd.Prodi))
	for _, p := range fd.Prodi {
		prodiParts = append(prodiParts, fmt.Sprintf("%s (%s)", p.NamaProdi, p.JenjangProdi))
	}
	wsParts := make([]string, 0, len(fd.WaktuStudi))
	for _, w := range fd.WaktuStudi {
		wsParts = append(wsParts, fmt.Sprintf("%s:%.1f", w.Jenjang, w.MeanMasaStudi))
	}
	f := func(v float64) string {
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return []string{
		id, d.KodePT, d.NamaPT, d.NamaSingkat, d.Kelompok, d.Pembina,
		d.StatusPT, d.AkreditasiPT, d.StatusAkreditasi,
		d.Email, d.NoTel, d.NoFax, d.Website, d.Alamat, d.KodePos,
		d.ProvinsiPT, d.KabKotaPT, d.KecamatanPT, f(d.LintangPT), f(d.BujurPT),
		d.TglBerdiriPT, d.SKPendirianSP,
		strconv.Itoa(len(fd.Prodi)), strings.Join(prodiParts, "; "),
		fd.Rasio, f(fd.MeanLulus), f(fd.MeanBaru), strings.Join(wsParts, "; "),
		f(fd.GraduationRate), fd.CostRange,
	}
}

// --- list command --------------------------------------------------------

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func runList(ctx context.Context, c *client, out string, rate float64) error {
	isNew := !fileExists(out)
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if isNew {
		if err := w.Write(idsHeader); err != nil {
			return err
		}
		w.Flush()
	}

	lim := newLimiter(rate)
	page := 1
	written := 0
	for {
		if err := lim.wait(ctx); err != nil {
			return err
		}
		var resp *listResponse
		if err := withRetry(ctx, func() error {
			var e error
			resp, e = c.searchPT(ctx, page, maxPageSize)
			return e
		}); err != nil {
			return fmt.Errorf("page %d: %w", page, err)
		}
		for _, it := range resp.Data.Data {
			if err := w.Write(idsRow(it)); err != nil {
				return err
			}
			written++
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		fmt.Printf("page %d/%d fetched (%d written)\n", page, resp.Data.TotalPages, written)
		if page >= resp.Data.TotalPages || len(resp.Data.Data) == 0 {
			break
		}
		page++
	}
	fmt.Printf("done: %d institutions written to %s\n", written, out)
	return nil
}

// --- detail command --------------------------------------------------------

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

func readIDs(path string) ([]string, error) {
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
	ids := make([]string, 0, len(rows))
	for _, row := range rows[1:] { // skip header
		if len(row) > 0 && row[0] != "" {
			ids = append(ids, row[0])
		}
	}
	return ids, nil
}

func runDetail(ctx context.Context, c *client, idsPath, out, checkpoint, errLog, semester string, concurrency int, rate float64) error {
	ids, err := readIDs(idsPath)
	if err != nil {
		return fmt.Errorf("read ids: %w", err)
	}
	done, err := loadDoneSet(checkpoint)
	if err != nil {
		return err
	}
	pending := make([]string, 0, len(ids))
	for _, id := range ids {
		if !done[id] {
			pending = append(pending, id)
		}
	}
	fmt.Printf("total ids: %d, already done: %d, pending: %d\n", len(ids), len(done), len(pending))
	if len(pending) == 0 {
		return nil
	}

	outIsNew := !fileExists(out)
	outFile, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer outFile.Close()
	csvW := csv.NewWriter(outFile)
	if outIsNew {
		if err := csvW.Write(detailHeader); err != nil {
			return err
		}
		csvW.Flush()
	}

	doneFile, err := os.OpenFile(checkpoint, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer doneFile.Close()

	var errFile *os.File
	if errLog != "" {
		if errFile, err = os.OpenFile(errLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err != nil {
			return err
		}
		defer errFile.Close()
	}

	var mu sync.Mutex
	lim := newLimiter(rate)
	jobs := make(chan string)
	var wg sync.WaitGroup
	var processed, failed int

	worker := func() {
		defer wg.Done()
		for id := range jobs {
			if ctx.Err() != nil {
				return
			}
			var fd *fullDetail
			err := withRetry(ctx, func() error {
				if err := lim.wait(ctx); err != nil {
					return err
				}
				var e error
				fd, e = c.fetchFullDetail(ctx, id, semester)
				return e
			})

			mu.Lock()
			if err != nil {
				if errFile != nil {
					fmt.Fprintf(errFile, "%s\t%v\n", id, err)
				}
				failed++
				mu.Unlock()
				continue
			}
			_ = csvW.Write(flatten(id, fd))
			csvW.Flush()
			fmt.Fprintln(doneFile, id)
			processed++
			p := processed
			mu.Unlock()

			if p%100 == 0 {
				fmt.Printf("processed %d/%d (failed %d)\n", p, len(pending), failed)
			}
		}
	}

	if concurrency < 1 {
		concurrency = 1
	}
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go worker()
	}
feed:
	for _, id := range pending {
		select {
		case jobs <- id:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("done: processed=%d failed=%d\n", processed, failed)
	return ctx.Err()
}

// --- CLI --------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c := newClient()

	switch os.Args[1] {
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		out := fs.String("out", "ids.csv", "output CSV path")
		rate := fs.Float64("rate", 3, "max requests per second")
		fs.Parse(os.Args[2:])
		if err := runList(ctx, c, *out, *rate); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "detail":
		fs := flag.NewFlagSet("detail", flag.ExitOnError)
		ids := fs.String("ids", "ids.csv", "input CSV from 'list'")
		out := fs.String("out", "detail.csv", "output CSV path")
		checkpoint := fs.String("checkpoint", "detail_done.txt", "completed-id log (enables resume)")
		errLog := fs.String("error-log", "detail_errors.tsv", "failed-id log (retried automatically next run)")
		semester := fs.String("semester", "20251", "semester code for the prodi (study program) sub-query")
		concurrency := fs.Int("concurrency", 5, "concurrent workers")
		rate := fs.Float64("rate", 5, "max requests per second (shared across workers; each institution costs ~6 requests)")
		fs.Parse(os.Args[2:])
		if err := runDetail(ctx, c, *ids, *out, *checkpoint, *errLog, *semester, *concurrency, *rate); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "test":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: pddikti-crawler test <id_sp>")
			os.Exit(1)
		}
		fd, err := c.fetchFullDetail(ctx, os.Args[2], "20251")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(fd)

	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`pddikti-crawler - crawl pddikti.kemdiktisaintek.go.id

Usage:
  pddikti-crawler list   [flags]     crawl the search listing into a CSV of institution ids
  pddikti-crawler detail [flags]     fetch full detail for each id, write CSV
  pddikti-crawler test   <id_sp>     fetch one institution's full detail and print JSON

Typical flow:
  go run . list   -out ids.csv
  go run . detail -ids ids.csv -out detail.csv

No sharding needed (unlike sekolah-crawler) — total institutions (~6,910)
never approach any pagination limit. Both commands resume automatically if
interrupted: re-run the identical command.`)
}
