// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reporting

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"time"
)

// Supported export formats.
const (
	FormatJSON = "json"
	FormatCSV  = "csv"
)

// AckExportRow is a single acknowledgment row in audit-quality exports.
// Field order in the CSV exporter matches the struct order below.
type AckExportRow struct {
	AckID           string    `json:"ack_id"`
	UserID          string    `json:"user_id"`
	PolicyVersionID string    `json:"policy_version_id"`
	AckedAt         time.Time `json:"acked_at"`
}

// AckFetcher returns all acknowledgment rows for a policy version. Implementations
// must return rows in a stable order (typically by acked_at ASC, ack_id ASC) so
// exports are reproducible.
type AckFetcher interface {
	FetchAcks(ctx context.Context, policyVersionID string) ([]AckExportRow, error)
}

// Exporter renders acknowledgment data into transport-ready bytes (CSV or JSON).
type Exporter struct {
	fetcher AckFetcher
}

// NewExporter constructs an Exporter backed by the given fetcher.
func NewExporter(f AckFetcher) *Exporter { return &Exporter{fetcher: f} }

// Export returns (data, contentType, error) for the given format.
//
// Supported formats: "json", "csv". For empty result sets, JSON returns "[]"
// and CSV returns just the header row.
func (e *Exporter) Export(ctx context.Context, policyVersionID, format string) ([]byte, string, error) {
	rows, err := e.fetcher.FetchAcks(ctx, policyVersionID)
	if err != nil {
		return nil, "", fmt.Errorf("fetch acks: %w", err)
	}

	switch format {
	case FormatJSON:
		// Normalize nil to empty slice so the export is always a valid JSON array.
		if rows == nil {
			rows = []AckExportRow{}
		}
		data, err := json.Marshal(rows)
		if err != nil {
			return nil, "", fmt.Errorf("marshal json: %w", err)
		}
		return data, "application/json", nil

	case FormatCSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := w.Write([]string{"ack_id", "user_id", "policy_version_id", "acked_at"}); err != nil {
			return nil, "", fmt.Errorf("write csv header: %w", err)
		}
		for _, r := range rows {
			if err := w.Write([]string{
				r.AckID,
				r.UserID,
				r.PolicyVersionID,
				r.AckedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return nil, "", fmt.Errorf("write csv row: %w", err)
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, "", fmt.Errorf("flush csv: %w", err)
		}
		return buf.Bytes(), "text/csv", nil

	default:
		return nil, "", fmt.Errorf("unsupported export format: %q", format)
	}
}
