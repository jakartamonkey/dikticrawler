package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Institution represents a school/university record in institutions table.
type Institution struct {
	ID          int
	Name        string
	NPSN        string
	Kabupaten   string
	Kecamatan   string
	Kelurahan   string
	YayasanID   int
	YayasanNPYP string
	Tokens      map[string]struct{}
}

// Yayasan represents a foundation record in yayasans table.
type Yayasan struct {
	ID        int
	Name      string
	NPYP      string
	Kabupaten string
	Kecamatan string
	Kelurahan string
	Tokens    map[string]struct{}
}

// Database holds in-memory records, inverted indexes, and IDF scores for fast fuzzy search.
type Database struct {
	Institutions []Institution
	Yayasans     []Yayasan
	InstIndex    map[string][]int
	YayIndex     map[string][]int
	InstIDF      map[string]float64
	YayIDF       map[string]float64
}

var (
	romanMap = map[string]string{
		"I": "1", "II": "2", "III": "3", "IV": "4", "V": "5",
		"VI": "6", "VII": "7", "VIII": "8", "IX": "9",
		"XI": "11", "XII": "12", "XIII": "13", "XIV": "14", "XV": "15",
		"XVI": "16", "XVII": "17", "XVIII": "18", "XIX": "19", "XX": "20",
		"XXI": "21", "XXII": "22", "XXIII": "23", "XXIV": "24", "XXV": "25",
	}

	schoolLevelSynonyms = map[string]string{
		"SMKS": "SMK", "SMKN": "SMK",
		"SMAS": "SMA", "SMAN": "SMA", "SMAK": "SMA",
		"SMPS": "SMP", "SMPN": "SMP", "SMPK": "SMP",
		"SDN": "SD", "SDS": "SD", "SDK": "SD",
		"TKS": "TK", "TKN": "TK",
		"MTSS": "MTS", "MTSN": "MTS",
		"MAS": "MA", "MAN": "MA",
		"MIS": "MI", "MIN": "MI",
		"SLBS": "SLB", "SLBN": "SLB",
	}

	yayasanSynonyms = map[string]string{
		"YAY": "YAYASAN", "YAS": "YAYASAN",
	}

	stopWords = map[string]struct{}{
		"KAB": {}, "KABUPATEN": {}, "KOTA": {}, "ADM": {},
		"PROV": {}, "PROVINSI": {}, "INDONESIA": {}, "NEGERI": {}, "SWASTA": {},
	}

	genericKeywords = map[string]struct{}{
		"YAYASAN": {}, "YAY": {}, "YAS": {}, "SEKOLAH": {}, "PENDIDIKAN": {},
		"LEMBAGA": {}, "PERGURUAN": {}, "CLIENT": {}, "REJECT": {}, "LOW": {}, "DONE": {},
	}

	cleanRegex = regexp.MustCompile(`[^A-Z0-9\s]`)
	multiSpace = regexp.MustCompile(`\s+`)
)

func cleanText(s string) string {
	s = strings.ToUpper(s)
	// Remove unicode zero-width spaces, BOM, etc.
	s = strings.ReplaceAll(s, "\ufeff", "")
	s = strings.ReplaceAll(s, "\u2060", "")
	s = strings.ReplaceAll(s, "\u200b", "")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "`", "'")

	s = cleanRegex.ReplaceAllString(s, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func normToken(w string) string {
	if num, ok := romanMap[w]; ok {
		return num
	}
	if syn, ok := schoolLevelSynonyms[w]; ok {
		return syn
	}
	if syn, ok := yayasanSynonyms[w]; ok {
		return syn
	}
	// Strip leading zeros for numbers (e.g., "01" -> "1")
	if len(w) > 1 && w[0] == '0' {
		if n, err := strconv.Atoi(w); err == nil {
			return strconv.Itoa(n)
		}
	}
	return w
}

func extractTokens(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	words := strings.Fields(cleanText(text))
	for _, w := range words {
		if _, isStop := stopWords[w]; isStop {
			continue
		}
		tokens[w] = struct{}{}
		nw := normToken(w)
		if nw != w {
			tokens[nw] = struct{}{}
		}
	}
	return tokens
}

func getSchoolLevel(tokens map[string]struct{}) string {
	for _, lvl := range []string{"SMA", "SMK", "SMP", "SD", "TK", "KB", "MI", "MTS", "MA", "SLB"} {
		if _, ok := tokens[lvl]; ok {
			return lvl
		}
	}
	return ""
}

func getNumbers(tokens map[string]struct{}) map[string]struct{} {
	nums := make(map[string]struct{})
	for t := range tokens {
		if _, err := strconv.Atoi(t); err == nil {
			nums[t] = struct{}{}
		}
	}
	return nums
}

func loadDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&mode=ro", dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	log.Println("Loading institutions from database...")
	instQuery := `
		SELECT 
			i.id, 
			i.name, 
			COALESCE(i.npsn, ''), 
			COALESCE(i.kabupaten, ''), 
			COALESCE(i.kecamatan, ''), 
			COALESCE(i.kelurahan, ''), 
			COALESCE(i.yayasan_id, 0), 
			COALESCE(y.npyp, '')
		FROM institutions i
		LEFT JOIN yayasans y ON i.yayasan_id = y.id
	`
	instRows, err := db.Query(instQuery)
	if err != nil {
		return nil, fmt.Errorf("query institutions failed: %w", err)
	}
	defer instRows.Close()

	institutions := make([]Institution, 0, 560000)
	instIndex := make(map[string][]int)

	idx := 0
	for instRows.Next() {
		var inst Institution
		if err := instRows.Scan(
			&inst.ID, &inst.Name, &inst.NPSN,
			&inst.Kabupaten, &inst.Kecamatan, &inst.Kelurahan,
			&inst.YayasanID, &inst.YayasanNPYP,
		); err != nil {
			return nil, fmt.Errorf("scan institution failed: %w", err)
		}

		fullText := fmt.Sprintf("%s %s %s %s", inst.Name, inst.Kabupaten, inst.Kecamatan, inst.Kelurahan)
		inst.Tokens = extractTokens(fullText)

		for t := range inst.Tokens {
			instIndex[t] = append(instIndex[t], idx)
		}

		institutions = append(institutions, inst)
		idx++
	}

	instIDF := make(map[string]float64, len(instIndex))
	totalInst := float64(len(institutions))
	for t, postings := range instIndex {
		instIDF[t] = math.Log(1.0 + totalInst/float64(len(postings)))
	}

	log.Printf("Loaded %d institutions and built IDF index.\n", len(institutions))

	log.Println("Loading yayasans from database...")
	yayQuery := `
		SELECT 
			id, 
			name, 
			COALESCE(npyp, ''), 
			COALESCE(kabupaten, ''), 
			COALESCE(kecamatan, ''), 
			COALESCE(kelurahan, '')
		FROM yayasans
	`
	yayRows, err := db.Query(yayQuery)
	if err != nil {
		return nil, fmt.Errorf("query yayasans failed: %w", err)
	}
	defer yayRows.Close()

	yayasans := make([]Yayasan, 0, 160000)
	yayIndex := make(map[string][]int)

	yIdx := 0
	for yayRows.Next() {
		var yay Yayasan
		if err := yayRows.Scan(
			&yay.ID, &yay.Name, &yay.NPYP,
			&yay.Kabupaten, &yay.Kecamatan, &yay.Kelurahan,
		); err != nil {
			return nil, fmt.Errorf("scan yayasan failed: %w", err)
		}

		fullText := fmt.Sprintf("%s %s %s %s", yay.Name, yay.Kabupaten, yay.Kecamatan, yay.Kelurahan)
		yay.Tokens = extractTokens(fullText)

		for t := range yay.Tokens {
			yayIndex[t] = append(yayIndex[t], yIdx)
		}

		yayasans = append(yayasans, yay)
		yIdx++
	}

	yayIDF := make(map[string]float64, len(yayIndex))
	totalYay := float64(len(yayasans))
	for t, postings := range yayIndex {
		yayIDF[t] = math.Log(1.0 + totalYay/float64(len(postings)))
	}

	log.Printf("Loaded %d yayasans and built IDF index.\n", len(yayasans))

	return &Database{
		Institutions: institutions,
		Yayasans:     yayasans,
		InstIndex:    instIndex,
		YayIndex:     yayIndex,
		InstIDF:      instIDF,
		YayIDF:       yayIDF,
	}, nil
}

// levenshteinRatio calculates similarity ratio between 0.0 and 1.0.
func levenshteinRatio(s1, s2 string) float64 {
	r1, r2 := []rune(s1), []rune(s2)
	l1, l2 := len(r1), len(r2)
	if l1 == 0 && l2 == 0 {
		return 1.0
	}
	if l1 == 0 || l2 == 0 {
		return 0.0
	}

	prev := make([]int, l2+1)
	curr := make([]int, l2+1)

	for j := 0; j <= l2; j++ {
		prev[j] = j
	}

	for i := 1; i <= l1; i++ {
		curr[0] = i
		for j := 1; j <= l2; j++ {
			cost := 0
			if r1[i-1] != r2[j-1] {
				cost = 1
			}
			curr[j] = min(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		copy(prev, curr)
	}

	dist := curr[l2]
	maxLen := max(l1, l2)
	return 1.0 - float64(dist)/float64(maxLen)
}

func min(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= a && b <= c {
		return b
	}
	return c
}

func max(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

type MatchResult struct {
	NPSP        string
	NPYP        string
	InstName    string
	YayName     string
	InstKab     string
	YayKab      string
	InstScore   float64
	YayScore    float64
	MatchedType string
}

func scoreCandidate(
	queryTokens map[string]struct{},
	qLevel string,
	qNums map[string]struct{},
	qCity string,
	cleanQuery string,
	candName string,
	candKab string,
	candTokens map[string]struct{},
	idfMap map[string]float64,
) float64 {
	// Weighted overlap by IDF
	score := 0.0
	for t := range queryTokens {
		if _, ok := candTokens[t]; ok {
			idf := idfMap[t]
			if idf == 0 {
				idf = 2.0
			}
			score += idf * 6.0
			if idf >= 6.0 {
				score += 15.0 // Bonus for matching rare distinctive token
			}
		}
	}

	// Level match / penalty
	cLevel := getSchoolLevel(candTokens)
	if qLevel != "" {
		if cLevel == qLevel {
			score += 25.0
		} else if cLevel != "" && cLevel != qLevel {
			score -= 35.0 // Strong penalty for mismatched school level
		}
	}

	// Number match / penalty
	cNums := getNumbers(candTokens)
	if len(qNums) > 0 {
		exactNumMatch := true
		for n := range qNums {
			if _, ok := cNums[n]; !ok {
				exactNumMatch = false
				break
			}
		}
		if exactNumMatch && len(qNums) == len(cNums) {
			score += 30.0
		} else if exactNumMatch {
			score += 15.0
		} else if len(cNums) > 0 {
			score -= 30.0 // Strong penalty for mismatched school numbers
		}
	}

	// City / Kabupaten match & penalty
	cleanKab := cleanText(candKab)
	cleanName := cleanText(candName)
	if qCity != "" {
		if strings.Contains(cleanKab, qCity) || strings.Contains(cleanName, qCity) {
			score += 35.0
		} else if strings.Contains(qCity, "JAKARTA") && (strings.Contains(cleanKab, "JAKARTA") || strings.Contains(cleanName, "JAKARTA")) {
			score += 25.0 // Both in Jakarta metro area
		} else if cleanKab != "" {
			score -= 25.0 // Penalty for location mismatch
		}
	}

	// Penalty for candidate having distinctive words not in query
	for t := range extractTokens(candName) {
		if _, ok := queryTokens[t]; !ok {
			if idf := idfMap[t]; idf >= 6.5 {
				score -= 15.0 // Extraneous distinctive word (e.g. "RAKYAT")
			}
		}
	}

	// Substring / edit distance bonus
	candCombined := cleanName
	if cleanKab != "" {
		candCombined = cleanName + " " + cleanKab
	}
	if strings.Contains(cleanQuery, cleanName) || strings.Contains(cleanName, cleanQuery) {
		score += 15.0
	}
	r1 := levenshteinRatio(cleanQuery, cleanName)
	r2 := levenshteinRatio(cleanQuery, candCombined)
	maxR := r1
	if r2 > maxR {
		maxR = r2
	}
	score += maxR * 35.0

	return score
}

func (db *Database) Search(summary, city string) MatchResult {
	cleanSum := cleanText(summary)
	cleanCit := cleanText(city)
	if cleanSum == "" {
		return MatchResult{}
	}

	qTokens := extractTokens(summary)

	// Check if query has any distinctive non-generic tokens
	hasDistinct := false
	for t := range qTokens {
		if _, isGeneric := genericKeywords[t]; !isGeneric {
			if _, err := strconv.Atoi(t); err != nil {
				hasDistinct = true
				break
			}
		}
	}
	if !hasDistinct {
		// Purely generic string like "Yayasan ", cannot match meaningfully
		return MatchResult{}
	}

	if cleanCit != "" {
		for t := range extractTokens(cleanCit) {
			qTokens[t] = struct{}{}
		}
	}

	qLevel := getSchoolLevel(qTokens)
	qNums := getNumbers(qTokens)

	isYayQuery := false
	lowerSum := strings.ToLower(summary)
	for _, kw := range []string{"yayasan", "yapelin", "perguruan", "pembina", "penyelenggara", "dikdasmen", "pimpinan wilayah", "pondok pesantren"} {
		if strings.Contains(lowerSum, kw) {
			isYayQuery = true
			break
		}
	}

	// 1. Search Institutions using IDF-weighted ranking
	type candidateHit struct {
		idx   int
		score float64
	}
	instHits := make(map[int]float64)
	for t := range qTokens {
		idf := db.InstIDF[t]
		for _, idx := range db.InstIndex[t] {
			instHits[idx] += idf
		}
	}

	instCands := make([]candidateHit, 0, len(instHits))
	for idx, sc := range instHits {
		instCands = append(instCands, candidateHit{idx, sc})
	}
	sort.Slice(instCands, func(i, j int) bool {
		return instCands[i].score > instCands[j].score
	})

	var bestInst Institution
	bestInstScore := -999.0
	limitInst := 120
	if len(instCands) < limitInst {
		limitInst = len(instCands)
	}
	for i := 0; i < limitInst; i++ {
		idx := instCands[i].idx
		inst := db.Institutions[idx]
		sc := scoreCandidate(qTokens, qLevel, qNums, cleanCit, cleanSum, inst.Name, inst.Kabupaten, inst.Tokens, db.InstIDF)
		if sc > bestInstScore {
			bestInstScore = sc
			bestInst = inst
		}
	}

	// 2. Search Yayasans using IDF-weighted ranking
	yayHits := make(map[int]float64)
	for t := range qTokens {
		idf := db.YayIDF[t]
		for _, idx := range db.YayIndex[t] {
			yayHits[idx] += idf
		}
	}

	yayCands := make([]candidateHit, 0, len(yayHits))
	for idx, sc := range yayHits {
		yayCands = append(yayCands, candidateHit{idx, sc})
	}
	sort.Slice(yayCands, func(i, j int) bool {
		return yayCands[i].score > yayCands[j].score
	})

	var bestYay Yayasan
	bestYayScore := -999.0
	limitYay := 120
	if len(yayCands) < limitYay {
		limitYay = len(yayCands)
	}
	for i := 0; i < limitYay; i++ {
		idx := yayCands[i].idx
		yay := db.Yayasans[idx]
		sc := scoreCandidate(qTokens, "", qNums, cleanCit, cleanSum, yay.Name, yay.Kabupaten, yay.Tokens, db.YayIDF)
		if sc > bestYayScore {
			bestYayScore = sc
			bestYay = yay
		}
	}

	res := MatchResult{
		InstScore: bestInstScore,
		YayScore:  bestYayScore,
	}

	const threshold = 25.0

	if isYayQuery {
		if bestYayScore >= threshold {
			res.NPYP = bestYay.NPYP
			res.YayName = bestYay.Name
			res.YayKab = bestYay.Kabupaten
			res.MatchedType = "yayasan"
		} else if bestInstScore >= threshold {
			res.NPSP = bestInst.NPSN
			res.NPYP = bestInst.YayasanNPYP
			res.InstName = bestInst.Name
			res.InstKab = bestInst.Kabupaten
			res.MatchedType = "institution"
		}
	} else {
		if bestInstScore >= threshold {
			res.NPSP = bestInst.NPSN
			res.NPYP = bestInst.YayasanNPYP
			res.InstName = bestInst.Name
			res.InstKab = bestInst.Kabupaten
			res.MatchedType = "institution"
		} else if bestYayScore >= threshold {
			res.NPYP = bestYay.NPYP
			res.YayName = bestYay.Name
			res.YayKab = bestYay.Kabupaten
			res.MatchedType = "yayasan"
		}
	}

	return res
}

func main() {
	csvPath := flag.String("csv", "Jira (6).csv", "Path to the input CSV file")
	dbPath := flag.String("db", "crm.db", "Path to SQLite crm.db database")
	outPath := flag.String("out", "", "Path to output CSV file (empty to update input CSV in-place)")
	dryRun := flag.Bool("dry-run", false, "Perform matching and print statistics without modifying CSV")
	verbose := flag.Bool("v", false, "Print detailed match information for each row")
	flag.Parse()

	startTime := time.Now()

	// 1. Read input CSV
	csvBytes, err := os.ReadFile(*csvPath)
	if err != nil {
		log.Fatalf("Error reading CSV file %s: %v", *csvPath, err)
	}

	hasBOM := bytes.HasPrefix(csvBytes, []byte("\xef\xbb\xbf"))
	cleanReaderBytes := csvBytes
	if hasBOM {
		cleanReaderBytes = bytes.TrimPrefix(csvBytes, []byte("\xef\xbb\xbf"))
	}

	reader := csv.NewReader(bytes.NewReader(cleanReaderBytes))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		log.Fatalf("Error parsing CSV: %v", err)
	}

	if len(rows) == 0 {
		log.Fatalf("CSV file is empty")
	}

	header := rows[0]
	summaryColIdx := -1
	cityColIdx := -1
	npypColIdx := -1
	npspColIdx := -1

	for i, col := range header {
		cleanCol := strings.TrimSpace(strings.ToLower(col))
		if summaryColIdx == -1 && (cleanCol == "summary" || strings.Contains(cleanCol, "summary")) {
			summaryColIdx = i
		}
		if cityColIdx == -1 && (cleanCol == "city" || strings.Contains(cleanCol, "city")) {
			cityColIdx = i
		}
		if cleanCol == "npyp" {
			npypColIdx = i
		}
		if cleanCol == "npsp" || cleanCol == "npsn" {
			npspColIdx = i
		}
	}

	if summaryColIdx == -1 {
		summaryColIdx = 0 // Default to first column
	}

	// If NPYP or NPSP columns don't exist in header, append them
	addNewNPYP := false
	addNewNPSP := false

	if npypColIdx == -1 {
		header = append(header, "NPYP")
		npypColIdx = len(header) - 1
		addNewNPYP = true
	}
	if npspColIdx == -1 {
		header = append(header, "NPSP")
		npspColIdx = len(header) - 1
		addNewNPSP = true
	}

	rows[0] = header

	// 2. Load database
	db, err := loadDatabase(*dbPath)
	if err != nil {
		log.Fatalf("Database initialization error: %v", err)
	}

	// 3. Process each row
	matchedNPSPCount := 0
	matchedNPYPCount := 0
	matchedBothCount := 0
	unmatchedCount := 0

	totalDataRows := len(rows) - 1
	log.Printf("Processing %d CSV rows...\n", totalDataRows)

	for i := 1; i < len(rows); i++ {
		row := rows[i]

		// Ensure row has enough columns if we added new ones
		for len(row) < len(header) {
			row = append(row, "")
		}

		summary := ""
		if summaryColIdx < len(row) {
			summary = strings.TrimSpace(row[summaryColIdx])
		}

		city := ""
		if cityColIdx != -1 && cityColIdx < len(row) {
			city = strings.TrimSpace(row[cityColIdx])
		}

		match := db.Search(summary, city)

		if match.NPSP != "" {
			row[npspColIdx] = match.NPSP
		}
		if match.NPYP != "" {
			row[npypColIdx] = match.NPYP
		}

		hasNPSP := row[npspColIdx] != ""
		hasNPYP := row[npypColIdx] != ""

		if hasNPSP && hasNPYP {
			matchedBothCount++
		} else if hasNPSP {
			matchedNPSPCount++
		} else if hasNPYP {
			matchedNPYPCount++
		} else {
			unmatchedCount++
		}

		if *verbose {
			fmt.Printf("Row %3d: %-45s | NPSP: %-8s | NPYP: %-8s | Type: %s (InstSc: %.1f, YaySc: %.1f)\n",
				i, summary, row[npspColIdx], row[npypColIdx], match.MatchedType, match.InstScore, match.YayScore)
		}

		rows[i] = row
	}

	totalMatched := matchedNPSPCount + matchedNPYPCount + matchedBothCount
	fmt.Printf("\n--- Processing Summary ---\n")
	fmt.Printf("Total Rows Processed: %d\n", totalDataRows)
	fmt.Printf("Rows with NPSP only : %d\n", matchedNPSPCount)
	fmt.Printf("Rows with NPYP only : %d\n", matchedNPYPCount)
	fmt.Printf("Rows with Both      : %d\n", matchedBothCount)
	fmt.Printf("Total Matched       : %d (%.1f%%)\n", totalMatched, float64(totalMatched)/float64(totalDataRows)*100.0)
	fmt.Printf("Unmatched Rows      : %d\n", unmatchedCount)
	fmt.Printf("Execution Time      : %v\n", time.Since(startTime))

	if *dryRun {
		fmt.Println("\nDry run mode enabled. No changes written to CSV.")
		return
	}

	// 4. Write back to CSV
	targetOut := *outPath
	if targetOut == "" {
		targetOut = *csvPath
	}

	var buf bytes.Buffer
	if hasBOM {
		buf.Write([]byte("\xef\xbb\xbf"))
	}

	writer := csv.NewWriter(&buf)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			log.Fatalf("Error writing row to CSV buffer: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		log.Fatalf("Error flushing CSV writer: %v", err)
	}

	// Write atomically using temporary file
	tmpFile := targetOut + ".tmp"
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		log.Fatalf("Failed to write temporary output file: %v", err)
	}

	if err := os.Rename(tmpFile, targetOut); err != nil {
		log.Fatalf("Failed to rename temporary output file to %s: %v", targetOut, err)
	}

	fmt.Printf("\nSuccessfully updated CSV: %s (NPYP added: %t, NPSP added: %t)\n", targetOut, addNewNPYP, addNewNPSP)
}
