package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ParquetWriter produces one immutable Parquet file at targetPath. It must
// write a temp file and leave final publication to the Publisher's atomic
// rename, or write directly to a path supplied by the Publisher only after
// the Publisher has created the temp file contract.
type ParquetWriter interface {
	WriteParquet(ctx context.Context, kind Kind, targetPath string, events []Event) (int64, error)
}

type Config struct {
	DatasetRoot      string
	SpoolPath        string
	NodeID           string
	BootID           string
	MaxSpoolBytes    int64
	MaxBatchRows     int
	MaxBatchBytes    int64
	MinBatchRows     int
	MinBatchBytes    int64
	FlushInterval    time.Duration
	MaxFlushInterval time.Duration
}

type Publisher struct {
	config Config
	writer ParquetWriter

	mu              sync.Mutex
	publishMu       sync.Mutex
	buffer          map[Kind][]Event
	sequence        uint64
	spool           map[string]int64 // spool file -> bytes
	received        atomic.Uint64
	published       atomic.Uint64
	recovered       atomic.Uint64
	dropped         atomic.Uint64
	lastReceived    uint64
	lastDropped     uint64
	lastPublished   uint64
	lastRecovered   uint64
	lastBatchRows   uint64
	lastBatchBytes  uint64
	lastPublishedAt time.Time
}

// Stats exposes the observability pipeline's own counters for health,
// status and diagnostics. These counters are intentionally kept outside the
// Parquet pipeline so pipeline failures remain visible without relying on the
// pipeline itself.
type Stats struct {
	Received       uint64 `json:"received"`
	Published      uint64 `json:"published"`
	Recovered      uint64 `json:"recovered"`
	Dropped        uint64 `json:"dropped"`
	SpoolFiles     int    `json:"spoolFiles"`
	BufferedEvents int    `json:"bufferedEvents"`
}

func NewPublisher(config Config, writer ParquetWriter) (*Publisher, error) {
	if config.DatasetRoot == "" || config.SpoolPath == "" || config.BootID == "" {
		return nil, errors.New("datasetRoot, spoolPath and bootID are required")
	}
	if writer == nil {
		return nil, errors.New("parquet writer is required")
	}
	if config.MaxBatchRows <= 0 {
		config.MaxBatchRows = 10000
	}
	if config.MaxBatchBytes <= 0 {
		config.MaxBatchBytes = 16 * 1024 * 1024
	}
	if config.MaxSpoolBytes <= 0 {
		config.MaxSpoolBytes = 1024 * 1024 * 1024
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 60 * time.Second
	}
	if config.MinBatchRows <= 0 {
		config.MinBatchRows = config.MaxBatchRows / 10
		if config.MinBatchRows < 10 {
			config.MinBatchRows = 10
		}
	}
	if config.MinBatchBytes <= 0 {
		config.MinBatchBytes = config.MaxBatchBytes / 10
		if config.MinBatchBytes < 1024*1024 {
			config.MinBatchBytes = 1024 * 1024
		}
	}
	if config.MaxFlushInterval <= 0 {
		config.MaxFlushInterval = config.FlushInterval * 10
		if config.MaxFlushInterval < 10*time.Minute {
			config.MaxFlushInterval = 10 * time.Minute
		}
	}
	if err := os.MkdirAll(config.SpoolPath, 0o700); err != nil {
		return nil, fmt.Errorf("create spool path: %w", err)
	}
	if info, err := os.Stat(config.DatasetRoot); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("dataset root is not a directory: %s", config.DatasetRoot)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect dataset root: %w", err)
	}
	publisher := &Publisher{
		config: config,
		writer: writer,
		buffer: map[Kind][]Event{KindMetrics: nil, KindLogs: nil},
		spool:  make(map[string]int64),
	}
	if err := publisher.scanSpool(); err != nil {
		return nil, err
	}
	return publisher, nil
}

// Add normalizes and buffers one event, flushing when row or byte thresholds
// are reached.
func (p *Publisher) Add(ctx context.Context, event Event) error {
	normalized, err := Normalize(event)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.buffer[normalized.Kind] = append(p.buffer[normalized.Kind], normalized)
	kind := normalized.Kind
	rows := len(p.buffer[kind])
	bytes := estimateBytes(p.buffer[kind])
	needsFlush := rows >= p.config.MaxBatchRows || bytes >= p.config.MaxBatchBytes
	p.mu.Unlock()
	p.received.Add(1)
	if needsFlush {
		return p.Flush(ctx, kind)
	}
	return nil
}

// AddBatch normalizes and buffers a batch of events. If any event fails
// normalization the whole batch is rejected, so callers never observe
// partial acceptance.
func (p *Publisher) AddBatch(ctx context.Context, events []Event) (int, error) {
	normalized := make([]Event, 0, len(events))
	for _, event := range events {
		value, err := Normalize(event)
		if err != nil {
			return 0, err
		}
		normalized = append(normalized, value)
	}

	p.mu.Lock()
	for _, event := range normalized {
		p.buffer[event.Kind] = append(p.buffer[event.Kind], event)
	}
	var flushKinds []Kind
	for _, kind := range []Kind{KindLogs, KindMetrics} {
		if len(p.buffer[kind]) >= p.config.MaxBatchRows || estimateBytes(p.buffer[kind]) >= p.config.MaxBatchBytes {
			flushKinds = append(flushKinds, kind)
		}
	}
	p.mu.Unlock()
	p.received.Add(uint64(len(normalized)))

	for _, kind := range flushKinds {
		if err := p.Flush(ctx, kind); err != nil {
			return len(normalized), err
		}
	}
	return len(normalized), nil
}

func (p *Publisher) Flush(ctx context.Context, kind Kind) error {
	p.mu.Lock()
	events := p.buffer[kind]
	p.buffer[kind] = nil
	p.mu.Unlock()
	if len(events) == 0 {
		return nil
	}
	return p.publish(ctx, kind, events)
}

func (p *Publisher) FlushAll(ctx context.Context) error {
	for _, kind := range []Kind{KindMetrics, KindLogs} {
		if err := p.Flush(ctx, kind); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) Dropped() uint64 { return p.dropped.Load() }

// Stats returns the pipeline's own counters and current buffer/spool sizes.
func (p *Publisher) Stats() Stats {
	p.mu.Lock()
	spoolFiles := len(p.spool)
	buffered := len(p.buffer[KindLogs]) + len(p.buffer[KindMetrics])
	p.mu.Unlock()
	return Stats{
		Received:       p.received.Load(),
		Published:      p.published.Load(),
		Recovered:      p.recovered.Load(),
		Dropped:        p.dropped.Load(),
		SpoolFiles:     spoolFiles,
		BufferedEvents: buffered,
	}
}

// SelfMetricEvents returns the observability pipeline's own metrics as
// registered metric events. Counter values are deltas since the previous
// call, so the same counters can also be read from Stats without double
// counting in Parquet.
func (p *Publisher) SelfMetricEvents(now time.Time) []Event {
	stats := p.Stats()
	p.mu.Lock()
	receivedDelta := stats.Received - p.lastReceived
	droppedDelta := stats.Dropped - p.lastDropped
	publishedDelta := stats.Published - p.lastPublished
	p.lastReceived = stats.Received
	p.lastDropped = stats.Dropped
	p.lastPublished = stats.Published
	p.lastRecovered = stats.Recovered
	batchRows := p.lastBatchRows
	batchBytes := p.lastBatchBytes
	lastPublishedAt := p.lastPublishedAt
	p.mu.Unlock()

	now = now.UTC()
	base := Event{
		SchemaVersion: SchemaVersion,
		EventTime:     now,
		NodeID:        p.config.NodeID,
		BootID:        p.config.BootID,
		Source:        "runtime",
		Kind:          KindMetrics,
	}
	events := []Event{
		selfMetric(base, "observability_events_received_total", "counter", float64(receivedDelta), map[string]string{"source": "runtime"}),
		selfMetric(base, "observability_events_dropped_total", "counter", float64(droppedDelta), map[string]string{"reason": "pipeline"}),
		selfMetric(base, "observability_batch_publish_total", "counter", float64(publishedDelta), map[string]string{"result": "ok"}),
		selfMetric(base, "observability_buffer_items", "gauge", float64(stats.BufferedEvents), nil),
		selfMetric(base, "observability_spool_batches", "gauge", float64(stats.SpoolFiles), nil),
		selfMetric(base, "observability_batch_rows", "gauge", float64(batchRows), nil),
		selfMetric(base, "observability_batch_bytes", "gauge", float64(batchBytes), nil),
	}
	if !lastPublishedAt.IsZero() {
		events = append(events, selfMetric(base, "observability_last_published_timestamp", "gauge", float64(lastPublishedAt.Unix()), nil))
	}
	events = append(events, selfMetric(base, "observability_nfs_available", "gauge", boolGauge(p.Healthy() == nil), nil))
	return events
}

func selfMetric(base Event, name, kind string, value float64, labels map[string]string) Event {
	event := base
	event.EventID = randomID()
	event.MetricName = name
	event.MetricKind = kind
	event.Value = &value
	event.Labels = labels
	return event
}

func boolGauge(ok bool) float64 {
	if ok {
		return 1
	}
	return 0
}

// EmitSelfMetrics buffers the pipeline's own metrics for the next Parquet
// flush. Failures are intentionally ignored here so self-observation never
// feeds back into the failure counters it is reporting.
func (p *Publisher) EmitSelfMetrics(ctx context.Context) {
	_, _ = p.AddBatch(ctx, p.SelfMetricEvents(time.Now()))
}

// Run flushes buffered events on the configured interval until ctx is done.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.config.FlushInterval)
	defer ticker.Stop()
	lastFlush := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Republish spooled batches first so NFS recovery is not delayed
			// until the next process start.
			if err := p.Recover(ctx); err != nil {
				p.dropped.Add(1)
			}
			if p.shouldFlush(time.Since(lastFlush)) {
				if err := p.FlushAll(ctx); err != nil {
					p.dropped.Add(1)
				}
				lastFlush = time.Now()
			}
			p.EmitSelfMetrics(ctx)
		}
	}
}

// shouldFlush reports whether buffered events are large enough to justify a
// Parquet batch, or old enough that waiting longer would delay diagnostics
// beyond MaxFlushInterval.
func (p *Publisher) shouldFlush(age time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if age >= p.config.MaxFlushInterval {
		return true
	}
	for _, kind := range []Kind{KindLogs, KindMetrics} {
		if len(p.buffer[kind]) >= p.config.MinBatchRows || estimateBytes(p.buffer[kind]) >= p.config.MinBatchBytes {
			return true
		}
	}
	return false
}

// Healthy verifies that the local spool remains writable, which is the only
// hard local dependency of the observability pipeline.
func (p *Publisher) Healthy() error {
	for _, root := range []string{p.config.SpoolPath, p.config.DatasetRoot} {
		probe := filepath.Join(root, ".probe-"+randomID())
		file, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(probe)
			return err
		}
		if err := os.Remove(probe); err != nil {
			return err
		}
	}
	return nil
}

// publish writes one immutable part file per partition and deletes the spool
// only after an atomic rename.
func (p *Publisher) publish(ctx context.Context, kind Kind, events []Event) error {
	groups := make(map[Partition][]Event)
	for _, event := range events {
		partition := PartitionFor(event.EventTime, p.config.NodeID)
		groups[partition] = append(groups[partition], event)
	}
	partitions := make([]Partition, 0, len(groups))
	for partition := range groups {
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(i, j int) bool {
		if partitions[i].Date != partitions[j].Date {
			return partitions[i].Date < partitions[j].Date
		}
		if partitions[i].Hour != partitions[j].Hour {
			return partitions[i].Hour < partitions[j].Hour
		}
		return partitions[i].NodeID < partitions[j].NodeID
	})
	for _, partition := range partitions {
		if err := p.publishPartition(ctx, kind, partition, groups[partition], ""); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) publishPartition(ctx context.Context, kind Kind, partition Partition, events []Event, batchID string) error {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	if batchID == "" {
		p.mu.Lock()
		p.sequence++
		batchID = BatchID(p.config.NodeID, p.config.BootID, p.sequence)
		p.mu.Unlock()
	}

	finalPath := partition.FinalPath(p.config.DatasetRoot, kind, batchID)
	tempPath := partition.TempPath(p.config.DatasetRoot, kind, batchID)
	spoolPath := filepath.Join(p.config.SpoolPath, string(kind)+"-"+batchID+".json")
	record := BatchRecord{
		BatchID:   batchID,
		Kind:      kind,
		Partition: partition,
		Rows:      len(events),
		FinalPath: finalPath,
		SpoolPath: spoolPath,
		SealedAt:  time.Now().UTC(),
	}
	if info, err := os.Stat(p.config.DatasetRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("dataset root is not a directory")
		}
		return fmt.Errorf("dataset root unavailable: %w", err)
	}
	if err := p.persistSpool(record, events); err != nil {
		p.dropped.Add(uint64(len(events)))
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(tempPath)
	if _, err := p.writer.WriteParquet(ctx, kind, tempPath, events); err != nil {
		return fmt.Errorf("write parquet batch %s: %w", batchID, err)
	}
	if _, err := os.Stat(finalPath); err == nil {
		// Already published (idempotent retry): treat as committed.
		_ = os.Remove(tempPath)
		_ = os.Remove(spoolPath)
		p.removeSpool(spoolPath)
		return nil
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("publish parquet batch %s: %w", batchID, err)
	}
	p.published.Add(uint64(len(events)))
	p.mu.Lock()
	p.lastBatchRows = uint64(len(events))
	p.lastBatchBytes = uint64(estimateBytes(events))
	p.lastPublishedAt = time.Now().UTC()
	p.mu.Unlock()
	_ = os.Remove(spoolPath)
	p.removeSpool(spoolPath)
	return nil
}

func (p *Publisher) removeSpool(path string) {
	p.mu.Lock()
	delete(p.spool, path)
	p.mu.Unlock()
}

func (p *Publisher) persistSpool(record BatchRecord, events []Event) error {
	data, err := json.Marshal(spoolFile{Record: record, Events: events})
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	total := int64(len(data))
	for _, size := range p.spool {
		total += size
	}
	if total > p.config.MaxSpoolBytes {
		return fmt.Errorf("observability spool quota exceeded: %d bytes", total)
	}
	file, err := os.OpenFile(record.SpoolPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	p.spool[record.SpoolPath] = int64(len(data))
	return nil
}

func (p *Publisher) scanSpool() error {
	entries, err := os.ReadDir(p.config.SpoolPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		p.spool[filepath.Join(p.config.SpoolPath, entry.Name())] = info.Size()
	}
	return nil
}

// Recover replays spooled batches after a crash. Batches whose final file
// already exists are treated as committed and their spool entries removed.
func (p *Publisher) Recover(ctx context.Context) error {
	p.mu.Lock()
	paths := make([]string, 0, len(p.spool))
	for path := range p.spool {
		paths = append(paths, path)
	}
	p.mu.Unlock()
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				p.removeSpool(path)
				continue
			}
			return err
		}
		var file spoolFile
		if err := json.Unmarshal(data, &file); err != nil {
			quarantine := filepath.Join(p.config.SpoolPath, "quarantine")
			if mkdirErr := os.MkdirAll(quarantine, 0o700); mkdirErr != nil {
				return mkdirErr
			}
			if renameErr := os.Rename(path, filepath.Join(quarantine, filepath.Base(path)+".corrupt")); renameErr != nil {
				return renameErr
			}
			p.removeSpool(path)
			continue
		}
		if file.Record.BatchID == "" || (file.Record.Kind != KindLogs && file.Record.Kind != KindMetrics) ||
			!validPartition(file.Record.Partition) ||
			file.Record.FinalPath != file.Record.Partition.FinalPath(p.config.DatasetRoot, file.Record.Kind, file.Record.BatchID) ||
			filepath.Clean(file.Record.SpoolPath) != filepath.Clean(path) {
			quarantine := filepath.Join(p.config.SpoolPath, "quarantine")
			if mkdirErr := os.MkdirAll(quarantine, 0o700); mkdirErr != nil {
				return mkdirErr
			}
			if renameErr := os.Rename(path, filepath.Join(quarantine, filepath.Base(path)+".invalid")); renameErr != nil {
				return renameErr
			}
			p.removeSpool(path)
			continue
		}
		if _, err := os.Stat(file.Record.FinalPath); err == nil {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
			p.removeSpool(path)
			p.recovered.Add(uint64(len(file.Events)))
			continue
		}
		if err := p.publishPartition(ctx, file.Record.Kind, file.Record.Partition, file.Events, file.Record.BatchID); err != nil {
			return err
		}
		p.removeSpool(path)
		p.recovered.Add(uint64(len(file.Events)))
	}
	return nil
}

func validPartition(partition Partition) bool {
	if len(partition.Date) != len("2006-01-02") || len(partition.Hour) != 2 || strings.ContainsAny(partition.NodeID, `/\\`) {
		return false
	}
	if _, err := time.Parse("2006-01-02", partition.Date); err != nil {
		return false
	}
	hour, err := strconv.Atoi(partition.Hour)
	return err == nil && hour >= 0 && hour <= 23
}

type spoolFile struct {
	Record BatchRecord `json:"record"`
	Events []Event     `json:"events"`
}

func estimateBytes(events []Event) int64 {
	total := int64(0)
	for _, event := range events {
		total += int64(len(event.Message) + len(event.TraceID) + len(event.MetricName))
		for key, value := range event.Labels {
			total += int64(len(key) + len(value))
		}
		for key, value := range event.Fields {
			total += int64(len(key) + len(value))
		}
	}
	return total
}
