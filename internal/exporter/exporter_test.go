package exporter

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCSV(t *testing.T) {
	data, err := CSV([]string{"ID", "名称"}, [][]string{{"1", "a,b"}, {"2", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, utf8BOM) {
		t.Fatal("csv missing UTF-8 BOM")
	}
	body := string(data[len(utf8BOM):])
	if !strings.Contains(body, `"a,b"`) {
		t.Fatalf("comma value not quoted: %q", body)
	}
}

func TestXLSXValidZip(t *testing.T) {
	data, err := XLSX("events", []string{"ID", "名称"}, [][]string{{"1", "测试 <a&b>"}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	var sheet string
	for _, f := range zr.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			sheet = string(b)
		}
	}
	if sheet == "" {
		t.Fatal("sheet1.xml missing")
	}
	if !strings.Contains(sheet, "&lt;a&amp;b&gt;") {
		t.Fatalf("special chars not escaped: %s", sheet)
	}
}

func TestColName(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for idx, want := range cases {
		if got := colName(idx); got != want {
			t.Errorf("colName(%d)=%s want %s", idx, got, want)
		}
	}
}
