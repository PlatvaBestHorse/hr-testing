package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestCSVSafeAndRoundtrip(t *testing.T) {
	inputs := []string{"=1+1", " +SUM(A1)", "-1", "@SUM(A1)", "\t=1", "\uFEFF=1", "\u200B=1", "Обычный; текст\nи кавычки \""}
	sheet := exportSheet{name: "Test", rows: [][]exportCell{cells(inputs...), {number("2.50")}}}
	data, err := csvFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\uFEFF")) {
		t.Fatal("missing UTF-8 BOM")
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	r.Comma = ';'
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for n, input := range inputs {
		want := "'" + input
		if n == len(inputs)-1 {
			want = input
		}
		if rows[0][n] != want {
			t.Fatalf("CSV did not preserve/neutralize field %d", n)
		}
	}
	if rows[1][0] != "2,50" {
		t.Fatal("CSV Russian decimal format incorrect")
	}
}
func TestXLSXLiteralStringsAndValidXML(t *testing.T) {
	data, err := xlsxFile([]exportSheet{{name: "Ответы", rows: [][]exportCell{cells("Header"), cells("=HYPERLINK(\"https://example.test\") & < Кириллица"), {number("62.50")}}}})
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	hasWorkbook := false
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		d := xml.NewDecoder(bytes.NewReader(body))
		for {
			_, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid XML in %s", f.Name)
			}
		}
		if f.Name == "xl/workbook.xml" {
			hasWorkbook = true
		}
		if strings.HasPrefix(f.Name, "xl/worksheets/") {
			if bytes.Contains(body, []byte("<f>")) || !bytes.Contains(body, []byte(`t="inlineStr"`)) || !bytes.Contains(body, []byte("<v>62.50</v>")) {
				t.Fatal("XLSX strings became formulas or numeric score is missing")
			}
		}
	}
	if !hasWorkbook {
		t.Fatal("missing workbook")
	}
	for _, value := range []string{"NaN", "+Inf", "not a number"} {
		if _, err = xlsxFile([]exportSheet{{name: "Test", rows: [][]exportCell{{number(value)}}}}); err == nil {
			t.Fatal("invalid numeric value accepted")
		}
	}
}
