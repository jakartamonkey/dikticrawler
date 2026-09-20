package crawler

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"

	"sekolah-crawler/internal/api"
	"sekolah-crawler/internal/ratelimit"
)

// DiscoverKabupaten harvests the set of exact kabupaten/kota strings this
// API actually uses (e.g. "Kab. Bogor", "Kota Adm. Jakarta Timur"). There is
// no endpoint that returns the complete list directly — the autocomplete
// endpoint (referensi/wilayah) is capped to a handful of suggestions per
// query, unsuitable for exhaustive enumeration. Instead this pages through
// every bentuk_pendidikan category (capped at the first 10,000 rows each,
// which is all the API allows anyway) and collects the "kabupaten" value
// seen on every row. Since almost every category (especially TK/SD/KB)
// exists in nearly every kabupaten nationwide, the union across all
// categories reliably captures the full ~514-entry set in practice; RunList
// verifies this afterward by comparing shard totals against the true grand
// total and reports any gap rather than assuming silent completeness.
func DiscoverKabupaten(ctx context.Context, client *api.Client, ratePerSec float64) ([]string, error) {
	cats, err := client.ListBentukPendidikan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list bentuk_pendidikan: %w", err)
	}

	limiter := ratelimit.New(ratePerSec)
	seen := map[string]bool{}

	scan := func(bentuk string) error {
		page := 0
		for {
			if err := limiter.Wait(ctx); err != nil {
				return err
			}
			var resp *api.CariSekolahResponse
			err := withRetry(ctx, 5, func() error {
				var e error
				resp, e = client.SearchSchools(ctx, api.CariSekolahRequest{
					Page: page, Size: api.MaxPageSize, BentukPendidikan: bentuk,
				})
				return e
			})
			if err != nil {
				return err
			}
			for _, item := range resp.Data {
				if item.Kabupaten != "" {
					seen[item.Kabupaten] = true
				}
			}
			page++
			nextOffset := page*api.MaxPageSize + api.MaxPageSize
			if len(resp.Data) < api.MaxPageSize || nextOffset > api.MaxResultWindow {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}

	fmt.Printf("discovering kabupaten/kota list from %d education-level categories...\n", len(cats))
	if err := scan(""); err != nil { // unfiltered pass too, cheap and harmless
		return nil, err
	}
	for _, c := range cats {
		if err := scan(c.Nama); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", c.Nama, err)
		}
	}

	list := make([]string, 0, len(seen))
	for k := range seen {
		list = append(list, k)
	}
	sort.Strings(list)
	fmt.Printf("discovered %d distinct kabupaten/kota values\n", len(list))
	return list, nil
}

func loadKabupatenCache(path string) ([]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var list []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v := sc.Text(); v != "" {
			list = append(list, v)
		}
	}
	return list, sc.Err()
}

func saveKabupatenCache(path string, list []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, v := range list {
		fmt.Fprintln(w, v)
	}
	return w.Flush()
}

// LoadOrDiscoverKabupaten returns the cached list at cachePath if present,
// otherwise runs DiscoverKabupaten and saves the result for next time.
func LoadOrDiscoverKabupaten(ctx context.Context, client *api.Client, cachePath string, ratePerSec float64) ([]string, error) {
	if cached, err := loadKabupatenCache(cachePath); err != nil {
		return nil, err
	} else if len(cached) > 0 {
		fmt.Printf("using cached kabupaten/kota list (%d entries) from %s\n", len(cached), cachePath)
		return cached, nil
	}

	list, err := DiscoverKabupaten(ctx, client, ratePerSec)
	if err != nil {
		return nil, err
	}
	if err := saveKabupatenCache(cachePath, list); err != nil {
		return nil, fmt.Errorf("save kabupaten cache: %w", err)
	}
	return list, nil
}
