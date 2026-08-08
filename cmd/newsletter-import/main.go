// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// newsletter-import is a one-shot CLI that loads a CSV subscriber export into
// the newsletter_subscriptions table. It is deliberately not an HTTP
// endpoint: a bulk import is an infrequent, operator-run, potentially
// long-lived batch job, not a request a caller waits on synchronously, and
// giving it its own binary keeps a large one-time CSV upload out of the
// newsletter-api request path entirely. See docs/subscriber-import.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("newsletter-import failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		filePath    = flag.String("file", "", "path to the input CSV, or - for stdin (required)")
		listID      = flag.String("list-id", model.ListAAIFUserCommunity, "newsletter_subscriptions.list_id to import into")
		batchSize   = flag.Int("batch-size", service.DefaultImportBatchSize, "rows per database batch")
		rejectedOut = flag.String("rejected-out", "./rejected.csv", "path to write rejected rows as CSV")
	)
	flag.Parse()

	if strings.TrimSpace(*filePath) == "" {
		return errors.New("-file is required (use -file - to read from stdin)")
	}

	in, err := openInput(*filePath)
	if err != nil {
		return err
	}
	defer in.Close()

	rejectedFile, err := os.Create(*rejectedOut)
	if err != nil {
		return fmt.Errorf("create rejected-rows file %q: %w", *rejectedOut, err)
	}
	defer rejectedFile.Close()

	ctx := context.Background()
	databaseURL, ok := resolveDatabaseURL()
	if !ok {
		return errors.New("DATABASE_URL (or PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE) is required")
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	if err := schema.Apply(ctx, pool); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	defer db.Close()

	repo := repository.NewPostgresSubscriptionRepo(db)
	importer := service.NewSubscriptionImporter(repo)

	summary, err := importer.Import(ctx, in, rejectedFile, service.ImportOptions{
		ListID:    *listID,
		BatchSize: *batchSize,
	})
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}

	fmt.Printf("total_rows=%d valid=%d invalid=%d deduped_in_file=%d inserted=%d already_present=%d\n",
		summary.TotalRows, summary.Valid, summary.Invalid, summary.DedupedInFile, summary.Inserted, summary.AlreadyPresent)
	if summary.Invalid > 0 {
		fmt.Printf("rejected rows written to %s\n", *rejectedOut)
	}
	return nil
}

// openInput opens path for reading, or returns os.Stdin when path is "-".
// The returned ReadCloser is always safe to Close, including for stdin.
func openInput(path string) (*os.File, error) {
	if path == "-" {
		return os.Stdin, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input file %q: %w", path, err)
	}
	return f, nil
}

// resolveDatabaseURL mirrors cmd/newsletter-api/service.composeDatabaseURL:
// prefer DATABASE_URL, else compose from PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE
// so the CLI connects to the same database the same way the API service does.
// Duplicated rather than imported because composeDatabaseURL is unexported
// and cmd/newsletter-api/service is not meant to be imported by other binaries.
func resolveDatabaseURL() (string, bool) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn, true
	}

	host := strings.TrimSpace(os.Getenv("PGHOST"))
	user := strings.TrimSpace(os.Getenv("PGUSER"))
	password := os.Getenv("PGPASSWORD")
	database := strings.TrimSpace(os.Getenv("PGDATABASE"))
	if host == "" || user == "" || password == "" || database == "" {
		return "", false
	}
	port := strings.TrimSpace(os.Getenv("PGPORT"))
	if port == "" {
		port = "5432"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":" + port,
		Path:   "/" + database,
	}
	return u.String(), true
}
