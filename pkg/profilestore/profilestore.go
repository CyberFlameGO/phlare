package profilestore

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/bufbuild/connect-go"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gogo/status"
	multierror "github.com/hashicorp/go-multierror"
	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	metastorepb "github.com/parca-dev/parca/gen/proto/go/parca/metastore/v1alpha1"
	parcametastore "github.com/parca-dev/parca/pkg/metastore"
	"github.com/parca-dev/parca/pkg/parcacol"
	"github.com/polarsignals/frostdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/thanos-io/objstore/filesystem"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"

	pushv1 "github.com/grafana/fire/pkg/gen/push/v1"
	"github.com/grafana/fire/pkg/metastore"
)

type ProfileStore struct {
	logger log.Logger
	tracer trace.Tracer

	metastore metastorepb.MetastoreServiceClient
	col       *frostdb.ColumnStore
	table     *frostdb.Table
}

type Config struct {
	// TODO: Reassemble to match Mimir/Loki/Tempo
	DataPath         string `json:"data_path"`
	ActiveMemorySize int64  `json:"active_memory_size"` // the active uncompressed memory used by the profile store
}

func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	f.StringVar(&cfg.DataPath, "profile-store.data-path", "./data", "Storage path of profile-store")
	f.Int64Var(&cfg.ActiveMemorySize, "profile-store.active-memory-size", 128*1024*1024, "Active memory size of the profile store")
}

func New(logger log.Logger, reg prometheus.Registerer, tracerProvider trace.TracerProvider, cfg *Config) (*ProfileStore, error) {
	var (
		granuleSize = 8 * 1024
	)

	var metaDataPath, profileDataPath string

	if cfg != nil && cfg.DataPath != "" {
		level.Info(logger).Log("msg", "initializing persistent profile-store", "data-path", cfg.DataPath)
		metaDataPath = filepath.Join(cfg.DataPath, "metastore-v1")
		profileDataPath = filepath.Join(cfg.DataPath, "profilestore-v1")
		if err := os.MkdirAll(metaDataPath, 0o755); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(profileDataPath, 0o755); err != nil {
			return nil, err
		}
	}

	// initialize metastore
	ms, err := metastore.NewBadgerMetastore(
		logger,
		reg,
		tracerProvider.Tracer("badger"),
		metaDataPath,
	)
	if err != nil {
		level.Error(logger).Log("msg", "failed to create badger metastore", "err", err)
		return nil, err
	}

	col := frostdb.New(
		reg,
		granuleSize,
		cfg.ActiveMemorySize,
	)
	if profileDataPath != "" {
		bucket, err := filesystem.NewBucket(profileDataPath)
		if err != nil {
			return nil, err
		}
		col.WithStorageBucket(bucket)
	}

	colDB, err := col.DB("fire")
	if err != nil {
		level.Error(logger).Log("msg", "failed to load database", "err", err)
		return nil, err
	}

	table, err := colDB.Table("stacktraces", frostdb.NewTableConfig(
		parcacol.Schema(),
	), logger)
	if err != nil {
		level.Error(logger).Log("msg", "create table", "err", err)
		return nil, err
	}

	return &ProfileStore{
		logger:    logger,
		tracer:    tracerProvider.Tracer("profilestore"),
		col:       col,
		table:     table,
		metastore: parcametastore.NewInProcessClient(ms),
	}, nil
}

func (ps *ProfileStore) Close() error {
	ps.table.Sync()

	var result error

	if err := ps.col.Close(); err != nil {
		result = multierror.Append(result, err)
	}

	return result
}

func (ps *ProfileStore) Ingest(ctx context.Context, req *connect.Request[pushv1.PushRequest]) error {

	ingester := parcacol.NewIngester(ps.logger, parcacol.NewNormalizer(ps.metastore), ps.table)

	for _, series := range req.Msg.Series {
		ls := make(labels.Labels, 0, len(series.Labels))
		for _, l := range series.Labels {
			if valid := model.LabelName(l.Name).IsValid(); !valid {
				return status.Errorf(codes.InvalidArgument, "invalid label name: %v", l.Name)
			}

			ls = append(ls, labels.Label{
				Name:  l.Name,
				Value: l.Value,
			})
		}

		for _, sample := range series.Samples {
			r, err := gzip.NewReader(bytes.NewBuffer(sample.RawProfile))
			if err != nil {
				return status.Errorf(codes.Internal, "failed to create gzip reader: %v", err)
			}

			content, err := ioutil.ReadAll(r)
			if err != nil {
				return status.Errorf(codes.Internal, "failed to decompress profile: %v", err)
			}

			p := &pprofpb.Profile{}
			if err := p.UnmarshalVT(content); err != nil {
				return status.Errorf(codes.InvalidArgument, "failed to parse profile: %v", err)
			}

			// TODO: Support normalized
			normalized := false
			if err := ingester.Ingest(ctx, ls, p, normalized); err != nil {
				return status.Errorf(codes.Internal, "failed to ingest profile: %v", err)
			}
		}
	}
	return nil
}

func (ps *ProfileStore) TableProvider() *frostdb.DBTableProvider {
	tb, err := ps.col.DB("fire")
	if err != nil {
		panic(err)
	}
	return tb.TableProvider()
}

func (ps *ProfileStore) Metastore() metastorepb.MetastoreServiceClient {
	return ps.metastore
}

func (ps *ProfileStore) Table() *frostdb.Table {
	return ps.table
}
