package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DSN         string
	Mode        string // once | continuous
	StartTime   time.Time
	EndTime     time.Time     // once mode only; zero means now
	Rows        int64         // once mode: total rows
	Rate        float64       // continuous mode: rows per simulated second
	Duration    time.Duration // continuous mode: stop after this much simulated time; 0 = forever
	BatchSize   int
	Tenants     int
	Users       int
	Seed        int64
	CreateTable bool
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// parseConfig reads flags; each flag defaults to its environment variable.
func parseConfig(args []string) (*Config, error) {
	c := &Config{}
	var start, end string
	fs := flag.NewFlagSet("raw-data-producer", flag.ContinueOnError)
	fs.StringVar(&c.DSN, "dsn", envOr("PRODUCER_DSN", "postgres://postgres:postgres@localhost:5432/app_db?sslmode=disable"), "Postgres connection string [PRODUCER_DSN]")
	fs.StringVar(&c.Mode, "mode", envOr("PRODUCER_MODE", "once"), "once | continuous [PRODUCER_MODE]")
	fs.StringVar(&start, "start-time", envOr("PRODUCER_START_TIME", ""), "RFC3339 timestamp of the first row; may be in the past; default now [PRODUCER_START_TIME]")
	fs.StringVar(&end, "end-time", envOr("PRODUCER_END_TIME", ""), "once mode: RFC3339 timestamp of the last row; default now [PRODUCER_END_TIME]")
	fs.Int64Var(&c.Rows, "rows", envInt("PRODUCER_ROWS", 100000), "once mode: number of rows, spread evenly between start and end [PRODUCER_ROWS]")
	fs.Float64Var(&c.Rate, "rate", envFloat("PRODUCER_RATE", 100), "continuous mode: rows per simulated second [PRODUCER_RATE]")
	fs.DurationVar(&c.Duration, "duration", envDuration("PRODUCER_DURATION", 0), "continuous mode: stop after this much simulated time (e.g. 24h); 0 = run forever [PRODUCER_DURATION]")
	fs.IntVar(&c.BatchSize, "batch-size", int(envInt("PRODUCER_BATCH_SIZE", 5000)), "rows per COPY batch [PRODUCER_BATCH_SIZE]")
	fs.IntVar(&c.Tenants, "tenants", int(envInt("PRODUCER_TENANTS", 50)), "number of tenants (skewed traffic) [PRODUCER_TENANTS]")
	fs.IntVar(&c.Users, "users", int(envInt("PRODUCER_USERS", 100000)), "number of distinct users, each belonging to one tenant [PRODUCER_USERS]")
	fs.Int64Var(&c.Seed, "seed", envInt("PRODUCER_SEED", 0), "random seed; 0 = random [PRODUCER_SEED]")
	fs.BoolVar(&c.CreateTable, "create-table", envBool("PRODUCER_CREATE_TABLE", true), "create the table if missing [PRODUCER_CREATE_TABLE]")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var err error
	if start == "" {
		c.StartTime = now
	} else if c.StartTime, err = time.Parse(time.RFC3339, start); err != nil {
		return nil, fmt.Errorf("invalid start-time: %w", err)
	}
	if end == "" {
		c.EndTime = now
	} else if c.EndTime, err = time.Parse(time.RFC3339, end); err != nil {
		return nil, fmt.Errorf("invalid end-time: %w", err)
	}

	switch c.Mode {
	case "once":
		if c.Rows <= 0 {
			return nil, fmt.Errorf("rows must be > 0")
		}
		if c.EndTime.Before(c.StartTime) {
			return nil, fmt.Errorf("end-time is before start-time")
		}
	case "continuous":
		if c.Rate <= 0 {
			return nil, fmt.Errorf("rate must be > 0")
		}
	default:
		return nil, fmt.Errorf("mode must be once or continuous, got %q", c.Mode)
	}
	if c.Users < c.Tenants {
		return nil, fmt.Errorf("users (%d) must be >= tenants (%d) so every tenant has a user", c.Users, c.Tenants)
	}
	if c.BatchSize <= 0 || c.Tenants <= 0 || c.Users <= 0 {
		return nil, fmt.Errorf("batch-size, tenants and users must be > 0")
	}
	return c, nil
}
