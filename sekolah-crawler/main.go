// Command sekolah-crawler crawls the public school database at
// sekolah.data.kemendikdasmen.go.id via its underlying JSON API
// (reverse-engineered from the Angular app's network calls — there is no
// published/documented API).
//
// Workflow:
//
//  1. sekolah-crawler list   -> crawls the search/listing endpoint into ids.csv
//  2. sekolah-crawler detail -> reads ids.csv, fetches full profile detail
//     for each school, writes sekolah_detail.csv
//
// Both phases are resumable: interrupt with Ctrl+C at any point and re-run
// the same command to continue where it left off.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sekolah-crawler/internal/api"
	"sekolah-crawler/internal/crawler"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client := api.NewClient(30 * time.Second)

	switch os.Args[1] {
	case "list":
		cmdList(ctx, client, os.Args[2:])
	case "split":
		cmdSplit(os.Args[2:])
	case "detail":
		cmdDetail(ctx, client, os.Args[2:])
	case "test":
		cmdTest(ctx, client, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`sekolah-crawler - crawl sekolah.data.kemendikdasmen.go.id

Usage:
  sekolah-crawler list   [flags]        crawl the search listing into a CSV of school ids
  sekolah-crawler split  [flags]        divide an ids.csv into fixed-size (or per-province) parts
  sekolah-crawler detail [flags]        fetch full profile detail for each id, write CSV
  sekolah-crawler test   <sekolah_id>   fetch one school's full detail and print raw JSON

Typical flow (whole country in one run):
  go run . list   -out ids.csv
  go run . detail -ids ids.csv -out sekolah_detail.csv

Typical flow (one equal-sized part at a time — recommended, since schools
are very unevenly spread across provinces so province-sized parts finish
in wildly different times):
  go run . list  -out ids.csv
  go run . split -ids ids.csv -chunk-size 10000 -out-dir parts
  go run . detail -ids parts/part_0001.csv \
      -out detail_part_0001.csv -checkpoint done_part_0001.txt

Run "sekolah-crawler <command> -h" for a command's flags.`)
}

func cmdList(ctx context.Context, client *api.Client, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	keyword := fs.String("keyword", "", "search keyword filter")
	kabupaten := fs.String("kabupaten-kota", "", "filter by kabupaten/kota name")
	bentuk := fs.String("bentuk-pendidikan", "", "filter by education level, e.g. SD, SMP, SMA, SMK, TK, MI, MTs, MA, PKBM")
	status := fs.String("status-sekolah", "", "filter by NEGERI or SWASTA")
	out := fs.String("out", "ids.csv", "output CSV path for the school id listing")
	kabCache := fs.String("kabupaten-cache", "kabupaten_master.txt", "cache file for the discovered kabupaten/kota list (see -h in README: needed to auto-shard queries over 10,000 results)")
	rate := fs.Float64("rate", 3, "max requests per second against the listing endpoint")
	maxRecords := fs.Int("max-records", 0, "stop after this many records (0 = unlimited)")
	restart := fs.Bool("restart", false, "ignore any existing checkpoint/output and start over")
	fs.Parse(args)

	opt := crawler.ListOptions{
		Keyword: *keyword, KabupatenKota: *kabupaten, BentukPendidikan: *bentuk, StatusSekolah: *status,
		OutPath: *out, KabupatenCachePath: *kabCache, RatePerSec: *rate, MaxRecords: *maxRecords, Restart: *restart,
	}
	if err := crawler.RunList(ctx, client, opt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdSplit(args []string) {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	ids := fs.String("ids", "ids.csv", "input CSV produced by the 'list' command (or a previous split's output)")
	chunkSize := fs.Int("chunk-size", 0, "split into fixed-size sequential parts of this many rows each (recommended: parts finish in predictable, roughly equal time, unlike grouping by province)")
	by := fs.String("by", "provinsi", "when -chunk-size is 0: column to group by instead — provinsi, kabupaten, or bentuk_pendidikan")
	outDir := fs.String("out-dir", "parts", "directory to write the split files into")
	fs.Parse(args)

	opt := crawler.SplitOptions{IDsPath: *ids, By: *by, OutDir: *outDir, ChunkSize: *chunkSize}
	if err := crawler.RunSplit(opt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdDetail(ctx context.Context, client *api.Client, args []string) {
	fs := flag.NewFlagSet("detail", flag.ExitOnError)
	ids := fs.String("ids", "ids.csv", "input CSV produced by the 'list' command")
	out := fs.String("out", "sekolah_detail.csv", "output CSV path for full detail records")
	doneLog := fs.String("checkpoint", "detail_done.txt", "file tracking completed sekolah_id (enables resume)")
	errLog := fs.String("error-log", "detail_errors.tsv", "file to log sekolah_id that failed after retries (they are retried on next run)")
	rawJSONL := fs.String("raw-jsonl", "", "optional path to also append the full untouched JSON per school (one object per line)")
	concurrency := fs.Int("concurrency", 5, "number of concurrent workers")
	rate := fs.Float64("rate", 5, "max requests per second against the detail endpoint (shared across all workers)")
	maxRecords := fs.Int("max-records", 0, "stop after this many new records (0 = unlimited)")
	restart := fs.Bool("restart", false, "ignore any existing checkpoint/output and start over")
	fs.Parse(args)

	opt := crawler.DetailOptions{
		IDsPath: *ids, OutPath: *out, DoneLogPath: *doneLog, ErrorLogPath: *errLog,
		RawJSONLPath: *rawJSONL, Concurrency: *concurrency, RatePerSec: *rate,
		MaxRecords: *maxRecords, Restart: *restart,
	}
	if err := crawler.RunDetail(ctx, client, opt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdTest(ctx context.Context, client *api.Client, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: sekolah-crawler test <sekolah_id>")
		os.Exit(1)
	}
	_, raw, err := client.FullDetail(ctx, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(string(raw))
}
