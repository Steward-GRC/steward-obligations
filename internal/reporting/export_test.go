// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reporting_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/reporting"
)

type fakeAckFetcher struct {
	rows []reporting.AckExportRow
	err  error
}

func (f *fakeAckFetcher) FetchAcks(_ context.Context, policyVersionID string) ([]reporting.AckExportRow, error) {
	return f.rows, f.err
}

func sampleRows() []reporting.AckExportRow {
	t1, _ := time.Parse(time.RFC3339, "2026-05-01T10:00:00Z")
	t2, _ := time.Parse(time.RFC3339, "2026-05-02T11:30:00Z")
	return []reporting.AckExportRow{
		{
			AckID:           "ack-1",
			UserID:          "u1",
			PolicyVersionID: "pv1",
			AckedAt:         t1,
		},
		{
			AckID:           "ack-2",
			UserID:          "u2",
			PolicyVersionID: "pv1",
			AckedAt:         t2,
		},
	}
}

func TestExporter_JSON(t *testing.T) {
	fetcher := &fakeAckFetcher{rows: sampleRows()}
	exp := reporting.NewExporter(fetcher)

	data, contentType, err := exp.Export(context.Background(), "pv1", "json")
	if err != nil {
		t.Fatalf("Export json: %v", err)
	}
	if contentType != "application/json" {
		t.Errorf("contentType: got %q", contentType)
	}

	var decoded []reporting.AckExportRow
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded rows: got %d, want 2", len(decoded))
	}
	if decoded[0].AckID != "ack-1" || decoded[1].UserID != "u2" {
		t.Errorf("unexpected row content: %+v", decoded)
	}
}

func TestExporter_CSV(t *testing.T) {
	fetcher := &fakeAckFetcher{rows: sampleRows()}
	exp := reporting.NewExporter(fetcher)

	data, contentType, err := exp.Export(context.Background(), "pv1", "csv")
	if err != nil {
		t.Fatalf("Export csv: %v", err)
	}
	if contentType != "text/csv" {
		t.Errorf("contentType: got %q", contentType)
	}

	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("csv records: got %d, want 3 (1 header + 2 rows)", len(records))
	}
	wantHeader := []string{"ack_id", "user_id", "policy_version_id", "acked_at"}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Errorf("csv header[%d]: got %q, want %q", i, records[0][i], h)
		}
	}
	if records[1][0] != "ack-1" {
		t.Errorf("csv row 1 ack_id unexpected: %v", records[1])
	}
	if records[2][0] != "ack-2" {
		t.Errorf("csv row 2 ack_id unexpected: %v", records[2])
	}
	// acked_at column should be RFC3339
	if records[1][3] != "2026-05-01T10:00:00Z" {
		t.Errorf("csv acked_at row 1: got %q, want %q", records[1][3], "2026-05-01T10:00:00Z")
	}
}

func TestExporter_UnsupportedFormat(t *testing.T) {
	fetcher := &fakeAckFetcher{rows: sampleRows()}
	exp := reporting.NewExporter(fetcher)

	_, _, err := exp.Export(context.Background(), "pv1", "xml")
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	if !strings.Contains(err.Error(), "xml") {
		t.Errorf("expected error to mention the format, got: %v", err)
	}
}

func TestExporter_FetchError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	fetcher := &fakeAckFetcher{err: wantErr}
	exp := reporting.NewExporter(fetcher)

	_, _, err := exp.Export(context.Background(), "pv1", "json")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped fetch error, got: %v", err)
	}
}

func TestExporter_EmptyRows(t *testing.T) {
	fetcher := &fakeAckFetcher{rows: []reporting.AckExportRow{}}
	exp := reporting.NewExporter(fetcher)

	// JSON: should produce a valid empty array.
	data, _, err := exp.Export(context.Background(), "pv1", "json")
	if err != nil {
		t.Fatalf("Export json empty: %v", err)
	}
	var decoded []reporting.AckExportRow
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal json empty: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected zero rows, got %d", len(decoded))
	}

	// CSV: should produce just the header row.
	data, _, err = exp.Export(context.Background(), "pv1", "csv")
	if err != nil {
		t.Fatalf("Export csv empty: %v", err)
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse csv empty: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected only header row, got %d records", len(records))
	}
}
