package olap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	QuerySchemaVersion = 1
	MaxQueryOffset     = 1_000_000
)

var ErrDatasetUnavailable = errors.New("OLAP dataset is not available")

// QueryRequest is the private OLAP query contract.
type QueryRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	SQL           string `json:"sql"`
	Params        []any  `json:"params,omitempty"`
	MaxRows       int    `json:"maxRows,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	SkipCount     bool   `json:"skipCount,omitempty"`
}

// QueryResult is consumed by the application OLAP query client.
type QueryResult struct {
	SchemaVersion        int               `json:"schemaVersion"`
	CompatibilityVersion int               `json:"compatibilityVersion"`
	Columns              []string          `json:"columns"`
	ColumnTypes          map[string]string `json:"columnTypes,omitempty"`
	Rows                 []map[string]any  `json:"rows"`
	Total                int64             `json:"total"`
	Generations          map[string]uint64 `json:"generations"`
	Truncated            bool              `json:"truncated"`
}

// QueryEngine executes validated OLAP SQL against one application dataset.
type QueryEngine interface {
	Query(ctx context.Context, datasetRoot string, request QueryRequest) (QueryResult, error)
}

// LoadManifests returns the currently visible Manifest for every logical table.
func LoadManifests(datasetRoot string) (map[string]Manifest, error) {
	dir := filepath.Join(datasetRoot, "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: manifests directory is missing", ErrDatasetUnavailable)
		}
		return nil, fmt.Errorf("read OLAP manifests: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	manifests := make(map[string]Manifest)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		manifest, err := ReadManifest(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("load OLAP manifest %s: %w", entry.Name(), err)
		}
		if _, exists := manifests[manifest.Table]; exists {
			return nil, fmt.Errorf("duplicate OLAP manifest table %q", manifest.Table)
		}
		manifests[manifest.Table] = manifest
	}
	return manifests, nil
}

var forbiddenQueryTokens = []string{
	"ATTACH", "INSTALL", "LOAD", "COPY", "PRAGMA", "CALL", "EXPORT", "IMPORT", "VACUUM", "CHECKPOINT",
	"READ_CSV", "READ_JSON", "READ_PARQUET", "READ_BLOB", "READ_TEXT", "READ_DATABASE",
	"PARQUET_SCAN", "CSV_SCAN", "JSON_SCAN", "GLOB", "QUERY", "QUERY_TABLE",
	"ICEBERG_SCAN", "DELTA_SCAN", "SQLITE_SCAN", "POSTGRES_SCAN", "MYSQL_SCAN",
}

// ValidateOLAPQuery enforces the runtime side of the read-only SQL contract.
func ValidateOLAPQuery(sql string, maxBytes int) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return errors.New("OLAP SQL is empty")
	}
	if maxBytes > 0 && len(sql) > maxBytes {
		return fmt.Errorf("OLAP SQL exceeds %d bytes", maxBytes)
	}
	if !regexp.MustCompile(`(?i)^\s*(SELECT|WITH)\b`).MatchString(sql) {
		return errors.New("OLAP SQL must be a read-only SELECT query")
	}

	clean, err := sqlWithoutQuotedAndComments(sql)
	if err != nil {
		return err
	}
	upper := strings.ToUpper(clean)
	for _, token := range forbiddenQueryTokens {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(token) + `\b`).MatchString(upper) {
			return fmt.Errorf("OLAP SQL contains forbidden token %q", token)
		}
	}
	if regexp.MustCompile(`\bREAD_[A-Z0-9_]+\b`).MatchString(upper) {
		return errors.New("OLAP SQL contains forbidden file reader")
	}
	for _, pattern := range []string{
		`\bINSERT\s+INTO\b`,
		"\\bUPDATE\\s+[A-Za-z_\"`]+\\s+SET\\b",
		`\bDELETE\s+FROM\b`,
		`\bMERGE\s+INTO\b`,
		`\b(CREATE|ALTER|DROP)\s+(TABLE|VIEW|MACRO|INDEX|SCHEMA|DATABASE|SECRET|TYPE|SEQUENCE)\b`,
		`\bTRUNCATE\s+TABLE\b`,
	} {
		if regexp.MustCompile(`(?i)` + pattern).MatchString(clean) {
			return errors.New("OLAP SQL contains a write operation")
		}
	}
	if strings.Contains(strings.TrimSuffix(clean, ";"), ";") {
		return errors.New("OLAP SQL must contain exactly one statement")
	}
	return nil
}

func sqlWithoutQuotedAndComments(sql string) (string, error) {
	length := len(sql)
	var result strings.Builder
	var quote byte
	lineComment := false
	blockComment := false

	for index := 0; index < length; index++ {
		char := sql[index]
		var next byte
		if index+1 < length {
			next = sql[index+1]
		}
		if lineComment {
			if char == '\n' {
				lineComment = false
				result.WriteByte(' ')
			}
			continue
		}
		if blockComment {
			if char == '*' && next == '/' {
				blockComment = false
				index++
				result.WriteByte(' ')
			}
			continue
		}
		if quote != 0 {
			if char == '\\' && index+1 < length {
				index++
				continue
			}
			if char == quote {
				if next == quote {
					index++
					continue
				}
				quote = 0
			}
			continue
		}
		if char == '-' && next == '-' {
			lineComment = true
			index++
			continue
		}
		if char == '#' {
			lineComment = true
			continue
		}
		if char == '/' && next == '*' {
			blockComment = true
			index++
			continue
		}
		if char == '\'' || char == '"' || char == '`' {
			quote = char
			continue
		}
		result.WriteByte(char)
	}
	if quote != 0 || blockComment {
		return "", errors.New("OLAP SQL contains an unterminated string or comment")
	}
	return result.String(), nil
}
