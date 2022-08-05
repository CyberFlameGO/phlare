package querier

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/gogo/status"
	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"google.golang.org/grpc/codes"

	commonv1 "github.com/grafana/fire/pkg/gen/common/v1"
	ingestv1 "github.com/grafana/fire/pkg/gen/ingester/v1"
	querierv1 "github.com/grafana/fire/pkg/gen/querier/v1"
	firemodel "github.com/grafana/fire/pkg/model"
)

var (
	minTime = time.Unix(math.MinInt64/1000+62135596801, 0).UTC()
	maxTime = time.Unix(math.MaxInt64/1000-62135596801, 999999999).UTC()

	minTimeFormatted = minTime.Format(time.RFC3339Nano)
	maxTimeFormatted = maxTime.Format(time.RFC3339Nano)
)

// LabelValuesHandler only returns the label values for the given label name.
// This is mostly for fulfilling the pyroscope API and won't be used in the future.
// /label-values?label=__name__
func (q *Querier) LabelValuesHandler(w http.ResponseWriter, req *http.Request) {
	label := req.URL.Query().Get("label")
	if label == "" {
		http.Error(w, "label parameter is required", http.StatusBadRequest)
		return
	}
	var (
		res []string
		err error
	)

	if label == "__name__" {
		response, err := q.ProfileTypes(req.Context(), connect.NewRequest(&querierv1.ProfileTypesRequest{}))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, t := range response.Msg.ProfileTypes {
			res = append(res, t.ID)
		}
	} else {
		response, err := q.LabelValues(req.Context(), connect.NewRequest(&querierv1.LabelValuesRequest{}))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res = response.Msg.Names
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (q *Querier) RenderHandler(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selectParams, profileType, err := parseSelectProfilesRequest(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := q.SelectMergeStacktraces(req.Context(), connect.NewRequest(selectParams))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ExportToFlamebearer(res.Msg.Flamegraph, profileType)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

type queryData struct {
	ResultType parser.ValueType `json:"resultType"`
	Result     parser.Value     `json:"result"`
}

type prometheusResponse struct {
	Status string    `json:"status"`
	Data   queryData `json:"data"`
}

func (q *Querier) PrometheusQueryRangeHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	start, err := parseTime(r.FormValue("start"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid start: %s", err.Error()), http.StatusBadRequest)
		return
	}
	end, err := parseTime(r.FormValue("end"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid end: %s", err.Error()), http.StatusBadRequest)
		return
	}
	if end.Before(start) {
		http.Error(w, "end timestamp must not be before start time", http.StatusBadRequest)
		return
	}
	selector, ptype, err := parseQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selectReq := connect.NewRequest(&ingestv1.SelectProfilesRequest{
		Start:         int64(model.TimeFromUnixNano(start.UnixNano())),
		End:           int64(model.TimeFromUnixNano(end.UnixNano())),
		LabelSelector: selector,
		Type:          ptype,
	})

	responses, err := forAllIngesters(r.Context(), q.ingesterQuerier, func(ic IngesterQueryClient) (*connect.ServerStreamForClient[ingestv1.SelectProfilesResponse], error) {
		res, err := ic.SelectProfiles(r.Context(), selectReq)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it := NewStreamsProfileIterator(responses)
	series := map[uint64]*promql.Series{}

	for it.Next() {
		var (
			p    = it.At()
			lbs  = p.Labels.WithoutPrivateLabels()
			hash = lbs.Hash()
		)
		point := promql.Point{
			T: int64(model.TimeFromUnixNano(p.Timestamp)),
		}
		point.V += float64(p.TotalValue)

		s, ok := series[hash]
		if !ok {
			series[hash] = &promql.Series{
				Metric: lbs.ToPrometheusLabels(),
				Points: []promql.Point{point},
			}
			continue
		}
		s.Points = append(s.Points, point)
	}
	if err := it.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	matrix := make(promql.Matrix, 0, len(series))

	for _, s := range series {
		matrix = append(matrix, *s)
	}

	result := prometheusResponse{
		Status: "success",
		Data: queryData{
			ResultType: parser.ValueTypeMatrix,
			Result:     matrix,
		},
	}

	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		ns = math.Round(ns*1000) / 1000
		return time.Unix(int64(s), int64(ns*float64(time.Second))).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}

	// Stdlib's time parser can only handle 4 digit years. As a workaround until
	// that is fixed we want to at least support our own boundary times.
	// Context: https://github.com/prometheus/client_golang/issues/614
	// Upstream issue: https://github.com/golang/go/issues/20555
	switch s {
	case minTimeFormatted:
		return minTime, nil
	case maxTimeFormatted:
		return maxTime, nil
	}
	return time.Time{}, errors.Errorf("cannot parse %q to a valid timestamp", s)
}

// render/render?format=json&from=now-12h&until=now&query=pyroscope.server.cpu
func parseSelectProfilesRequest(req *http.Request) (*querierv1.SelectMergeStacktracesRequest, *commonv1.ProfileType, error) {
	selector, ptype, err := parseQuery(req)
	if err != nil {
		return nil, nil, err
	}

	// default start and end to now-1h
	start := model.TimeFromUnixNano(time.Now().Add(-1 * time.Hour).UnixNano())
	end := model.TimeFromUnixNano(time.Now().UnixNano())

	if from := req.Form.Get("from"); from != "" {
		from, err := parseRelativeTime(from)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse from: %w", err)
		}
		start = end.Add(-from)
	}
	return &querierv1.SelectMergeStacktracesRequest{
		Start:         int64(start),
		End:           int64(end),
		LabelSelector: selector,
		ProfileTypeID: ptype.ID,
	}, ptype, nil
}

func parseQuery(req *http.Request) (string, *commonv1.ProfileType, error) {
	q := req.Form.Get("query")
	if q == "" {
		return "", nil, fmt.Errorf("query is required")
	}

	parsedSelector, err := parser.ParseMetricSelector(q)
	if err != nil {
		return "", nil, status.Error(codes.InvalidArgument, "failed to parse query")
	}

	sel := make([]*labels.Matcher, 0, len(parsedSelector))
	var nameLabel *labels.Matcher
	for _, matcher := range parsedSelector {
		if matcher.Name == labels.MetricName {
			nameLabel = matcher
		} else {
			sel = append(sel, matcher)
		}
	}
	if nameLabel == nil {
		return "", nil, status.Error(codes.InvalidArgument, "query must contain a profile-type selection")
	}

	profileSelector, err := firemodel.ParseProfileTypeSelector(nameLabel.Value)
	if err != nil {
		return "", nil, status.Error(codes.InvalidArgument, "failed to parse query")
	}
	return convertMatchersToString(sel), profileSelector, nil
}

func parseRelativeTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "now-")
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func convertMatchersToString(matchers []*labels.Matcher) string {
	out := strings.Builder{}
	out.WriteRune('{')

	for idx, m := range matchers {
		if idx > 0 {
			out.WriteRune(',')
		}

		out.WriteString(m.String())
	}

	out.WriteRune('}')
	return out.String()
}
