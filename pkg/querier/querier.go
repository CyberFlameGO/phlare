package querier

import (
	"context"
	"flag"
	"sort"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/go-kit/log"
	"github.com/grafana/dskit/ring"
	ring_client "github.com/grafana/dskit/ring/client"
	"github.com/grafana/dskit/services"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/samber/lo"

	commonv1 "github.com/grafana/fire/pkg/gen/common/v1"
	ingestv1 "github.com/grafana/fire/pkg/gen/ingester/v1"
	querierv1 "github.com/grafana/fire/pkg/gen/querier/v1"
	"github.com/grafana/fire/pkg/ingester/clientpool"
	firemodel "github.com/grafana/fire/pkg/model"
)

// todo: move to non global metrics.
var clients = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "fire",
	Name:      "querier_ingester_clients",
	Help:      "The current number of ingester clients.",
})

type Config struct {
	PoolConfig      clientpool.PoolConfig `yaml:"pool_config,omitempty"`
	ExtraQueryDelay time.Duration         `yaml:"extra_query_delay,omitempty"`
}

// RegisterFlags registers distributor-related flags.
func (cfg *Config) RegisterFlags(fs *flag.FlagSet) {
	cfg.PoolConfig.RegisterFlagsWithPrefix("querier", fs)
	fs.DurationVar(&cfg.ExtraQueryDelay, "querier.extra-query-delay", 0, "Time to wait before sending more than the minimum successful query requests.")
}

type Querier struct {
	services.Service
	subservices        *services.Manager
	subservicesWatcher *services.FailureWatcher

	cfg    Config
	logger log.Logger

	ingestersRing   ring.ReadRing
	pool            *ring_client.Pool
	ingesterQuerier *IngesterQuerier
}

func New(cfg Config, ingestersRing ring.ReadRing, factory ring_client.PoolFactory, logger log.Logger) (*Querier, error) {
	q := &Querier{
		cfg:           cfg,
		logger:        logger,
		ingestersRing: ingestersRing,
		pool:          clientpool.NewPool(cfg.PoolConfig, ingestersRing, factory, clients, logger),
	}
	var err error
	q.subservices, err = services.NewManager(q.pool)
	if err != nil {
		return nil, errors.Wrap(err, "services manager")
	}
	q.subservicesWatcher = services.NewFailureWatcher()
	q.subservicesWatcher.WatchManager(q.subservices)
	q.Service = services.NewBasicService(q.starting, q.running, q.stopping)
	q.ingesterQuerier = NewIngesterQuerier(q.pool, ingestersRing, cfg.ExtraQueryDelay)
	return q, nil
}

func (q *Querier) starting(ctx context.Context) error {
	return services.StartManagerAndAwaitHealthy(ctx, q.subservices)
}

func (q *Querier) running(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case err := <-q.subservicesWatcher.Chan():
		return errors.Wrap(err, "distributor subservice failed")
	}
}

func (q *Querier) stopping(_ error) error {
	return services.StopManagerAndAwaitStopped(context.Background(), q.subservices)
}

func (q *Querier) ProfileTypes(ctx context.Context, req *connect.Request[querierv1.ProfileTypesRequest]) (*connect.Response[querierv1.ProfileTypesResponse], error) {
	responses, err := forAllIngesters(ctx, q.ingesterQuerier, func(ic IngesterQueryClient) ([]*commonv1.ProfileType, error) {
		res, err := ic.ProfileTypes(ctx, connect.NewRequest(&ingestv1.ProfileTypesRequest{}))
		if err != nil {
			return nil, err
		}
		return res.Msg.ProfileTypes, nil
	})
	if err != nil {
		return nil, err
	}
	var profileTypeIDs []string
	profileTypes := make(map[string]*commonv1.ProfileType)
	for _, response := range responses {
		for _, profileType := range response.response {
			if _, ok := profileTypes[profileType.ID]; !ok {
				profileTypeIDs = append(profileTypeIDs, profileType.ID)
				profileTypes[profileType.ID] = profileType
			}
		}
	}
	sort.Strings(profileTypeIDs)
	result := &querierv1.ProfileTypesResponse{
		ProfileTypes: make([]*commonv1.ProfileType, 0, len(profileTypes)),
	}
	for _, id := range profileTypeIDs {
		result.ProfileTypes = append(result.ProfileTypes, profileTypes[id])
	}
	return connect.NewResponse(result), nil
}

func (q *Querier) LabelValues(ctx context.Context, req *connect.Request[querierv1.LabelValuesRequest]) (*connect.Response[querierv1.LabelValuesResponse], error) {
	responses, err := forAllIngesters(ctx, q.ingesterQuerier, func(ic IngesterQueryClient) ([]string, error) {
		res, err := ic.LabelValues(ctx, connect.NewRequest(&ingestv1.LabelValuesRequest{
			Name: req.Msg.Name,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.Names, nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&querierv1.LabelValuesResponse{
		Names: uniqueSortedStrings(responses),
	}), nil
}

func (q *Querier) Series(ctx context.Context, req *connect.Request[querierv1.SeriesRequest]) (*connect.Response[querierv1.SeriesResponse], error) {
	responses, err := forAllIngesters(ctx, q.ingesterQuerier, func(ic IngesterQueryClient) ([]*commonv1.Labels, error) {
		res, err := ic.Series(ctx, connect.NewRequest(&ingestv1.SeriesRequest{
			Matchers: req.Msg.Matchers,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.LabelsSet, nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&querierv1.SeriesResponse{
		LabelsSet: lo.UniqBy(
			lo.FlatMap(responses, func(r responseFromIngesters[[]*commonv1.Labels], _ int) []*commonv1.Labels {
				return r.response
			}),
			func(t *commonv1.Labels) uint64 {
				return firemodel.Labels(t.Labels).Hash()
			}),
	}), nil
}

func (q *Querier) SelectMergeStacktraces(ctx context.Context, req *connect.Request[querierv1.SelectMergeStacktracesRequest]) (*connect.Response[querierv1.SelectMergeStacktracesResponse], error) {
	profileType, err := firemodel.ParseProfileTypeSelector(req.Msg.ProfileTypeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	responses, err := forAllIngesters(ctx, q.ingesterQuerier, func(ic IngesterQueryClient) (*ingestv1.SelectProfilesResponse, error) {
		_, err := ic.SelectProfiles(ctx, connect.NewRequest(&ingestv1.SelectProfilesRequest{
			LabelSelector: req.Msg.LabelSelector,
			Start:         req.Msg.Start,
			End:           req.Msg.End,
			Type:          profileType,
		}))
		if err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&querierv1.SelectMergeStacktracesResponse{
		Flamegraph: NewFlameGraph(newTree(mergeStacktraces(dedupeProfiles(responses)))),
	}), nil
}

func uniqueSortedStrings(responses []responseFromIngesters[[]string]) []string {
	total := 0
	for _, r := range responses {
		total += len(r.response)
	}
	unique := make(map[string]struct{}, total)
	result := make([]string, 0, total)
	for _, r := range responses {
		for _, elem := range r.response {
			if _, ok := unique[elem]; !ok {
				unique[elem] = struct{}{}
				result = append(result, elem)
			}
		}
	}
	sort.Strings(result)
	return result
}
