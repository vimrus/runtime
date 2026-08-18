package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	DatasetRoot   string
	SpoolPath     string
	NodeID        string
	ClusterID     string
	BootID        string
	MaxSpoolBytes int64
	MaxBatchRows  int
	MaxBatchBytes int64
	FlushInterval time.Duration
}

type Publisher struct {
	config Config
	writer ParquetWriter

	mu       sync.Mutex
	buffer   map[Kind][]Event
	sequence uint64
	spool    map[string]int64 // spool file -> bytes
	dropped  atomic.Uint64
}

func NewPublisher(config Config, writer ParquetWriter) (*Publisher, error) {
	if config.DatasetRoot == "" || config.SpoolPath == "" || config.NodeID == "" || config.ClusterID == "" || config.BootID == "" {
		return nil, errors.New("datasetRoot, spoolPath, nodeID, clusterID and bootID are required")
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
	for _, path := range []string{config.DatasetRoot, config.SpoolPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
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
	if needsFlush {
		return p.Flush(ctx, kind)
	}
	return nil
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

// Run flushes buffered events on the configured interval until ctx is done.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.config.FlushInterval)
	defer ticker.Stop()
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
			if err := p.FlushAll(ctx); err != nil {
				p.dropped.Add(1)
			}
		}
	}
}

// Healthy verifies that the local spool remains writable, which is the only
// hard local dependency of the observability pipeline.
func (p *Publisher) Healthy() error {
	probe := filepath.Join(p.config.SpoolPath, ".probe-"+randomID())
	file, err := os.Create(probe)
	if err != nil {
		return err
	}
	_ = file.Close()
	_ = os.Remove(probe)
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
		return nil
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("publish parquet batch %s: %w", batchID, err)
	}
	_ = os.Remove(spoolPath)
	return nil
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
	file, err := os.OpenFile(record.SpoolPath, os.O_CREATE|os.O_WRONLY, 0o600)
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
				continue
			}
			return err
		}
		var file spoolFile
		if err := json.Unmarshal(data, &file); err != nil {
			continue
		}
		if _, err := os.Stat(file.Record.FinalPath); err == nil {
			_ = os.Remove(path)
			p.mu.Lock()
			delete(p.spool, path)
			p.mu.Unlock()
			continue
		}
		if err := p.publishPartition(ctx, file.Record.Kind, file.Record.Partition, file.Events, file.Record.BatchID); err != nil {
			return err
		}
		p.mu.Lock()
		delete(p.spool, path)
		p.mu.Unlock()
	}
	return nil
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
