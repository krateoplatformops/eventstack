package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	xcontext "github.com/krateoplatformops/plumbing/context"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	serviceName = "etcd-cleanup-manager"
)

type CleanupOptions struct {
	Prefix       string
	DesiredRatio float64
	BatchSize    int
	AutoCompact  bool
	AutoDefrag   bool
}

type CleanupManager struct {
	cli          *clientv3.Client
	prefix       string
	desiredRatio float64
	batchSize    int
	autoCompact  bool
	autoDefrag   bool
}

type kvInfo struct {
	Key          string
	CreateRev    int64
	EstimatedLen int64
}

// NewCleanupManager creates a configured CleanupManager instance.
func NewCleanupManager(cli *clientv3.Client, opts CleanupOptions) *CleanupManager {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 50
	}

	return &CleanupManager{
		cli:          cli,
		prefix:       opts.Prefix,
		desiredRatio: opts.DesiredRatio,
		batchSize:    opts.BatchSize,
		autoCompact:  opts.AutoCompact,
		autoDefrag:   opts.AutoDefrag,
	}
}

func (m *CleanupManager) RunCleanup(ctx context.Context, status *clientv3.StatusResponse) {
	log := xcontext.Logger(ctx).
		With(slog.String("service", serviceName))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if status == nil || status.DbSizeQuota == 0 {
		log.Debug("invalid etcd status — skipping cleanup")
		return
	}

	used := status.DbSizeInUse
	quota := status.DbSizeQuota
	needFree := int64(float64(used) - m.desiredRatio*float64(quota))
	if needFree <= 0 {
		log.Debug("no need to free space",
			slog.Int64("used", used), slog.Int64("quota", quota))
		return
	}

	log.Info("starting cleanup",
		slog.Int64("used", used), slog.Int64("quota", quota),
		slog.Float64("target", m.desiredRatio), slog.Int64("needFree", needFree),
	)

	nonComp, comp, err := m.listKeys(ctx)
	if err != nil {
		log.Error("failed to list keys", slog.Any("err", err))
		return
	}

	freed, deleted := m.deleteKeys(ctx, needFree, nonComp, comp)
	log.Info(fmt.Sprintf("finished deletes: %d keys, est. freed=%d bytes", deleted, freed))

	if m.autoCompact {
		m.runCompact(ctx)
	}

	if m.autoDefrag {
		m.runDefrag(ctx)
	}

	log.Info("cleanup sequence complete")
}

// listKeys retrieves and classifies keys by prefix.
func (m *CleanupManager) listKeys(ctx context.Context) (nonComp, comp []kvInfo, err error) {
	resp, err := m.cli.Get(ctx, m.prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, nil, err
	}

	for _, kv := range resp.Kvs {
		key := string(kv.Key)
		relative := strings.TrimPrefix(key, m.prefix)
		info := kvInfo{
			Key:          key,
			CreateRev:    kv.CreateRevision,
			EstimatedLen: int64(len(kv.Key) + len(kv.Value)),
		}

		if strings.Contains(relative, "/comp-") {
			comp = append(comp, info)
		} else {
			nonComp = append(nonComp, info)
		}
	}

	sort.Slice(nonComp, func(i, j int) bool { return nonComp[i].CreateRev < nonComp[j].CreateRev })
	sort.Slice(comp, func(i, j int) bool { return comp[i].CreateRev < comp[j].CreateRev })

	return nonComp, comp, nil
}

// deleteKeys removes keys in order until the estimated bytes freed reach the target.
func (m *CleanupManager) deleteKeys(ctx context.Context, needFree int64, nonComp, comp []kvInfo) (freed int64, deleted int) {
	log := xcontext.Logger(ctx).
		With(slog.String("service", serviceName))

	deleteFromList := func(list []kvInfo) {
		for i := 0; i < len(list) && freed < needFree; i += m.batchSize {
			end := i + m.batchSize
			if end > len(list) {
				end = len(list)
			}
			for _, kv := range list[i:end] {
				if ctx.Err() != nil {
					return
				}
				_, err := m.cli.Delete(ctx, kv.Key)
				if err != nil {
					log.Error("delete failed", slog.Any("err", err))
					continue
				}
				freed += kv.EstimatedLen
				deleted++
				log.Debug(fmt.Sprintf("deleted %s (rev=%d, est=%d bytes)", kv.Key, kv.CreateRev, kv.EstimatedLen))
			}
		}
	}

	deleteFromList(nonComp)
	if freed < needFree {
		deleteFromList(comp)
	}
	return freed, deleted
}

// runCompact performs etcd compaction at the latest revision.
func (m *CleanupManager) runCompact(ctx context.Context) {
	log := xcontext.Logger(ctx).
		With(slog.String("service", serviceName))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	statusResp, err := m.cli.Status(ctx, m.cli.Endpoints()[0])
	if err != nil {
		log.Error("compact: failed to get latest revision", slog.Any("err", err))
		return
	}

	rev := statusResp.Header.Revision
	log.Debug(fmt.Sprintf("compacting at revision %d", rev))
	if _, err := m.cli.Compact(ctx, rev); err != nil {
		log.Error("compact failed", slog.Any("err", err))
		return
	}
	log.Info("compact succeeded")
}

// runDefrag defragments the cluster, skipping if >1 member.
func (m *CleanupManager) runDefrag(ctx context.Context) {
	log := xcontext.Logger(ctx).
		With(slog.String("service", serviceName))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	members, err := m.cli.MemberList(ctx)
	if err != nil {
		log.Error("defrag: failed to list members", slog.Any("err", err))
		return
	}

	if tot := len(members.Members); tot > 1 {
		log.Info("defrag skipped (multi-member cluster)", slog.Int("members", tot))
		return
	}

	for _, ep := range m.cli.Endpoints() {
		log.Debug(fmt.Sprintf("defragging endpoint %s ...", ep))
		if _, err := m.cli.Maintenance.Defragment(ctx, ep); err != nil {
			log.Error(fmt.Sprintf("defrag failed on %s", ep), slog.Any("err", err))
			continue
		}
		log.Debug(fmt.Sprintf("defrag succeeded on %s", ep))
	}
}
