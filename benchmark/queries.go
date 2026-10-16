package main

import (
	"fmt"
	"strings"
	"time"
)

// Query is one time-series workload, written for both engines. Placeholders:
//
//	{table}  table name            {unit}  date_trunc unit (Postgres)
//	{bucket} toStartOf* (ClickHouse)
//	$1,$2,$3 from, to, tenant (Postgres)   {from},{to},{tenant} literals (ClickHouse)
type Query struct {
	ID, Title string
	Tenant    bool
	PG, CH    string
}

var queries = []Query{
	{ID: "requests", Title: "Requests over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t, count(*) FROM {table}
		     WHERE request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		CH: `SELECT {bucket}(request_time) AS t, count() FROM {table}
		     WHERE request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
	{ID: "error_rate", Title: "Error rate (5xx) over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t,
		            100.0 * count(*) FILTER (WHERE status_code >= 500) / count(*)
		     FROM {table} WHERE request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		CH: `SELECT {bucket}(request_time) AS t, 100.0 * countIf(status_code >= 500) / count()
		     FROM {table} WHERE request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
	{ID: "latency_pct", Title: "Latency p50/p95/p99 over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t,
		            percentile_cont(0.5)  WITHIN GROUP (ORDER BY response_time_ms),
		            percentile_cont(0.95) WITHIN GROUP (ORDER BY response_time_ms),
		            percentile_cont(0.99) WITHIN GROUP (ORDER BY response_time_ms)
		     FROM {table} WHERE request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		// quantilesExact matches Postgres' exact (not approximate) percentiles.
		CH: `SELECT {bucket}(request_time) AS t, quantilesExact(0.5, 0.95, 0.99)(response_time_ms)
		     FROM {table} WHERE request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
	{ID: "tenant", Title: "One tenant: requests and avg latency", Tenant: true,
		PG: `SELECT date_trunc('{unit}', request_time) AS t, count(*), avg(response_time_ms) FROM {table}
		     WHERE tenant_id = $3 AND request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		CH: `SELECT {bucket}(request_time) AS t, count(), avg(response_time_ms) FROM {table}
		     WHERE tenant_id = {tenant} AND request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
	{ID: "top_routes", Title: "Requests per route over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t, route_template, count(*) FROM {table}
		     WHERE request_time >= $1 AND request_time < $2 GROUP BY t, route_template ORDER BY t, 3 DESC`,
		CH: `SELECT {bucket}(request_time) AS t, route_template, count() FROM {table}
		     WHERE request_time >= {from} AND request_time < {to} GROUP BY t, route_template ORDER BY t, 3 DESC`},
	{ID: "unique_users", Title: "Unique users over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t, count(DISTINCT user_id) FROM {table}
		     WHERE request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		CH: `SELECT {bucket}(request_time) AS t, uniqExact(user_id) FROM {table}
		     WHERE request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
	{ID: "bytes", Title: "Bytes in/out over time",
		PG: `SELECT date_trunc('{unit}', request_time) AS t, sum(request_size_bytes), sum(response_size_bytes)
		     FROM {table} WHERE request_time >= $1 AND request_time < $2 GROUP BY t ORDER BY t`,
		CH: `SELECT {bucket}(request_time) AS t, sum(request_size_bytes), sum(response_size_bytes)
		     FROM {table} WHERE request_time >= {from} AND request_time < {to} GROUP BY t ORDER BY t`},
}

// Window is a time range plus the bucket size used for it.
type Window struct {
	Name     string
	From, To time.Time
	Unit     string // minute | hour | day
}

var chBucket = map[string]string{"minute": "toStartOfMinute", "hour": "toStartOfHour", "day": "toStartOfDay"}

// windows returns 1h, 1d, 1w and the full span, ending at the newest row; ranges longer than the data are dropped.
func windows(min, max time.Time) []Window {
	to := max.Add(time.Second)
	var ws []Window
	for _, w := range []Window{
		{Name: "1h", From: to.Add(-time.Hour), Unit: "minute"},
		{Name: "1d", From: to.Add(-24 * time.Hour), Unit: "hour"},
		{Name: "1w", From: to.Add(-7 * 24 * time.Hour), Unit: "hour"},
	} {
		if w.From.After(min) {
			w.To = to
			ws = append(ws, w)
		}
	}
	return append(ws, Window{Name: "all", From: min, To: to, Unit: "day"})
}

func chLit(t time.Time) string {
	return fmt.Sprintf("toDateTime64('%s', 6, 'UTC')", t.UTC().Format("2006-01-02 15:04:05.000000"))
}

func (q Query) pgSQL(table string, w Window) string {
	return strings.NewReplacer("{table}", table, "{unit}", w.Unit).Replace(q.PG)
}

func (q Query) chSQL(table string, w Window, tenant int) string {
	return strings.NewReplacer("{table}", table, "{bucket}", chBucket[w.Unit],
		"{from}", chLit(w.From), "{to}", chLit(w.To), "{tenant}", fmt.Sprint(tenant)).Replace(q.CH)
}
