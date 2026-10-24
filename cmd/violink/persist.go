// persist.go - values and short history that survive a restart.
//
// Two kinds of data, both saved to one JSON file (data/persist.json):
//
//   values  - save(key, value) / load(key) in scripts. Last known values,
//             counters, calibration offsets... anything a script wants back
//             after a restart.
//   series  - record(key, number) in scripts. Timestamped numbers used by
//             average(), highest(), lowest(), significant() and samples().
//             Only the last persist.maxSeriesMinutes are kept.
//
// This is not a database. It is just enough memory to carry on after a
// restart as if nothing happened: a Reading Steiner for the worldline shift
// that is a reboot.
//
// Keys are stored per script ("scriptname/key"), so two scripts can both use
// a key called "last" without overwriting each other.

package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

type Sample struct {
	T int64   // unix milliseconds
	V float64 // value
}

// Samples are written as [time, value] pairs to keep the file small.
func (s Sample) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]float64{float64(s.T), s.V})
}

func (s *Sample) UnmarshalJSON(b []byte) error {
	var p [2]float64
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	s.T = int64(p[0])
	s.V = p[1]
	return nil
}

type persistFile struct {
	Saved  time.Time              `json:"saved"`
	Values map[string]interface{} `json:"values"`
	Series map[string][]Sample    `json:"series"`
}

var (
	persistLock   sync.Mutex
	persistValues = map[string]interface{}{}
	persistSeries = map[string][]Sample{}
	persistDirty  bool
	persistPath   string
	persistCfg    PersistConfig
	persistStop   = make(chan struct{})
	persistDone   = make(chan struct{})
)

// loadPersist reads the file from the last run. A missing file is normal on
// the first start; a broken file is renamed so it is not lost.
func loadPersist(cfg PersistConfig) {
	persistCfg = cfg
	if persistCfg.MaxSeriesMinutes <= 0 {
		persistCfg.MaxSeriesMinutes = 180
	}
	if persistCfg.MaxSeriesSamples <= 0 {
		persistCfg.MaxSeriesSamples = 100000
	}
	persistPath = resolvePath(cfg.File)

	data, err := os.ReadFile(persistPath)
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("persist: cannot read %s: %v", persistPath, err)
		}
		return
	}
	var f persistFile
	if err := json.Unmarshal(data, &f); err != nil {
		bad := persistPath + ".broken"
		os.Rename(persistPath, bad)
		logError("persist: %s is damaged (%v), moved to %s and starting empty", persistPath, err, bad)
		return
	}
	persistLock.Lock()
	if f.Values != nil {
		persistValues = f.Values
	}
	if f.Series != nil {
		persistSeries = f.Series
	}
	for key := range persistSeries {
		pruneSeries(key, time.Now().UnixMilli())
	}
	persistLock.Unlock()
	logInfo("persist: loaded %d values and %d series (saved %s)", len(f.Values), len(f.Series), f.Saved.Format("2006-01-02 15:04:05"))
}

// runPersist saves changed data at the configured interval until stopPersist.
func runPersist() {
	defer close(persistDone)
	interval := time.Duration(persistCfg.SaveIntervalSec) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-persistStop:
			savePersist()
			return
		case <-t.C:
			savePersist()
		}
	}
}

func stopPersist() {
	close(persistStop)
	<-persistDone
}

func savePersist() {
	persistLock.Lock()
	if !persistDirty {
		persistLock.Unlock()
		return
	}
	data, err := json.Marshal(persistFile{Saved: time.Now(), Values: persistValues, Series: persistSeries})
	persistDirty = false
	persistLock.Unlock()
	if err != nil {
		logError("persist: %v", err)
		return
	}
	if err := writeFileAtomic(persistPath, data); err != nil {
		logError("persist: cannot write %s: %v", persistPath, err)
	}
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

func persistSet(key string, value interface{}) {
	persistLock.Lock()
	persistValues[key] = value
	persistDirty = true
	persistLock.Unlock()
}

func persistGet(key string) (interface{}, bool) {
	persistLock.Lock()
	defer persistLock.Unlock()
	v, ok := persistValues[key]
	return v, ok
}

func persistDelete(key string) {
	persistLock.Lock()
	delete(persistValues, key)
	persistDirty = true
	persistLock.Unlock()
}

// ---------------------------------------------------------------------------
// Series and the aggregate functions
// ---------------------------------------------------------------------------

func seriesAdd(key string, value float64, t time.Time) {
	persistLock.Lock()
	defer persistLock.Unlock()
	ms := t.UnixMilli()
	s := persistSeries[key]
	// Keep the series sorted by time. Values normally arrive in order, so
	// this is just an append; an out-of-order sample is inserted in place.
	if len(s) == 0 || s[len(s)-1].T <= ms {
		s = append(s, Sample{T: ms, V: value})
	} else {
		i := sort.Search(len(s), func(i int) bool { return s[i].T > ms })
		s = append(s, Sample{})
		copy(s[i+1:], s[i:])
		s[i] = Sample{T: ms, V: value}
	}
	persistSeries[key] = s
	pruneSeries(key, ms)
	persistDirty = true
}

// pruneSeries drops samples older than maxSeriesMinutes, and the oldest ones
// when there are more than maxSeriesSamples. Must be called with persistLock held.
func pruneSeries(key string, nowMs int64) {
	s := persistSeries[key]
	oldest := nowMs - int64(persistCfg.MaxSeriesMinutes)*60000
	cut := 0
	for cut < len(s) && s[cut].T < oldest {
		cut++
	}
	if extra := len(s) - cut - persistCfg.MaxSeriesSamples; extra > 0 {
		cut += extra
	}
	if cut > 0 {
		// Re-slicing is enough: the next time append grows the slice, Go
		// copies only the live part and the old memory is freed.
		persistSeries[key] = s[cut:]
	}
	if len(persistSeries[key]) == 0 {
		delete(persistSeries, key)
	}
}

// seriesWindow returns a copy of the values from the last `minutes` minutes.
func seriesWindow(key string, minutes float64) []float64 {
	persistLock.Lock()
	defer persistLock.Unlock()
	s := persistSeries[key]
	from := time.Now().UnixMilli() - int64(minutes*60000)
	i := sort.Search(len(s), func(i int) bool { return s[i].T >= from })
	out := make([]float64, 0, len(s)-i)
	for ; i < len(s); i++ {
		out = append(out, s[i].V)
	}
	return out
}

// The aggregate functions return ok=false when there is no data in the
// window; scripts then get null.

func aggAverage(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals)), true
}

func aggHighest(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	m := math.Inf(-1)
	for _, v := range vals {
		m = math.Max(m, v)
	}
	return m, true
}

func aggLowest(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	m := math.Inf(1)
	for _, v := range vals {
		m = math.Min(m, v)
	}
	return m, true
}

// aggSignificant is the metocean "significant" value: the mean of the highest
// third of the values (H1/3). Record individual wave heights and this gives
// the significant wave height Hs for the window. With fewer than 3 values it
// is simply the highest.
//
// Observe first, conclude later. The heavens did not give up their
// movements to anyone who skipped the measurements either.
func aggSignificant(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted) / 3
	if n < 1 {
		n = 1
	}
	return aggAverage(sorted[len(sorted)-n:])
}
