// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strconv"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// DefaultImportBatchSize is the number of rows sent to
// SubscriptionRepository.ImportBatch per call when ImportOptions.BatchSize is
// unset. See docs/subscriber-import.md for the tuning rationale.
const DefaultImportBatchSize = 1000

// emailColumn, subscribedColumn, and sourceColumn are the recognized CSV
// header names, matched case-insensitively. subscribedColumn is required —
// see the "Compliance: verbatim subscribed state" note in
// docs/subscriber-import.md. Gatewaze's own list_subscriptions.subscribed
// value must round-trip through this column unchanged; defaulting it when
// absent would risk resubscribing someone who already opted out upstream.
const (
	emailColumn      = "email"
	subscribedColumn = "subscribed"
	sourceColumn     = "source"
)

// ImportSummary reports the outcome of one Import call. Every field is a
// count over the input file except Inserted and AlreadyPresent, which come
// from SubscriptionRepository.ImportBatch and therefore reflect the
// database's (list_id, email) state, not just what this run attempted.
type ImportSummary struct {
	TotalRows      int
	Valid          int
	Invalid        int
	DedupedInFile  int
	Inserted       int
	AlreadyPresent int
}

// ImportOptions configures one Import call.
type ImportOptions struct {
	// ListID is the newsletter_subscriptions.list_id value written for every
	// row in this run. Must satisfy the database's list_id CHECK constraint.
	ListID string
	// BatchSize is the number of rows per ImportBatch call. Defaults to
	// DefaultImportBatchSize when <= 0.
	BatchSize int
}

// SubscriptionImporter loads a CSV subscriber export into a
// SubscriptionRepository. See docs/subscriber-import.md for the CSV contract.
type SubscriptionImporter struct {
	Repo port.SubscriptionRepository
}

// NewSubscriptionImporter constructs a SubscriptionImporter over repo.
func NewSubscriptionImporter(repo port.SubscriptionRepository) *SubscriptionImporter {
	return &SubscriptionImporter{Repo: repo}
}

// Import reads CSV rows from r, validates and dedupes them in memory, and
// writes accepted rows to imp.Repo in batches. Rejected rows (malformed
// email, unparseable subscribed value) are written to rejectedOut as CSV
// (original columns plus a trailing "reason" column) and counted, but never
// cause Import to return an error — only a non-validation failure (a
// malformed file, a missing required column, or a repository error) does
// that, per the CLI's "non-zero exit only for non-validation failures"
// contract.
func (imp *SubscriptionImporter) Import(ctx context.Context, r io.Reader, rejectedOut io.Writer, opts ImportOptions) (ImportSummary, error) {
	var summary ImportSummary

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultImportBatchSize
	}

	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return summary, fmt.Errorf("read CSV header: %w", err)
	}
	emailIdx, subscribedIdx, sourceIdx, err := resolveColumns(header)
	if err != nil {
		return summary, err
	}

	rejectedWriter := csv.NewWriter(rejectedOut)
	rejectedHeader := append(append([]string{}, header...), "reason")
	if err := rejectedWriter.Write(rejectedHeader); err != nil {
		return summary, fmt.Errorf("write rejected-rows header: %w", err)
	}

	rowOrder := make([]string, 0, batchSize)
	byEmail := make(map[string]model.Subscription, batchSize)

	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return summary, fmt.Errorf("read CSV row %d: %w", summary.TotalRows+2, readErr)
		}
		summary.TotalRows++

		row, reason := parseRow(record, emailIdx, subscribedIdx, sourceIdx)
		if reason != "" {
			summary.Invalid++
			if writeErr := rejectedWriter.Write(append(append([]string{}, record...), reason)); writeErr != nil {
				return summary, fmt.Errorf("write rejected row: %w", writeErr)
			}
			continue
		}

		summary.Valid++
		if _, exists := byEmail[row.Email]; exists {
			summary.DedupedInFile++
		} else {
			rowOrder = append(rowOrder, row.Email)
		}
		byEmail[row.Email] = row
	}
	rejectedWriter.Flush()
	if err := rejectedWriter.Error(); err != nil {
		return summary, fmt.Errorf("flush rejected-rows writer: %w", err)
	}

	slog.InfoContext(ctx, "subscription import: file parsed",
		"total_rows", summary.TotalRows,
		"valid", summary.Valid,
		"invalid", summary.Invalid,
		"deduped_in_file", summary.DedupedInFile,
	)

	batch := make([]model.Subscription, 0, batchSize)
	batchNum := 0
	totalBatches := (len(rowOrder) + batchSize - 1) / batchSize
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batchNum++
		inserted, importErr := imp.Repo.ImportBatch(ctx, opts.ListID, batch)
		if importErr != nil {
			return fmt.Errorf("import batch %d/%d: %w", batchNum, totalBatches, importErr)
		}
		summary.Inserted += inserted
		summary.AlreadyPresent += len(batch) - inserted
		slog.InfoContext(ctx, "subscription import: batch complete",
			"batch", batchNum,
			"total_batches", totalBatches,
			"rows", len(batch),
			"inserted", inserted,
			"already_present", len(batch)-inserted,
		)
		batch = batch[:0]
		return nil
	}

	for _, email := range rowOrder {
		batch = append(batch, byEmail[email])
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return summary, err
			}
		}
	}
	if err := flush(); err != nil {
		return summary, err
	}

	slog.InfoContext(ctx, "subscription import: complete",
		"total_rows", summary.TotalRows,
		"valid", summary.Valid,
		"invalid", summary.Invalid,
		"deduped_in_file", summary.DedupedInFile,
		"inserted", summary.Inserted,
		"already_present", summary.AlreadyPresent,
	)
	return summary, nil
}

// resolveColumns finds the (case-insensitive) header positions this importer
// requires. subscribedColumn is mandatory — see the doc comment on the
// subscribedColumn constant. sourceColumn is optional; -1 means absent.
func resolveColumns(header []string) (emailIdx, subscribedIdx, sourceIdx int, err error) {
	emailIdx, subscribedIdx, sourceIdx = -1, -1, -1
	for i, col := range header {
		switch strings.ToLower(strings.TrimSpace(col)) {
		case emailColumn:
			emailIdx = i
		case subscribedColumn:
			subscribedIdx = i
		case sourceColumn:
			sourceIdx = i
		}
	}
	if emailIdx == -1 {
		return 0, 0, 0, fmt.Errorf("CSV is missing required column %q", emailColumn)
	}
	if subscribedIdx == -1 {
		return 0, 0, 0, fmt.Errorf("CSV is missing required column %q: this importer will not guess a subscribed state, to avoid resubscribing someone who already opted out in Gatewaze", subscribedColumn)
	}
	return emailIdx, subscribedIdx, sourceIdx, nil
}

// parseRow validates and normalizes one CSV record. On success it returns a
// zero-reason Subscription with a lowercased, trimmed Email. On failure it
// returns a non-empty reason describing why the row was rejected; the
// returned Subscription is unused in that case.
func parseRow(record []string, emailIdx, subscribedIdx, sourceIdx int) (model.Subscription, string) {
	rawEmail := field(record, emailIdx)
	trimmedEmail := strings.TrimSpace(rawEmail)
	if trimmedEmail == "" {
		return model.Subscription{}, "email is required"
	}
	addr, err := mail.ParseAddress(trimmedEmail)
	if err != nil {
		return model.Subscription{}, fmt.Sprintf("invalid email: %v", err)
	}

	rawSubscribed := strings.TrimSpace(field(record, subscribedIdx))
	subscribed, ok := parseBool(rawSubscribed)
	if !ok {
		return model.Subscription{}, fmt.Sprintf("subscribed value %q not recognized (expected true/false, 1/0, yes/no)", rawSubscribed)
	}

	source := strings.TrimSpace(field(record, sourceIdx))
	if source == "" {
		source = model.SubscriptionSourceImport
	}

	return model.Subscription{
		Email:      strings.ToLower(addr.Address),
		Subscribed: subscribed,
		Source:     source,
	}, ""
}

// field returns record[idx], or "" when idx is -1 (column absent) or the row
// is short a trailing field.
func field(record []string, idx int) string {
	if idx < 0 || idx >= len(record) {
		return ""
	}
	return record[idx]
}

// parseBool accepts strconv.ParseBool's vocabulary (1/0, t/f, true/false,
// case-insensitive) plus yes/no, since subscriber-list exports commonly use
// either convention.
func parseBool(s string) (bool, bool) {
	if b, err := strconv.ParseBool(s); err == nil {
		return b, true
	}
	switch strings.ToLower(s) {
	case "yes", "y":
		return true, true
	case "no", "n":
		return false, true
	default:
		return false, false
	}
}
