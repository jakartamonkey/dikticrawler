package crawler

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SplitOptions struct {
	IDsPath   string
	By        string // column name from ids.csv's header, e.g. "provinsi", "kabupaten", "bentuk_pendidikan" — ignored when ChunkSize > 0
	OutDir    string
	ChunkSize int // if > 0, ignore By and split into fixed-size sequential parts instead
}

func columnIndex(header []string, name string) (int, error) {
	for i, h := range header {
		if h == name {
			return i, nil
		}
	}
	return -1, fmt.Errorf("column %q not found in header %v", name, header)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

// RunSplit reads an ids.csv produced by RunList and divides it into parts
// under OutDir for "detail" to process one at a time. Two modes:
//
//   - ChunkSize > 0: fixed-size sequential parts (part_0001.csv, ...), each
//     with (up to) ChunkSize rows regardless of any real-world grouping.
//     This is the recommended default — Indonesia's schools are extremely
//     unevenly distributed across provinces/kabupaten (Java alone holds a
//     large share), so grouping by province makes some parts finish in
//     minutes and others take many hours. Fixed-size chunks make every
//     part take roughly the same, predictable amount of time.
//   - ChunkSize == 0: group by the By column (e.g. "provinsi", "kabupaten",
//     "bentuk_pendidikan") — useful when you want parts that mean something
//     geographically/categorically rather than just evenly sized, e.g. to
//     report progress per region. Combine the two by splitting a single
//     oversized province's file again with ChunkSize.
func RunSplit(opt SplitOptions) error {
	f, err := os.Open(opt.IDsPath)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return err
	}
	if len(rows) <= 1 {
		return fmt.Errorf("no data rows in %s", opt.IDsPath)
	}
	header := rows[0]

	if err := os.MkdirAll(opt.OutDir, 0755); err != nil {
		return err
	}

	if opt.ChunkSize > 0 {
		return runChunkSplit(header, rows[1:], opt)
	}

	idx, err := columnIndex(header, opt.By)
	if err != nil {
		return err
	}

	type group struct {
		file  *os.File
		w     *csv.Writer
		name  string
		count int
	}
	groups := map[string]*group{}

	for _, row := range rows[1:] {
		if len(row) <= idx {
			continue
		}
		val := row[idx]
		key := slugify(val)
		if key == "" {
			key = "unknown"
		}
		g, ok := groups[key]
		if !ok {
			path := filepath.Join(opt.OutDir, key+".csv")
			file, err := os.Create(path)
			if err != nil {
				return err
			}
			w := csv.NewWriter(file)
			if err := w.Write(header); err != nil {
				return err
			}
			g = &group{file: file, w: w, name: val}
			groups[key] = g
		}
		if err := g.w.Write(row); err != nil {
			return err
		}
		g.count++
	}

	keys := make([]string, 0, len(groups))
	for k, g := range groups {
		g.w.Flush()
		g.file.Close()
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("split %d rows by %q into %d files under %s/:\n", len(rows)-1, opt.By, len(groups), opt.OutDir)
	for _, k := range keys {
		g := groups[k]
		fmt.Printf("  %-45s %-30s %d rows\n", g.name, k+".csv", g.count)
	}
	return nil
}

func runChunkSplit(header []string, dataRows [][]string, opt SplitOptions) error {
	total := len(dataRows)
	numChunks := (total + opt.ChunkSize - 1) / opt.ChunkSize
	width := len(strconv.Itoa(numChunks))
	if width < 4 {
		width = 4
	}

	fmt.Printf("splitting %d rows into %d parts of up to %d rows each, under %s/:\n", total, numChunks, opt.ChunkSize, opt.OutDir)

	for i := 0; i < numChunks; i++ {
		start := i * opt.ChunkSize
		end := start + opt.ChunkSize
		if end > total {
			end = total
		}

		name := fmt.Sprintf("part_%0*d.csv", width, i+1)
		path := filepath.Join(opt.OutDir, name)
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		w := csv.NewWriter(file)
		if err := w.Write(header); err != nil {
			file.Close()
			return err
		}
		for _, row := range dataRows[start:end] {
			if err := w.Write(row); err != nil {
				file.Close()
				return err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			file.Close()
			return err
		}
		file.Close()

		fmt.Printf("  %-20s %d rows\n", name, end-start)
	}
	return nil
}
