// Package exporter renders tabular data — a header plus rows — as a CSV or XLSX byte stream.
// It is domain-agnostic, taking only []string rows, and serves the handler layer's list
// exports. The XLSX path hand-writes a minimal OOXML structure with the standard library's
// archive/zip, pulling in no third-party dependency.
package exporter

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
)

// utf8BOM makes Excel open the CSV as UTF-8, which keeps non-ASCII text from being mangled.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// CSV renders a CSV byte stream with a UTF-8 BOM.
func CSV(headers []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(utf8BOM)
	w := csv.NewWriter(&buf)
	if len(headers) > 0 {
		if err := w.Write(headers); err != nil {
			return nil, err
		}
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// XLSX renders a minimal working .xlsx: one sheet, with every cell written as inlineStr text.
func XLSX(sheetName string, headers []string, rows [][]string) ([]byte, error) {
	if sheetName == "" {
		sheetName = "Sheet1"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML,
		"_rels/.rels":                rootRelsXML,
		"xl/workbook.xml":            workbookXML(sheetName),
		"xl/_rels/workbook.xml.rels": workbookRelsXML,
		"xl/worksheets/sheet1.xml":   sheetXML(headers, rows),
	}
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`</Types>`

const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
	`</Relationships>`

func workbookXML(sheetName string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<sheets><sheet name="` + escapeXML(sheetName) + `" sheetId="1" r:id="rId1"/></sheets>` +
		`</workbook>`
}

func sheetXML(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	rowNum := 1
	if len(headers) > 0 {
		writeRow(&b, rowNum, headers)
		rowNum++
	}
	for _, row := range rows {
		writeRow(&b, rowNum, row)
		rowNum++
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func writeRow(b *strings.Builder, rowNum int, cells []string) {
	b.WriteString(`<row r="`)
	b.WriteString(strconv.Itoa(rowNum))
	b.WriteString(`">`)
	for i, v := range cells {
		ref := colName(i) + strconv.Itoa(rowNum)
		b.WriteString(`<c r="`)
		b.WriteString(ref)
		b.WriteString(`" t="inlineStr"><is><t xml:space="preserve">`)
		b.WriteString(escapeXML(v))
		b.WriteString(`</t></is></c>`)
	}
	b.WriteString(`</row>`)
}

// colName converts a 0-based column index to an Excel column name (0 -> A, 25 -> Z, 26 -> AA).
func colName(idx int) string {
	name := ""
	for idx >= 0 {
		name = string(rune('A'+idx%26)) + name
		idx = idx/26 - 1
	}
	return name
}

// escapeXML escapes the XML special characters and strips the control characters OOXML
// disallows, keeping \t, \n and \r.
func escapeXML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
