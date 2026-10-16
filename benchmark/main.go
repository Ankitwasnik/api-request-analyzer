// Command benchmark runs the time-series queries against Postgres and ClickHouse and
// writes timings to results/<label>.json; `report` turns all results into one HTML page.
//
//	go run . run    [-label 2m] [-runs 5]
//	go run . report
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Result struct {
	Query      string    `json:"query"`
	Window     string    `json:"window"`
	Engine     string    `json:"engine"`
	WindowRows int64     `json:"window_rows"`
	ResultRows int       `json:"result_rows"`
	WallMs     []float64 `json:"wall_ms"`
	MedianMs   float64   `json:"median_ms"`
	MinMs      float64   `json:"min_ms"`
	MaxMs      float64   `json:"max_ms"`
	ServerMs   float64   `json:"server_median_ms"` // 0 = unavailable
}

type Run struct {
	Label   string            `json:"label"`
	At      string            `json:"at"`
	Rows    int64             `json:"rows"`
	Runs    int               `json:"runs"`
	Titles  map[string]string `json:"titles"`
	Results []Result          `json:"results"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: benchmark run|report [flags]")
	}
	for _, f := range []string{"../.env", ".env", "../.env.example"} { // earlier files win
		_ = godotenv.Load(f)
	}
	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "report":
		reportCmd(os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	label := fs.String("label", "", "name for this run (default: row count, e.g. 2.0M)")
	runs := fs.Int("runs", 5, "timed repetitions per query, after one warm-up")
	only := fs.String("query", "", "comma-separated query ids (default all)")
	pgTable := fs.String("pg-table", "api_requests", "Postgres table")
	chTable := fs.String("ch-table", "api_requests_bench", "ClickHouse table")
	chURL := fs.String("ch-url", env("CLICKHOUSE_HTTP_URL", "http://localhost:8123"), "ClickHouse HTTP URL")
	tenantF := fs.Int("tenant", 0, "tenant for the per-tenant query (default: lowest id present)")
	out := fs.String("out", "results", "results directory")
	_ = fs.Parse(args)

	ctx := context.Background()
	pg, err := newPG(ctx, env("PRODUCER_DSN", "postgres://postgres:postgres@localhost:5432/app_db?sslmode=disable"), *pgTable)
	check(err)
	ch := &chEngine{base: *chURL, user: env("CLICKHOUSE_USER", "clickhouse_user"), pass: env("CLICKHOUSE_PASSWORD", "password"),
		db: env("CLICKHOUSE_DB", "app_db"), table: *chTable, client: &http.Client{Timeout: 30 * time.Minute}}

	// Data span, row counts and tenant come from Postgres; ClickHouse must hold the same rows.
	var minT, maxT time.Time
	var pgRows int64
	check(pg.scalar(ctx, fmt.Sprintf("SELECT min(request_time), max(request_time), count(*) FROM %s", *pgTable), nil, &minT, &maxT, &pgRows))
	chRowsS, err := ch.scalar(ctx, fmt.Sprintf("SELECT count() FROM %s FINAL", *chTable))
	check(err)
	if chRows, _ := strconv.ParseInt(chRowsS, 10, 64); chRows != pgRows {
		log.Printf("WARNING: row counts differ (postgres %d, clickhouse %d); sync Airbyte and re-run scripts/bench-prep.sh for a fair comparison", pgRows, chRows)
	}
	tenant := *tenantF
	if tenant == 0 {
		check(pg.scalar(ctx, fmt.Sprintf("SELECT min(tenant_id) FROM %s", *pgTable), nil, &tenant))
	}
	if *label == "" {
		*label = fmt.Sprintf("%.3gM", float64(pgRows)/1e6)
	}

	sel := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id != "" {
			sel[id] = true
		}
	}
	run := Run{Label: *label, At: time.Now().UTC().Format(time.RFC3339), Rows: pgRows, Runs: *runs, Titles: map[string]string{}}
	engines := []Engine{pg, ch}

	for _, w := range windows(minT, maxT) {
		var wrows int64
		check(pg.scalar(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE request_time >= $1 AND request_time < $2", *pgTable),
			[]any{w.From, w.To}, &wrows))
		for _, q := range queries {
			if len(sel) > 0 && !sel[q.ID] {
				continue
			}
			run.Titles[q.ID] = q.Title
			var resRows []int
			for _, e := range engines {
				r, err := measure(ctx, e, q, w, tenant, *runs)
				if err != nil {
					log.Fatalf("%s/%s/%s: %v", q.ID, w.Name, e.Name(), err)
				}
				r.WindowRows = wrows
				run.Results = append(run.Results, r)
				resRows = append(resRows, r.ResultRows)
				fmt.Printf("%-13s %-4s %-10s median %9.1f ms  (server %9.1f ms)  rows in window %d\n",
					q.ID, w.Name, e.Name(), r.MedianMs, r.ServerMs, wrows)
			}
			if resRows[0] != resRows[1] {
				log.Printf("WARNING: %s/%s returned %d rows on postgres but %d on clickhouse", q.ID, w.Name, resRows[0], resRows[1])
			}
		}
	}

	check(os.MkdirAll(*out, 0o755))
	path := filepath.Join(*out, *label+".json")
	data, _ := json.MarshalIndent(run, "", "  ")
	check(os.WriteFile(path, data, 0o644))
	fmt.Println("wrote", path)
}

// measure does one untimed warm-up, then n timed runs.
func measure(ctx context.Context, e Engine, q Query, w Window, tenant, n int) (Result, error) {
	if _, err := e.Run(ctx, q, w, tenant); err != nil {
		return Result{}, err
	}
	r := Result{Query: q.ID, Window: w.Name, Engine: e.Name()}
	var server []float64
	for i := 0; i < n; i++ {
		s, err := e.Run(ctx, q, w, tenant)
		if err != nil {
			return r, err
		}
		r.ResultRows = s.ResultRows
		r.WallMs = append(r.WallMs, ms(s.Wall))
		if s.Server > 0 {
			server = append(server, ms(s.Server))
		}
	}
	r.MedianMs, r.MinMs, r.MaxMs = median(r.WallMs), minOf(r.WallMs), maxOf(r.WallMs)
	if len(server) > 0 {
		r.ServerMs = median(server)
	}
	return r, nil
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func minOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = min(m, x)
	}
	return m
}
func maxOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

//go:embed report.tmpl.html
var reportHTML string

func reportCmd(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("dir", "results", "results directory")
	out := fs.String("out", "report.html", "output file")
	_ = fs.Parse(args)

	files, _ := filepath.Glob(filepath.Join(*dir, "*.json"))
	var all []json.RawMessage
	for _, f := range files {
		b, err := os.ReadFile(f)
		check(err)
		all = append(all, b)
	}
	if len(all) == 0 {
		log.Fatalf("no results in %s; run `go run . run` first", *dir)
	}
	data, _ := json.Marshal(all)
	html := strings.Replace(reportHTML, "/*DATA*/[]", string(data), 1)
	check(os.WriteFile(*out, []byte(html), 0o644))
	fmt.Println("wrote", *out)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}
