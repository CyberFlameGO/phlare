package querier

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/go-kit/log"
	"github.com/grafana/dskit/ring"
	"github.com/grafana/dskit/ring/client"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/grafana/fire/pkg/gen/common/v1"
	ingestv1 "github.com/grafana/fire/pkg/gen/ingester/v1"
	querierv1 "github.com/grafana/fire/pkg/gen/querier/v1"
	"github.com/grafana/fire/pkg/ingester/clientpool"
	"github.com/grafana/fire/pkg/testutil"
)

func Test_QuerySampleType(t *testing.T) {
	querier, err := New(Config{
		PoolConfig: clientpool.PoolConfig{ClientCleanupPeriod: 1 * time.Millisecond},
	}, testutil.NewMockRing([]ring.InstanceDesc{
		{Addr: "1"},
		{Addr: "2"},
		{Addr: "3"},
	}, 3), func(addr string) (client.PoolClient, error) {
		q := newFakeQuerier()
		switch addr {
		case "1":
			q.On("ProfileTypes", mock.Anything, mock.Anything).
				Return(connect.NewResponse(&ingestv1.ProfileTypesResponse{
					ProfileTypes: []*commonv1.ProfileType{
						{ID: "foo"},
						{ID: "bar"},
					},
				}), nil)
		case "2":
			q.On("ProfileTypes", mock.Anything, mock.Anything).
				Return(connect.NewResponse(&ingestv1.ProfileTypesResponse{
					ProfileTypes: []*commonv1.ProfileType{
						{ID: "bar"},
						{ID: "buzz"},
					},
				}), nil)
		case "3":
			q.On("ProfileTypes", mock.Anything, mock.Anything).
				Return(connect.NewResponse(&ingestv1.ProfileTypesResponse{
					ProfileTypes: []*commonv1.ProfileType{
						{ID: "buzz"},
						{ID: "foo"},
					},
				}), nil)
		}
		return q, nil
	}, log.NewLogfmtLogger(os.Stdout))

	require.NoError(t, err)
	out, err := querier.ProfileTypes(context.Background(), connect.NewRequest(&querierv1.ProfileTypesRequest{}))
	ids := make([]string, 0, len(out.Msg.ProfileTypes))
	for _, pt := range out.Msg.ProfileTypes {
		ids = append(ids, pt.ID)
	}
	require.NoError(t, err)
	require.Equal(t, []string{"bar", "buzz", "foo"}, ids)
}

func Test_QueryLabelValues(t *testing.T) {
	req := connect.NewRequest(&ingestv1.LabelValuesRequest{Name: "foo"})
	querier, err := New(Config{
		PoolConfig: clientpool.PoolConfig{ClientCleanupPeriod: 1 * time.Millisecond},
	}, testutil.NewMockRing([]ring.InstanceDesc{
		{Addr: "1"},
		{Addr: "2"},
		{Addr: "3"},
	}, 3), func(addr string) (client.PoolClient, error) {
		q := newFakeQuerier()
		switch addr {
		case "1":
			q.On("LabelValues", mock.Anything, req).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"foo", "bar"}}), nil)
		case "2":
			q.On("LabelValues", mock.Anything, req).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"bar", "buzz"}}), nil)
		case "3":
			q.On("LabelValues", mock.Anything, req).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"buzz", "foo"}}), nil)
		}
		return q, nil
	}, log.NewLogfmtLogger(os.Stdout))

	require.NoError(t, err)
	out, err := querier.LabelValues(context.Background(), req.Msg.Name)
	require.NoError(t, err)
	require.Equal(t, []string{"bar", "buzz", "foo"}, out)
}

// func Test_selectMerge(t *testing.T) {
// 	req := connect.NewRequest(&ingestv1.SelectProfilesRequest{
// 		LabelSelector: `{app="foo"}`,
// 		Type: &commonv1.ProfileType{
// 			Name:       "memory",
// 			SampleType: "inuse_space",
// 			SampleUnit: "bytes",
// 			PeriodType: "space",
// 			PeriodUnit: "bytes",
// 		},
// 		Start: 0,
// 		End:   2,
// 	})
// 	names := []string{"foo", "bar", "buzz"}
// 	p1, p2, p3 := &ingestv1.Profile{
// 		ID:        "1",
// 		Type:      req.Msg.Type,
// 		Labels:    []*commonv1.LabelPair{{Name: "app", Value: "foo"}},
// 		Timestamp: 1,
// 		Stacktraces: []*ingestv1.StacktraceSample{
// 			{FunctionIds: []int32{1}, Value: 1},
// 		},
// 	}, &ingestv1.Profile{
// 		ID:        "2",
// 		Type:      req.Msg.Type,
// 		Labels:    []*commonv1.LabelPair{{Name: "app", Value: "bar"}},
// 		Timestamp: 2,
// 		Stacktraces: []*ingestv1.StacktraceSample{
// 			{FunctionIds: []int32{2}, Value: 1},
// 		},
// 	},
// 		&ingestv1.Profile{
// 			ID:        "3",
// 			Type:      req.Msg.Type,
// 			Labels:    []*commonv1.LabelPair{{Name: "app", Value: "fuzz"}},
// 			Timestamp: 3,
// 			Stacktraces: []*ingestv1.StacktraceSample{
// 				{FunctionIds: []int32{0}, Value: 1},
// 			},
// 		}

// 	querier, err := New(Config{
// 		PoolConfig: clientpool.PoolConfig{ClientCleanupPeriod: 1 * time.Millisecond},
// 	}, testutil.NewMockRing([]ring.InstanceDesc{
// 		{Addr: "1"},
// 		{Addr: "2"},
// 		{Addr: "3"},
// 	}, 1), func(addr string) (client.PoolClient, error) {
// 		q := newFakeQuerier()
// 		switch addr {
// 		case "1":
// 			q.On("SelectProfiles", mock.Anything, req).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
// 				Profiles: []*ingestv1.Profile{
// 					p1, p2, p3,
// 				},
// 				FunctionNames: names,
// 			}), nil)
// 		case "2":
// 			q.On("SelectProfiles", mock.Anything, req).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
// 				Profiles: []*ingestv1.Profile{
// 					p1, p2,
// 				},
// 				FunctionNames: names,
// 			}), nil)

// 		case "3":
// 			q.On("SelectProfiles", mock.Anything, req).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
// 				Profiles: []*ingestv1.Profile{
// 					p2, p3,
// 				},
// 				FunctionNames: names,
// 			}), nil)
// 		}
// 		return q, nil
// 	}, log.NewLogfmtLogger(os.Stdout))
// 	require.NoError(t, err)
// 	flame, err := querier.selectMerge(context.Background(), req.Msg)
// 	require.NoError(t, err)

// 	// todo(cyriltovena): comparing flameGraph is complicated because it's not deterministic. We should investigate where this is coming from.
// 	require.Equal(t, flamebearer.FlamebearerMetadataV1{
// 		Format:     "single",
// 		Units:      "bytes",
// 		Name:       "inuse_space",
// 		SampleRate: 100,
// 	}, flame.FlamebearerProfileV1.Metadata)

// 	sort.Strings(flame.Flamebearer.Names)
// 	require.Equal(t, []string{"bar", "buzz", "foo", "total"}, flame.Flamebearer.Names)
// 	require.Equal(t, []int{0, 3, 0, 0}, flame.Flamebearer.Levels[0])
// 	require.Equal(t, 3, flame.FlamebearerProfileV1.Flamebearer.NumTicks)
// 	require.Equal(t, 1, flame.FlamebearerProfileV1.Flamebearer.MaxSelf)
// }

var _ IngesterQueryClient = &fakeQuerierIngester{}

type fakeQuerierIngester struct {
	mock.Mock
	testutil.FakePoolClient
}

func newFakeQuerier() *fakeQuerierIngester {
	return &fakeQuerierIngester{}
}

func (f *fakeQuerierIngester) LabelValues(ctx context.Context, req *connect.Request[ingestv1.LabelValuesRequest]) (*connect.Response[ingestv1.LabelValuesResponse], error) {
	var (
		args = f.Called(ctx, req)
		res  *connect.Response[ingestv1.LabelValuesResponse]
		err  error
	)
	if args[0] != nil {
		res = args[0].(*connect.Response[ingestv1.LabelValuesResponse])
	}
	if args[1] != nil {
		err = args.Get(1).(error)
	}
	return res, err
}

func (f *fakeQuerierIngester) ProfileTypes(ctx context.Context, req *connect.Request[ingestv1.ProfileTypesRequest]) (*connect.Response[ingestv1.ProfileTypesResponse], error) {
	var (
		args = f.Called(ctx, req)
		res  *connect.Response[ingestv1.ProfileTypesResponse]
		err  error
	)
	if args[0] != nil {
		res = args[0].(*connect.Response[ingestv1.ProfileTypesResponse])
	}
	if args[1] != nil {
		err = args.Get(1).(error)
	}

	return res, err
}

func (f *fakeQuerierIngester) SelectProfiles(ctx context.Context, req *connect.Request[ingestv1.SelectProfilesRequest]) (*connect.ServerStreamForClient[ingestv1.SelectProfilesResponse], error) {
	args := f.Called(ctx, req)
	return args.Get(0).(*connect.ServerStreamForClient[ingestv1.SelectProfilesResponse]), args.Get(1).(error)
}

func (f *fakeQuerierIngester) SelectStacktraceSamples(ctx context.Context) *connect.ClientStreamForClient[ingestv1.SelectStacktraceSamplesRequest, ingestv1.SelectStacktraceSamplesResponse] {
	args := f.Called(ctx)
	return args.Get(0).(*connect.ClientStreamForClient[ingestv1.SelectStacktraceSamplesRequest, ingestv1.SelectStacktraceSamplesResponse])
}
