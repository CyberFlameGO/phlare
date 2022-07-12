package firedb

import (
	"sync"

	profilev1 "github.com/grafana/fire/pkg/gen/google/v1"
)

type labelCache struct {
	labels map[labelKey]*profilev1.Label
	rw     sync.RWMutex
}

func (lc *labelCache) init() {
	lc.labels = make(map[labelKey]*profilev1.Label)
}

func (lc *labelCache) rewriteLabels(t stringConversionTable, in []*profilev1.Label) []*profilev1.Label {
	out := make([]*profilev1.Label, len(in))
	lc.rw.RLock()
	defer lc.rw.RUnlock()
	for i, l := range in {
		k := labelKey{
			Key:     t[l.Key],
			NumUnit: t[l.NumUnit],
			Str:     t[l.Str],
			Num:     l.Num,
		}
		l, ok := lc.labels[k]
		if ok {
			out[i] = l
			continue
		}
		lc.rw.RUnlock()
		lc.rw.Lock()
		l, ok = lc.labels[k]
		if !ok {
			l = &profilev1.Label{
				Key:     k.Key,
				NumUnit: k.NumUnit,
				Str:     k.Str,
				Num:     k.Num,
			}
			lc.labels[k] = l
			out[i] = l
			lc.rw.Unlock()
			lc.rw.RLock()
			continue
		}
		lc.rw.Unlock()
		lc.rw.RLock()
		out[i] = l
	}
	return out
}

type labelKey struct {
	Key     int64
	Str     int64
	Num     int64
	NumUnit int64
}
