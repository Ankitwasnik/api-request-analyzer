package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sample is one timed execution.
type Sample struct {
	Wall       time.Duration // client-side, including fetching the result
	Server     time.Duration // engine-reported execution time; 0 if unavailable
	ResultRows int
}

type Engine interface {
	Name() string
	Run(ctx context.Context, q Query, w Window, tenant int) (Sample, error)
}

// ---- Postgres ----

type pgEngine struct {
	conn  *pgx.Conn
	table string
}

func newPG(ctx context.Context, dsn, table string) (*pgEngine, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["timezone"] = "UTC"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &pgEngine{conn: conn, table: table}, nil
}

func (p *pgEngine) Name() string { return "postgres" }

func (p *pgEngine) args(q Query, w Window, tenant int) []any {
	a := []any{w.From, w.To}
	if q.Tenant {
		a = append(a, tenant)
	}
	return a
}

func (p *pgEngine) Run(ctx context.Context, q Query, w Window, tenant int) (Sample, error) {
	sql, args := q.pgSQL(p.table, w), p.args(q, w, tenant)
	var s Sample
	start := time.Now()
	rows, err := p.conn.Query(ctx, sql, args...)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		s.ResultRows++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	s.Wall = time.Since(start)

	// Server-side time from a second, equally warm execution.
	var plan []byte
	if err := p.conn.QueryRow(ctx, "EXPLAIN (ANALYZE, TIMING OFF, FORMAT JSON) "+sql, args...).Scan(&plan); err != nil {
		return s, err
	}
	var parsed []struct {
		Exec float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(plan, &parsed); err == nil && len(parsed) > 0 {
		s.Server = time.Duration(parsed[0].Exec * float64(time.Millisecond))
	}
	return s, nil
}

func (p *pgEngine) scalar(ctx context.Context, sql string, args []any, dst ...any) error {
	return p.conn.QueryRow(ctx, sql, args...).Scan(dst...)
}

// ---- ClickHouse (HTTP interface, no driver) ----

type chEngine struct {
	base, user, pass, db, table string
	client                      *http.Client
}

func (c *chEngine) Name() string { return "clickhouse" }

func (c *chEngine) exec(ctx context.Context, sql string) (body []byte, summary map[string]string, wall time.Duration, err error) {
	v := url.Values{"database": {c.db}, "default_format": {"TabSeparated"},
		"wait_end_of_query": {"1"}, "use_query_cache": {"0"}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.base+"/?"+v.Encode(), bytes.NewBufferString(sql))
	req.SetBasicAuth(c.user, c.pass)
	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	wall = time.Since(start)
	if err != nil {
		return nil, nil, 0, err
	}
	if resp.StatusCode != 200 {
		return nil, nil, 0, fmt.Errorf("clickhouse HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	_ = json.Unmarshal([]byte(resp.Header.Get("X-ClickHouse-Summary")), &summary)
	return body, summary, wall, nil
}

func (c *chEngine) Run(ctx context.Context, q Query, w Window, tenant int) (Sample, error) {
	body, sum, wall, err := c.exec(ctx, q.chSQL(c.table, w, tenant))
	if err != nil {
		return Sample{}, err
	}
	s := Sample{Wall: wall, ResultRows: bytes.Count(body, []byte("\n"))}
	if ns, err := strconv.ParseInt(sum["elapsed_ns"], 10, 64); err == nil {
		s.Server = time.Duration(ns)
	}
	return s, nil
}

func (c *chEngine) scalar(ctx context.Context, sql string) (string, error) {
	body, _, _, err := c.exec(ctx, sql)
	return string(bytes.TrimSpace(body)), err
}
