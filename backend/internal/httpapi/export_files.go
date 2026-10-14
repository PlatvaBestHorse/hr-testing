package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type exportCell struct {
	text   string
	number bool
}
type exportSheet struct {
	name string
	rows [][]exportCell
}

func cells(values ...string) []exportCell {
	out := []exportCell{}
	for _, v := range values {
		out = append(out, exportCell{text: v})
	}
	return out
}
func number(value string) exportCell { return exportCell{text: value, number: true} }

// Neutralize spreadsheet formulas in untrusted CSV text, including leading whitespace.
func csvSafe(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' || r == '\u200B' })
	if (len(value) > 0 && strings.ContainsRune("\t\r\n", rune(value[0]))) || (len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
		return "'" + value
	}
	return value
}
func csvFile(sheet exportSheet) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("\uFEFF")
	w := csv.NewWriter(&b)
	w.Comma = ';'
	w.UseCRLF = true
	for _, row := range sheet.rows {
		values := []string{}
		for _, cell := range row {
			value := cell.text
			if cell.number {
				value = strings.ReplaceAll(value, ".", ",")
			} else {
				value = csvSafe(value)
			}
			values = append(values, value)
		}
		if err := w.Write(values); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}
func xmlText(value string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(value))
	return b.String()
}
func columnName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

// Standard OpenXML with inline strings. User text can never become a formula.
func xlsxFile(sheets []exportSheet) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	put := func(name, body string) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(body))
		return err
	}
	types := `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`
	workbook := `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`
	rels := `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="styles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`
	for i, sheet := range sheets {
		n := i + 1
		types += fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		workbook += fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlText(sheet.name), n, n)
		rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		var body strings.Builder
		body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="18" defaultColWidth="24"/><sheetData>`)
		for r, row := range sheet.rows {
			fmt.Fprintf(&body, `<row r="%d">`, r+1)
			for c, cell := range row {
				address := columnName(c+1) + strconv.Itoa(r+1)
				style := 0
				if r == 0 {
					style = 1
				}
				if cell.number {
					if value, err := strconv.ParseFloat(cell.text, 64); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
						return nil, fmt.Errorf("invalid numeric export cell")
					}
					fmt.Fprintf(&body, `<c r="%s" s="%d"><v>%s</v></c>`, address, style, xmlText(cell.text))
				} else {
					fmt.Fprintf(&body, `<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, address, style, xmlText(cell.text))
				}
			}
			body.WriteString(`</row>`)
		}
		body.WriteString(`</sheetData></worksheet>`)
		if err := put(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), body.String()); err != nil {
			return nil, err
		}
	}
	entries := map[string]string{
		"[Content_Types].xml": types + `</Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":     workbook + `</sheets></workbook>`, "xl/_rels/workbook.xml.rels": rels + `</Relationships>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`,
	}
	for name, body := range entries {
		if err := put(name, body); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
