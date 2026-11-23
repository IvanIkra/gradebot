package workbook

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Load gives the visible tabs of a workbook: a NotWorkbookError when the data is none, a BrokenWorkbookError when a
// tab cannot be parsed.
func Load(data []byte) ([]*Tab, error) {
	book, err := open(data)
	if err != nil {
		return nil, &NotWorkbookError{fmt.Sprintf("not a workbook (%v)", err)}
	}
	var tabs []*Tab
	for _, sheet := range book.sheets {
		if sheet.state != "" && sheet.state != "visible" {
			continue
		}
		cells, err := book.cells(sheet.path)
		if err != nil {
			return nil, &BrokenWorkbookError{fmt.Sprintf("broken workbook (%v)", err)}
		}
		tabs = append(tabs, newTab(sheet.name, cells))
	}
	return tabs, nil
}

type sheetRef struct{ name, state, path string }

type book struct {
	files    map[string]*zip.File
	sheets   []sheetRef
	strings  []string
	dates    map[int]bool // styles (cellXfs) with a date format
	elapsed  map[int]bool // styles with an elapsed-time format
	date1904 bool
}

func (b *book) read(name string) ([]byte, error) {
	f, ok := b.files[name]
	if !ok {
		return nil, fmt.Errorf("no %s", name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

type rels struct {
	Rels []struct {
		ID     string `xml:"Id,attr"`
		Type   string `xml:"Type,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

func target(base, t string) string {
	if strings.HasPrefix(t, "/") {
		return strings.TrimPrefix(t, "/")
	}
	return path.Clean(path.Join(base, t))
}

func open(data []byte) (*book, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	b := &book{files: map[string]*zip.File{}}
	for _, f := range z.File {
		b.files[f.Name] = f
	}
	main := "xl/workbook.xml"
	if raw, err := b.read("_rels/.rels"); err == nil {
		var r rels
		if xml.Unmarshal(raw, &r) == nil {
			for _, rel := range r.Rels {
				if strings.HasSuffix(rel.Type, "/officeDocument") {
					main = target("", rel.Target)
				}
			}
		}
	}
	raw, err := b.read(main)
	if err != nil {
		return nil, err
	}
	var wb struct {
		Pr struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
		Sheets []struct {
			Name  string `xml:"name,attr"`
			State string `xml:"state,attr"`
			RID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(raw, &wb); err != nil {
		return nil, err
	}
	b.date1904 = wb.Pr.Date1904 == "1" || wb.Pr.Date1904 == "true"
	dir := path.Dir(main)
	relsRaw, err := b.read(path.Join(dir, "_rels", path.Base(main)+".rels"))
	if err != nil {
		return nil, err
	}
	var wbRels rels
	if err := xml.Unmarshal(relsRaw, &wbRels); err != nil {
		return nil, err
	}
	byID := map[string]string{}
	stringsPath, stylesPath := "", ""
	for _, rel := range wbRels.Rels {
		switch {
		case strings.HasSuffix(rel.Type, "/worksheet"):
			byID[rel.ID] = target(dir, rel.Target)
		case strings.HasSuffix(rel.Type, "/sharedStrings"):
			stringsPath = target(dir, rel.Target)
		case strings.HasSuffix(rel.Type, "/styles"):
			stylesPath = target(dir, rel.Target)
		}
	}
	for _, s := range wb.Sheets {
		if p, ok := byID[s.RID]; ok { // chart sheets are no worksheets
			b.sheets = append(b.sheets, sheetRef{name: s.Name, state: s.State, path: p})
		}
	}
	if stringsPath != "" {
		if raw, err := b.read(stringsPath); err == nil {
			if b.strings, err = sharedStrings(raw); err != nil {
				return nil, err
			}
		}
	}
	b.dates, b.elapsed = map[int]bool{}, map[int]bool{}
	if stylesPath != "" {
		if raw, err := b.read(stylesPath); err == nil {
			if err := b.styles(raw); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}

// richText is <si> or <is>: plain text and the runs' text; phonetic runs are no part of it.
type richText struct {
	T    *string `xml:"t"`
	Runs []struct {
		T *string `xml:"t"`
	} `xml:"r"`
}

func (r richText) content() string {
	var b strings.Builder
	if r.T != nil {
		b.WriteString(*r.T)
	}
	for _, run := range r.Runs {
		if run.T != nil {
			b.WriteString(*run.T)
		}
	}
	return b.String()
}

func sharedStrings(raw []byte) ([]string, error) {
	var sst struct {
		Items []richText `xml:"si"`
	}
	if err := xml.Unmarshal(raw, &sst); err != nil {
		return nil, err
	}
	out := make([]string, len(sst.Items))
	for n, si := range sst.Items {
		out[n] = strings.ReplaceAll(si.content(), "x005F_", "")
	}
	return out, nil
}

// builtinFormats are the number formats with no code in the file.
var builtinFormats = map[int]string{0: "General", 1: "0", 2: "0.00", 3: "#,##0", 4: "#,##0.00",
	5: `"$"#,##0_);("$"#,##0)`, 6: `"$"#,##0_);[Red]("$"#,##0)`, 7: `"$"#,##0.00_);("$"#,##0.00)`,
	8: `"$"#,##0.00_);[Red]("$"#,##0.00)`, 9: "0%", 10: "0.00%", 11: "0.00E+00", 12: "# ?/?", 13: "# ??/??",
	14: "mm-dd-yy", 15: "d-mmm-yy", 16: "d-mmm", 17: "mmm-yy", 18: "h:mm AM/PM", 19: "h:mm:ss AM/PM", 20: "h:mm",
	21: "h:mm:ss", 22: "m/d/yy h:mm", 37: "#,##0_);(#,##0)", 38: "#,##0_);[Red](#,##0)",
	39: "#,##0.00_);(#,##0.00)", 40: "#,##0.00_);[Red](#,##0.00)",
	41: `_(* #,##0_);_(* \(#,##0\);_(* "-"_);_(@_)`, 42: `_("$"* #,##0_);_("$"* \(#,##0\);_("$"* "-"_);_(@_)`,
	43: `_(* #,##0.00_);_(* \(#,##0.00\);_(* "-"??_);_(@_)`,
	44: `_("$"* #,##0.00_)_("$"* \(#,##0.00\)_("$"* "-"??_)_(@_)`, 45: "mm:ss", 46: "[h]:mm:ss", 47: "mmss.0",
	48: "##0.0E+0", 49: "@"}

var (
	dateCharRe  = regexp.MustCompile(`[dmhysDMHYS]`)
	timedeltaRe = regexp.MustCompile(`\[hh?\](:mm(:ss(\.0*)?)?)?|\[mm?\](:ss(\.0*)?)?|\[ss?\](\.0*)?`)
)

// isDateFormat is openpyxl's: date letters in the first section, outside quotes and brackets (but [h], [mm], [ss]),
// not after "_" or "\".
func isDateFormat(code string) bool {
	code, _, _ = strings.Cut(code, ";")
	code = stripFormat(code)
	for _, loc := range dateCharRe.FindAllStringIndex(code, -1) {
		if loc[0] == 0 || (code[loc[0]-1] != '_' && code[loc[0]-1] != '\\') {
			return true
		}
	}
	return false
}

// stripFormat drops quoted text and bracketed parts but [h], [hh], [m], [mm], [s] and [ss], as openpyxl's STRIP_RE.
func stripFormat(code string) string {
	var out strings.Builder
	for i := 0; i < len(code); {
		switch code[i] {
		case '"':
			if end := strings.IndexByte(code[i+1:], '"'); end >= 0 {
				i += end + 2
				continue
			}
		case '[':
			if end := strings.IndexByte(code[i+1:], ']'); end >= 0 {
				switch code[i+1 : i+1+end] {
				case "h", "hh", "m", "mm", "s", "ss":
				default:
					i += end + 2
					continue
				}
			}
		}
		out.WriteByte(code[i])
		i++
	}
	return out.String()
}

func isTimedeltaFormat(code string) bool {
	code, _, _ = strings.Cut(code, ";")
	return timedeltaRe.MatchString(code)
}

func (b *book) styles(raw []byte) error {
	var ss struct {
		NumFmts []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Xfs []struct {
			NumFmtID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err := xml.Unmarshal(raw, &ss); err != nil {
		return err
	}
	custom := map[int]string{}
	for _, f := range ss.NumFmts {
		custom[f.ID] = f.Code
	}
	for idx, xf := range ss.Xfs {
		code, ok := custom[xf.NumFmtID]
		if !ok {
			code, ok = builtinFormats[xf.NumFmtID]
		}
		if !ok {
			continue
		}
		b.dates[idx] = isDateFormat(code)
		b.elapsed[idx] = isTimedeltaFormat(code)
	}
	return nil
}

var coordRe = regexp.MustCompile(`^\$?([A-Za-z]{1,3})\$?(\d+)$`)

// cells parses a worksheet into rows of raw values, row n at index n-1.
func (b *book) cells(name string) ([][]Value, error) {
	raw, err := b.read(name)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var rows [][]Value
	rowNumber, col := 0, 0
	set := func(r, c int, v Value) {
		for len(rows) < r {
			rows = append(rows, nil)
		}
		row := rows[r-1]
		for len(row) < c {
			row = append(row, nil)
		}
		row[c-1] = v
		rows[r-1] = row
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "row":
			rowNumber++
			col = 0
			for _, a := range start.Attr {
				if a.Name.Local == "r" {
					if n, err := strconv.Atoi(a.Value); err == nil {
						rowNumber = n
					}
				}
			}
			for len(rows) < rowNumber {
				rows = append(rows, nil)
			}
		case "c":
			var c struct {
				R string    `xml:"r,attr"`
				T string    `xml:"t,attr"`
				S string    `xml:"s,attr"`
				V *string   `xml:"v"`
				F *struct{} `xml:"f"`
				Is richText `xml:"is"`
			}
			if err := dec.DecodeElement(&c, &start); err != nil {
				return nil, err
			}
			r := rowNumber
			if m := coordRe.FindStringSubmatch(c.R); m != nil {
				col = Index(strings.ToUpper(m[1])) + 1
				r, _ = strconv.Atoi(m[2])
			} else {
				col++
			}
			v, err := b.value(c.T, c.S, c.V, c.Is)
			if err != nil {
				return nil, err
			}
			if v != nil && r > 0 {
				set(r, col, v)
			}
		}
	}
	return rows, nil
}

// value is a cell's value as openpyxl reads it with data_only: the cached result of a formula.
func (b *book) value(kind, style string, v *string, is richText) (Value, error) {
	if kind == "" {
		kind = "n"
	}
	if kind == "inlineStr" {
		return is.content(), nil
	}
	if v == nil || *v == "" {
		return nil, nil
	}
	switch kind {
	case "n":
		number, err := castNumber(*v)
		if err != nil {
			return nil, err
		}
		s, _ := strconv.Atoi(style)
		if b.dates[s] {
			return b.fromExcel(number, b.elapsed[s]), nil
		}
		return number, nil
	case "s":
		n, err := strconv.Atoi(strings.TrimSpace(*v))
		if err != nil || n < 0 || n >= len(b.strings) {
			return nil, fmt.Errorf("no shared string %q", *v)
		}
		return b.strings[n], nil
	case "b":
		n, err := strconv.Atoi(*v)
		if err != nil {
			return nil, err
		}
		return n != 0, nil
	case "d":
		for _, layout := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999",
			"2006-01-02", "15:04:05.999999999"} {
			if t, err := time.Parse(layout, *v); err == nil {
				return t, nil
			}
		}
		return *v, nil
	}
	return *v, nil // "str", "e": the text
}

// castNumber is an int, or a float when the text has a point or an exponent.
func castNumber(text string) (Value, error) {
	if strings.ContainsAny(text, ".eE") {
		return strconv.ParseFloat(text, 64)
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err == nil {
		return n, nil
	}
	if e, ok := err.(*strconv.NumError); ok && e.Err == strconv.ErrRange {
		return BigInt(strings.TrimLeft(text, "+")), nil
	}
	return nil, err
}

// fromExcel is a serial date as openpyxl's from_excel: 1899-12-30 (or 1904-01-01) plus days, milliseconds.
func (b *book) fromExcel(number Value, elapsed bool) Value {
	var value float64
	switch n := number.(type) {
	case int64:
		value = float64(n)
	case float64:
		value = n
	}
	if elapsed {
		us := math.Round(value * 86400 * 1e6)
		d := time.Duration(us) * time.Microsecond
		if frac := d % time.Second; frac != 0 {
			d = d - frac + time.Duration(math.RoundToEven(float64(frac/time.Microsecond)/1000))*time.Millisecond
		}
		return Duration{d}
	}
	day := math.Floor(value)
	fraction := value - day
	ms := math.RoundToEven(fraction * 86400 * 1000)
	diff := time.Duration(ms) * time.Millisecond
	if value >= 0 && value < 1 && diff < 24*time.Hour {
		return TimeOfDay{diff}
	}
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if b.date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if value > 0 && value < 60 {
		day++
	}
	return epoch.AddDate(0, 0, int(day)).Add(diff)
}
