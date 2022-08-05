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
	firemodel "github.com/grafana/fire/pkg/model"
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
	req := connect.NewRequest(&querierv1.LabelValuesRequest{Name: "foo"})
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
			q.On("LabelValues", mock.Anything, mock.Anything).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"foo", "bar"}}), nil)
		case "2":
			q.On("LabelValues", mock.Anything, mock.Anything).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"bar", "buzz"}}), nil)
		case "3":
			q.On("LabelValues", mock.Anything, mock.Anything).Return(connect.NewResponse(&ingestv1.LabelValuesResponse{Names: []string{"buzz", "foo"}}), nil)
		}
		return q, nil
	}, log.NewLogfmtLogger(os.Stdout))

	require.NoError(t, err)
	out, err := querier.LabelValues(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, []string{"bar", "buzz", "foo"}, out.Msg.Names)
}

func Test_Series(t *testing.T) {
	foobarlabels := firemodel.NewLabelsBuilder().Set("foo", "bar")
	foobuzzlabels := firemodel.NewLabelsBuilder().Set("foo", "buzz")
	req := connect.NewRequest(&querierv1.SeriesRequest{Matchers: []string{`{foo="bar"}`}})
	ingesterReponse := connect.NewResponse(&ingestv1.SeriesResponse{LabelsSet: []*commonv1.Labels{
		{Labels: foobarlabels.Labels()},
		{Labels: foobuzzlabels.Labels()},
	}})
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
			q.On("Series", mock.Anything, mock.Anything).Return(ingesterReponse, nil)
		case "2":
			q.On("Series", mock.Anything, mock.Anything).Return(ingesterReponse, nil)
		case "3":
			q.On("Series", mock.Anything, mock.Anything).Return(ingesterReponse, nil)
		}
		return q, nil
	}, log.NewLogfmtLogger(os.Stdout))

	require.NoError(t, err)
	out, err := querier.Series(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, []*commonv1.Labels{
		{Labels: foobarlabels.Labels()},
		{Labels: foobuzzlabels.Labels()},
	}, out.Msg.LabelsSet)
}

func Test_SelectMergeStacktraces(t *testing.T) {
	// req := connect.NewRequest(&querierv1.SelectMergeStacktracesRequest{
	// 	LabelSelector: `{app="foo"}`,
	// 	ProfileTypeID: "memory:inuse_space:bytes:space:byte",
	// 	Start:         0,
	// 	End:           2,
	// })
	// profileType, err := firemodel.ParseProfileTypeSelector(req.Msg.ProfileTypeID)
	// require.NoError(t, err)
	// names := []string{"foo", "bar", "buzz"}
	// p1, p2, p3 := &ingestv1.Profile{
	// 	ID:        "1",
	// 	Type:      profileType,
	// 	Labels:    []*commonv1.LabelPair{{Name: "app", Value: "foo"}},
	// 	Timestamp: 1,
	// 	Stacktraces: []*ingestv1.StacktraceSample{
	// 		{FunctionIds: []int32{1}, Value: 1},
	// 	},
	// }, &ingestv1.Profile{
	// 	ID:        "2",
	// 	Type:      profileType,
	// 	Labels:    []*commonv1.LabelPair{{Name: "app", Value: "bar"}},
	// 	Timestamp: 2,
	// 	Stacktraces: []*ingestv1.StacktraceSample{
	// 		{FunctionIds: []int32{2}, Value: 1},
	// 	},
	// },
	// 	&ingestv1.Profile{
	// 		ID:        "3",
	// 		Type:      profileType,
	// 		Labels:    []*commonv1.LabelPair{{Name: "app", Value: "fuzz"}},
	// 		Timestamp: 3,
	// 		Stacktraces: []*ingestv1.StacktraceSample{
	// 			{FunctionIds: []int32{0}, Value: 1},
	// 		},
	// 	}

	// querier, err := New(Config{
	// 	PoolConfig: clientpool.PoolConfig{ClientCleanupPeriod: 1 * time.Millisecond},
	// }, testutil.NewMockRing([]ring.InstanceDesc{
	// 	{Addr: "1"},
	// 	{Addr: "2"},
	// 	{Addr: "3"},
	// }, 1), func(addr string) (client.PoolClient, error) {
	// 	q := newFakeQuerier()
	// 	switch addr {
	// 	case "1":
	// 		q.On("SelectProfiles", mock.Anything, mock.Anything).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
	// 			Profiles: []*ingestv1.Profile{
	// 				p1, p2, p3,
	// 			},
	// 			FunctionNames: names,
	// 		}), nil)
	// 	case "2":
	// 		q.On("SelectProfiles", mock.Anything, mock.Anything).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
	// 			Profiles: []*ingestv1.Profile{
	// 				p1, p2,
	// 			},
	// 			FunctionNames: names,
	// 		}), nil)

	// 	case "3":
	// 		q.On("SelectProfiles", mock.Anything, mock.Anything).Once().Return(connect.NewResponse(&ingestv1.SelectProfilesResponse{
	// 			Profiles: []*ingestv1.Profile{
	// 				p2, p3,
	// 			},
	// 			FunctionNames: names,
	// 		}), nil)
	// 	}
	// 	return q, nil
	// }, log.NewLogfmtLogger(os.Stdout))
	// require.NoError(t, err)
	// flame, err := querier.SelectMergeStacktraces(context.Background(), req)
	// require.NoError(t, err)

	// sort.Strings(flame.Msg.Flamegraph.Names)
	// require.Equal(t, []string{"bar", "buzz", "foo", "total"}, flame.Msg.Flamegraph.Names)
	// require.Equal(t, []int64{0, 3, 0, 0}, flame.Msg.Flamegraph.Levels[0].Values)
	// require.Equal(t, int64(3), flame.Msg.Flamegraph.Total)
	// require.Equal(t, int64(1), flame.Msg.Flamegraph.MaxSelf)
}

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

func (f *fakeQuerierIngester) Series(ctx context.Context, req *connect.Request[ingestv1.SeriesRequest]) (*connect.Response[ingestv1.SeriesResponse], error) {
	var (
		args = f.Called(ctx, req)
		res  *connect.Response[ingestv1.SeriesResponse]
		err  error
	)
	if args[0] != nil {
		res = args[0].(*connect.Response[ingestv1.SeriesResponse])
	}
	if args[1] != nil {
		err = args.Get(1).(error)
	}

	return res, err
}
