package actions

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ReadExcelRows: Belirtilen Excel dosyasının belirtilen sayfasındaki satırları okur.
// İlk satırı başlık (keys) olarak kabul edip map dizisi döner.
func ReadExcelRows(filePath string, sheetName string, maxRows int) ([]map[string]any, []string, error) {
	if filePath == "" {
		return nil, nil, errors.New("excel dosya yolu boş olamaz")
	}

	f, err := excelize.OpenFile(filepath.Clean(filePath))
	if err != nil {
		return nil, nil, fmt.Errorf("excel dosyası açılamadı (%s): %w", filePath, err)
	}
	defer f.Close()

	if sheetName == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return nil, nil, errors.New("excel dosyasında sayfa bulunamadı")
		}
		sheetName = sheets[0]
	}

	rows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, nil, fmt.Errorf("sayfa '%s' okunamadı: %w", sheetName, err)
	}
	if len(rows) == 0 {
		return []map[string]any{}, []string{}, nil
	}

	// 1. Satır: Başlıklar
	headers := make([]string, len(rows[0]))
	for i, h := range rows[0] {
		headers[i] = strings.TrimSpace(h)
	}

	result := make([]map[string]any, 0)
	for rIdx := 1; rIdx < len(rows); rIdx++ {
		if maxRows > 0 && len(result) >= maxRows {
			break
		}
		row := rows[rIdx]
		rowMap := make(map[string]any)
		hasVal := false
		for cIdx, header := range headers {
			if header == "" {
				continue
			}
			var val string
			if cIdx < len(row) {
				val = strings.TrimSpace(row[cIdx])
			}
			if val != "" {
				hasVal = true
			}
			rowMap[header] = val
		}
		if hasVal {
			result = append(result, rowMap)
		}
	}

	return result, headers, nil
}
