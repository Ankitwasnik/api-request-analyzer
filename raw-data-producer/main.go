package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/ankitw/api-request-analyzer/raw-data-producer/internal/db"
)

//go:embed schema.sql
var schemaSQL string

func main() {
	// Real env vars win; .env is optional (repo root or current dir).
	for _, f := range []string{".env", "../.env"} {
		_ = godotenv.Load(f) // Load aborts on the first missing file, so call it per path
	}
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg *Config) error {
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if cfg.CreateTable {
		if _, err := pool.Exec(ctx, schemaSQL); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}

	seed := cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	gen := NewGenerator(seed, cfg.Tenants, cfg.Users)
	log.Printf("mode=%s start=%s seed=%d", cfg.Mode, cfg.StartTime.Format(time.RFC3339), seed)

	var total int64
	queries := db.New(pool)
	flush := func(rows []db.InsertAPIRequestsParams) error {
		if len(rows) == 0 {
			return nil
		}
		n, err := queries.InsertAPIRequests(ctx, rows)
		total += n
		return err
	}

	switch cfg.Mode {
	case "once":
		err = runOnce(ctx, cfg, gen, flush, &total)
	default:
		err = runContinuous(ctx, cfg, gen, flush, &total)
	}
	log.Printf("done: %d rows inserted", total)
	return err
}

// runOnce spreads cfg.Rows evenly across [start, end].
func runOnce(ctx context.Context, cfg *Config, gen *Generator, flush func([]db.InsertAPIRequestsParams) error, total *int64) error {
	span := cfg.EndTime.Sub(cfg.StartTime)
	step := time.Duration(0)
	if cfg.Rows > 1 {
		step = span / time.Duration(cfg.Rows-1)
	}
	batch := make([]db.InsertAPIRequestsParams, 0, cfg.BatchSize)
	for i := int64(0); i < cfg.Rows; i++ {
		if ctx.Err() != nil {
			break
		}
		batch = append(batch, gen.Next(cfg.StartTime.Add(time.Duration(i)*step)))
		if len(batch) == cfg.BatchSize {
			if err := flush(batch); err != nil {
				return err
			}
			batch = batch[:0]
			log.Printf("inserted %d/%d", *total, cfg.Rows)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return flush(batch)
}

// runContinuous advances a simulated clock from start at cfg.Rate rows/sec.
// Rows whose simulated time is already in the past are inserted immediately
// (backfill); once the clock catches up with wall time it runs at real-time pace.
func runContinuous(ctx context.Context, cfg *Config, gen *Generator, flush func([]db.InsertAPIRequestsParams) error, total *int64) error {
	interval := time.Duration(float64(time.Second) / cfg.Rate)
	if interval <= 0 {
		interval = 1
	}
	var end time.Time
	if cfg.Duration > 0 {
		end = cfg.StartTime.Add(cfg.Duration)
	}
	next := cfg.StartTime
	lastLog := time.Now()
	batch := make([]db.InsertAPIRequestsParams, 0, cfg.BatchSize)

	for ctx.Err() == nil {
		now := time.Now()
		batch = batch[:0]
		for len(batch) < cfg.BatchSize && !next.After(now) && (end.IsZero() || next.Before(end)) {
			batch = append(batch, gen.Next(next))
			next = next.Add(interval)
		}
		if len(batch) > 0 {
			if err := flush(batch); err != nil {
				return err
			}
			if time.Since(lastLog) > 5*time.Second {
				lastLog = time.Now()
				log.Printf("inserted %d rows; simulated time %s (lag %s)", *total,
					next.Format(time.RFC3339), now.Sub(next).Round(time.Second))
			}
			continue // possibly still behind; don't sleep
		}
		if !end.IsZero() && !next.Before(end) {
			return nil
		}
		// Caught up: wait until enough rows are due to fill a modest batch (max 1s).
		wait := time.Until(next)
		if max := time.Second; wait > max {
			wait = max
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return ctx.Err()
}
