// Package ingest ships frps access records into SQLite. frps keeps records
// in a bounded in-memory ring; the poller fetches them periodically and
// stores them transactionally, keyed by (instance, seq) so re-polls and
// frps restarts never create duplicates.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"frps-gateway/frps"
	"frps-gateway/store"
)

var ErrInvalidRecordIdentity = errors.New("access record is missing instance or sequence")

const (
	// fetchLimit matches frps's maximum page size for the accesslog API.
	fetchLimit = 5000
	// pruneInterval is how often expired records are removed.
	pruneInterval = time.Hour
)

// Poller periodically pulls the frps access log into the store.
type Poller struct {
	client        *frps.Client
	st            *store.Store
	interval      time.Duration
	retentionDays int // <= 0 keeps records forever
	logger        *slog.Logger
}

func New(client *frps.Client, st *store.Store, interval time.Duration, retentionDays int, logger *slog.Logger) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{
		client:        client,
		st:            st,
		interval:      interval,
		retentionDays: retentionDays,
		logger:        logger,
	}
}

// Run polls until ctx is cancelled, pruning expired records once an hour.
// Polling errors are logged and retried on the next tick; they never stop
// the poller because the in-memory ring on frps keeps overwriting while we
// wait, so a permanent stop would silently lose records.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	pruner := time.NewTicker(pruneInterval)
	defer pruner.Stop()

	p.pollAndLog(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAndLog(ctx)
		case <-pruner.C:
			p.pruneOnce(ctx)
		}
	}
}

func (p *Poller) pollAndLog(ctx context.Context) {
	if err := p.PollOnce(ctx); err != nil {
		p.logger.Error("access log ingest failed", "err", err)
	}
}

// PollOnce fetches the current access log page and stores new records.
func (p *Poller) PollOnce(ctx context.Context) error {
	records, err := p.client.ListAccessLog(ctx, fetchLimit)
	if err != nil {
		return fmt.Errorf("fetch access log: %w", err)
	}
	inserted, err := p.store(ctx, records)
	if err != nil {
		return err
	}
	if inserted > 0 {
		p.logger.Info("ingested access records", "fetched", len(records), "inserted", inserted)
	}
	return nil
}

func (p *Poller) store(ctx context.Context, records []frps.AccessRecord) (int64, error) {
	if len(records) == 0 {
		return 0, nil
	}
	batch := make([]store.AccessRecord, 0, len(records))
	type sequenceRange struct {
		min, max uint64
		count    int
	}
	ranges := make(map[string]sequenceRange)
	for _, rec := range records {
		if rec.Instance == "" || rec.Seq == 0 {
			return 0, fmt.Errorf("%w: instance=%q seq=%d", ErrInvalidRecordIdentity, rec.Instance, rec.Seq)
		}
		rg, ok := ranges[rec.Instance]
		if !ok || rec.Seq < rg.min {
			rg.min = rec.Seq
		}
		if rec.Seq > rg.max {
			rg.max = rec.Seq
		}
		rg.count++
		ranges[rec.Instance] = rg
		batch = append(batch, store.AccessRecord{
			Instance: rec.Instance,
			Seq:      rec.Seq,
			Time:     rec.Time,
			IP:       rec.IP,
			User:     rec.User,
			Source:   rec.Source,
			Action:   rec.Action,
			Reason:   rec.Reason,
		})
	}
	for instance, rg := range ranges {
		previous, exists, err := p.st.MaxAccessSequence(ctx, instance)
		if err != nil {
			return 0, fmt.Errorf("read access log cursor: %w", err)
		}
		expected := uint64(1)
		if exists {
			expected = previous + 1
		}
		if (rg.max >= expected && rg.min > expected) || rg.max-rg.min+1 > uint64(rg.count) {
			p.logger.Warn("access log gap detected", "instance", instance, "expectedSeq", expected, "oldestFetchedSeq", rg.min, "newestFetchedSeq", rg.max)
		}
	}
	inserted, err := p.st.InsertAccessRecords(ctx, batch)
	if err != nil {
		return 0, fmt.Errorf("store access log: %w", err)
	}
	return inserted, nil
}

func (p *Poller) pruneOnce(ctx context.Context) {
	if p.retentionDays <= 0 {
		return
	}
	before := time.Now().AddDate(0, 0, -p.retentionDays)
	removed, err := p.st.PruneAccessRecords(ctx, before)
	if err != nil {
		p.logger.Warn("prune access records failed", "err", err)
		return
	}
	if removed > 0 {
		p.logger.Info("pruned access records", "removed", removed, "olderThanDays", p.retentionDays)
	}
}
