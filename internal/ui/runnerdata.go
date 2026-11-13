package ui

// Runner data files: a CSV or JSON table whose columns become variables
// ({{column}}) for every request, one full pass per row, as Bruno's
// collection runner does.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gruno/internal/send"
)

// loadRunData reads a CSV (header row required) or a JSON array of flat
// objects into rows of string columns.
func loadRunData(path string) ([]map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return parseCSVData(data)
	case ".json", "":
		return parseJSONData(data)
	}
	return nil, fmt.Errorf("不支持的数据文件类型 %s（支持 .csv、.json）", filepath.Ext(path))
}

func parseCSVData(data []byte) ([]map[string]string, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV 需要表头和至少一行数据")
	}
	header := records[0]
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
		if header[i] == "" {
			header[i] = fmt.Sprintf("column%d", i+1)
		}
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(map[string]string, len(header))
		for i, name := range header {
			if i < len(rec) {
				row[name] = rec[i]
			} else {
				row[name] = ""
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseJSONData(data []byte) ([]map[string]string, error) {
	var list []map[string]any
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("JSON 数据应为对象数组: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("数据文件没有行")
	}
	rows := make([]map[string]string, 0, len(list))
	for _, obj := range list {
		row := make(map[string]string, len(obj))
		for k, v := range obj {
			switch x := v.(type) {
			case nil:
				row[k] = ""
			case string:
				row[k] = x
			case float64:
				row[k] = strconv.FormatFloat(x, 'f', -1, 64)
			case bool:
				if x {
					row[k] = "true"
				} else {
					row[k] = "false"
				}
			default:
				b, _ := json.Marshal(x)
				row[k] = string(b)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// dataPairs turns one row into variable pairs, sorted by name for
// deterministic UI output.
func dataPairs(row map[string]string) []send.Pair {
	names := make([]string, 0, len(row))
	for k := range row {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]send.Pair, 0, len(names))
	for _, k := range names {
		out = append(out, send.Pair{Key: k, Value: row[k], Enabled: true})
	}
	return out
}

// dataSummary describes the loaded table for the runner header.
func dataSummary(rows []map[string]string) string {
	if len(rows) == 0 {
		return ""
	}
	return fmt.Sprintf("%d 行数据 · 每行执行一次", len(rows))
}
