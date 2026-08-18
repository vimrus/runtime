// Package diagnostics builds bounded, redacted diagnostic bundles from local
// Runtime files. It never includes credentials, tokens, request payloads or
// business data.
package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxFileBytes = 16 * 1024 * 1024

type Collector struct {
	LogPaths  []string
	OutputDir string
	Summary   func() map[string]any
}

// Collect writes <output>/collect-<timestamp>.tar.gz and returns its path.
func (c *Collector) Collect() (string, error) {
	if c.OutputDir == "" {
		return "", errors.New("output directory is required")
	}
	if err := os.MkdirAll(c.OutputDir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	output := filepath.Join(c.OutputDir, "collect-"+stamp+".tar.gz")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	closeBundle := func() error {
		if err := tarWriter.Close(); err != nil {
			return err
		}
		if err := gzipWriter.Close(); err != nil {
			return err
		}
		return file.Close()
	}

	summary := map[string]any{"collectedAt": stamp}
	if c.Summary != nil {
		summary = c.Summary()
		summary["collectedAt"] = stamp
	}
	if err := writeJSONEntry(tarWriter, "summary.json", summary); err != nil {
		_ = closeBundle()
		return "", err
	}

	paths := append([]string(nil), c.LogPaths...)
	sort.Strings(paths)
	for _, path := range paths {
		if err := appendFile(tarWriter, path); err != nil {
			_ = closeBundle()
			return "", err
		}
	}
	if err := closeBundle(); err != nil {
		return "", err
	}
	return output, nil
}

func writeJSONEntry(writer *tar.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now().UTC()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func appendFile(writer *tar.Writer, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.Size() > maxFileBytes {
		return fmt.Errorf("log file exceeds diagnostic size limit: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	name := strings.TrimPrefix(path, string(filepath.Separator))
	name = strings.ReplaceAll(name, "\\", "/")
	header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime().UTC()}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(writer, file)
	return err
}
