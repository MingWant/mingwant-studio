package service

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
)

func TestSafeCSVCellPreventsFormulaExecution(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "equals", value: "=HYPERLINK(\"https://example.invalid\")", want: "'=HYPERLINK(\"https://example.invalid\")"},
		{name: "plus", value: "+cmd|' /C calc'!A0", want: "'+cmd|' /C calc'!A0"},
		{name: "minus expression", value: "-1+SUM(1,1)", want: "'-1+SUM(1,1)"},
		{name: "at", value: "@SUM(1,1)", want: "'@SUM(1,1)"},
		{name: "leading spaces", value: "  =SUM(1,1)", want: "'  =SUM(1,1)"},
		{name: "leading tab", value: "\tordinary", want: "'\tordinary"},
		{name: "leading carriage return", value: "\r=SUM(1,1)", want: "'\r=SUM(1,1)"},
		{name: "unicode format prefix", value: "\uFEFF=SUM(1,1)", want: "'\uFEFF=SUM(1,1)"},
		{name: "invalid utf8", value: string([]byte{0xff, '=', '1'}), want: "'\uFFFD=1"},
		{name: "plain negative integer", value: "-123", want: "-123"},
		{name: "plain negative decimal", value: "-123.45", want: "-123.45"},
		{name: "ordinary", value: "model-name", want: "model-name"},
		{name: "ordinary with spaces", value: "  model-name", want: "  model-name"},
		{name: "empty", value: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeCSVCell(test.value); got != test.want {
				t.Fatalf("safeCSVCell(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestWriteSafeCSVRecordKeepsCellsParseable(t *testing.T) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writeSafeCSVRecord(writer, []string{"=SUM(1,1)", "normal,field", "-42", "\ttext"}); err != nil {
		t.Fatal(err)
	}
	if err := flushCSVWriter(writer); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(&buffer).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"'=SUM(1,1)", "normal,field", "-42", "'\ttext"}
	if len(records) != 1 || len(records[0]) != len(want) {
		t.Fatalf("unexpected records: %#v", records)
	}
	for index := range want {
		if records[0][index] != want[index] {
			t.Fatalf("record[%d] = %q, want %q", index, records[0][index], want[index])
		}
	}
}

func TestFlushCSVWriterReturnsUnderlyingError(t *testing.T) {
	expected := errors.New("write failed")
	writer := csv.NewWriter(failingCSVWriter{err: expected})
	if err := writeSafeCSVRecord(writer, []string{strings.Repeat("x", 16)}); err != nil {
		t.Fatal(err)
	}
	if err := flushCSVWriter(writer); !errors.Is(err, expected) {
		t.Fatalf("flushCSVWriter() error = %v, want %v", err, expected)
	}
}

func TestWriteSafeCSVRecordReturnsUnderlyingError(t *testing.T) {
	expected := errors.New("write failed")
	writer := csv.NewWriter(failingCSVWriter{err: expected})
	if err := writeSafeCSVRecord(writer, []string{strings.Repeat("x", 8192)}); !errors.Is(err, expected) {
		t.Fatalf("writeSafeCSVRecord() error = %v, want %v", err, expected)
	}
}

type failingCSVWriter struct {
	err error
}

func (writer failingCSVWriter) Write(_ []byte) (int, error) {
	return 0, writer.err
}
