package context

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/protocol"
	"github.com/xuri/excelize/v2"
)

// GetActiveExplorerPath: Windows işletim sisteminde o an açık veya odaktaki Dosya Gezgini'nin yolunu döner.
func GetActiveExplorerPath(ctx context.Context) (string, error) {
	if runtime.GOOS != "windows" {
		return "", nil
	}

	// PowerShell ile Shell.Application COM nesnesinden aktif Explorer pencerelerini sorgula
	script := `
$shell = New-Object -ComObject Shell.Application
foreach ($w in $shell.Windows()) {
    try {
        $p = $w.Document.Folder.Self.Path
        if ($p -and (Test-Path $p)) {
            Write-Output $p
            break
        }
    } catch {}
}
`
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to query windows explorer path: %w", err)
	}

	path := strings.TrimSpace(string(output))
	return path, nil
}

// DetectOSContext: Aktif klasörü tespit eder, içindeki dosya türlerini listeler ve örnek sütun başlıklarını yakalar.
func DetectOSContext(ctx context.Context, fallbackFolder string) (*protocol.OSContext, error) {
	activePath, err := GetActiveExplorerPath(ctx)
	if err != nil || activePath == "" {
		activePath = fallbackFolder
	}

	osCtx := &protocol.OSContext{
		ActiveWindow:      "Windows Explorer",
		ActiveFolderPath:  activePath,
		SelectedFiles:     make([]string, 0),
		DetectedFileTypes: make([]string, 0),
		SampleHeaders:     make([]string, 0),
		Metadata:          make(map[string]any),
	}

	if activePath == "" {
		return osCtx, nil
	}

	entries, err := os.ReadDir(activePath)
	if err != nil {
		return osCtx, fmt.Errorf("failed to read active directory %s: %w", activePath, err)
	}

	typeMap := make(map[string]bool)
	var firstExcelFile string
	var firstCsvFile string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != "" {
			typeMap[ext] = true
		}

		if len(osCtx.SelectedFiles) < 10 {
			osCtx.SelectedFiles = append(osCtx.SelectedFiles, name)
		}

		if firstExcelFile == "" && (ext == ".xlsx" || ext == ".xlsm") {
			firstExcelFile = filepath.Join(activePath, name)
		}
		if firstCsvFile == "" && ext == ".csv" {
			firstCsvFile = filepath.Join(activePath, name)
		}
	}

	for ext := range typeMap {
		osCtx.DetectedFileTypes = append(osCtx.DetectedFileTypes, ext)
	}

	// İlk tablodan örnek başlıkları oku (Peek Headers)
	if firstExcelFile != "" {
		headers, peekErr := peekExcelHeaders(firstExcelFile)
		if peekErr == nil && len(headers) > 0 {
			osCtx.SampleHeaders = headers
			osCtx.Metadata["sample_file"] = filepath.Base(firstExcelFile)
		}
	} else if firstCsvFile != "" {
		headers, peekErr := peekCsvHeaders(firstCsvFile)
		if peekErr == nil && len(headers) > 0 {
			osCtx.SampleHeaders = headers
			osCtx.Metadata["sample_file"] = filepath.Base(firstCsvFile)
		}
	}

	return osCtx, nil
}

func peekExcelHeaders(filePath string) ([]string, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open excel file: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	headers := make([]string, 0, len(rows[0]))
	for _, cell := range rows[0] {
		val := strings.TrimSpace(cell)
		if val != "" {
			headers = append(headers, val)
		}
	}
	return headers, nil
}

func peekCsvHeaders(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open csv file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	record, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read csv headers: %w", err)
	}

	headers := make([]string, 0, len(record))
	for _, col := range record {
		val := strings.TrimSpace(col)
		if val != "" {
			headers = append(headers, val)
		}
	}
	return headers, nil
}
