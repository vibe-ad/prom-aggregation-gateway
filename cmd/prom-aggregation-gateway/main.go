package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"sort"
	"sync"

	"github.com/matttproud/golang_protobuf_extensions/pbutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/route"
)

func lablesLessThan(a, b []*dto.LabelPair) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if *a[i].Name != *b[j].Name {
			return *a[i].Name < *b[j].Name
		}
		if *a[i].Value != *b[j].Value {
			return *a[i].Value < *b[j].Value
		}
		i++
		j++
	}
	return len(a) < len(b)
}

type byLabel []*dto.Metric

func (a byLabel) Len() int           { return len(a) }
func (a byLabel) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byLabel) Less(i, j int) bool { return lablesLessThan(a[i].Label, a[j].Label) }

// Sort a slice of LabelPairs by name
type byName []*dto.LabelPair

func (a byName) Len() int           { return len(a) }
func (a byName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byName) Less(i, j int) bool { return a[i].GetName() < a[j].GetName() }

func uint64ptr(a uint64) *uint64 {
	return &a
}

func float64ptr(a float64) *float64 {
	return &a
}

func mergeBuckets(a, b []*dto.Bucket) []*dto.Bucket {
	output := []*dto.Bucket{}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if *a[i].UpperBound < *b[j].UpperBound {
			output = append(output, a[i])
			i++
		} else if *a[i].UpperBound > *b[j].UpperBound {
			output = append(output, b[j])
			j++
		} else {
			output = append(output, &dto.Bucket{
				CumulativeCount: uint64ptr(*a[i].CumulativeCount + *b[j].CumulativeCount),
				UpperBound:      a[i].UpperBound,
			})
			i++
			j++
		}
	}
	for ; i < len(a); i++ {
		output = append(output, a[i])
	}
	for ; j < len(b); j++ {
		output = append(output, b[j])
	}
	return output
}

func mergeMetric(ty dto.MetricType, a, b *dto.Metric) *dto.Metric {
	switch ty {
	case dto.MetricType_COUNTER:
		return &dto.Metric{
			Label: a.Label,
			Counter: &dto.Counter{
				Value: float64ptr(*a.Counter.Value + *b.Counter.Value),
			},
		}

	case dto.MetricType_GAUGE:
		return &dto.Metric{
			Label: a.Label,
			Gauge: &dto.Gauge{
				Value: b.Gauge.Value,
			},
		}

	case dto.MetricType_HISTOGRAM:
		return &dto.Metric{
			Label: a.Label,
			Histogram: &dto.Histogram{
				SampleCount: uint64ptr(*a.Histogram.SampleCount + *b.Histogram.SampleCount),
				SampleSum:   float64ptr(*a.Histogram.SampleSum + *b.Histogram.SampleSum),
				Bucket:      mergeBuckets(a.Histogram.Bucket, b.Histogram.Bucket),
			},
		}

	case dto.MetricType_UNTYPED:
		return &dto.Metric{
			Label: a.Label,
			Untyped: &dto.Untyped{
				Value: float64ptr(*a.Untyped.Value + *b.Untyped.Value),
			},
		}

	default:
		// No way of merging other metrics types, discard.
		return nil
	}
}

func mergeFamily(a, b *dto.MetricFamily) (*dto.MetricFamily, error) {
	if *a.Type != *b.Type {
		return nil, fmt.Errorf("Cannot merge metric '%s': type %s != %s",
			*a.Name, a.Type.String(), b.Type.String())
	}

	output := &dto.MetricFamily{
		Name: a.Name,
		Help: a.Help,
		Type: a.Type,
	}

	i, j := 0, 0
	for i < len(a.Metric) && j < len(b.Metric) {
		if lablesLessThan(a.Metric[i].Label, b.Metric[j].Label) {
			output.Metric = append(output.Metric, a.Metric[i])
			i++
		} else if lablesLessThan(b.Metric[j].Label, a.Metric[i].Label) {
			output.Metric = append(output.Metric, b.Metric[j])
			j++
		} else {
			merged := mergeMetric(*a.Type, a.Metric[i], b.Metric[j])
			if merged != nil {
				output.Metric = append(output.Metric, merged)
			}
			i++
			j++
		}
	}
	for ; i < len(a.Metric); i++ {
		output.Metric = append(output.Metric, a.Metric[i])
	}
	for ; j < len(b.Metric); j++ {
		output.Metric = append(output.Metric, b.Metric[j])
	}
	return output, nil
}

type aggate struct {
	familiesLock sync.RWMutex
	families     map[string]*dto.MetricFamily
}

func newAggate() *aggate {
	return &aggate{
		families: map[string]*dto.MetricFamily{},
	}
}

func validateFamily(f *dto.MetricFamily) error {
	// Map of fingerprints we've seen before in this family
	fingerprints := make(map[model.Fingerprint]struct{}, len(f.Metric))
	for _, m := range f.Metric {
		// Turn protobuf LabelSet into Prometheus model LabelSet
		lset := make(model.LabelSet, len(m.Label)+1)
		for _, p := range m.Label {
			lset[model.LabelName(p.GetName())] = model.LabelValue(p.GetValue())
		}
		lset[model.MetricNameLabel] = model.LabelValue(f.GetName())
		if err := lset.Validate(); err != nil {
			return err
		}
		fingerprint := lset.Fingerprint()
		if _, found := fingerprints[fingerprint]; found {
			return fmt.Errorf("Duplicate labels: %v", lset)
		}
		fingerprints[fingerprint] = struct{}{}
	}
	return nil
}

func (a *aggate) parseAndMerge(r *http.Request) error {
	inFamilies, err := a.parse(r)
	if err != nil {
		return err
	}

	job := route.Param(r.Context(), "job")
	return a.merge(inFamilies, job)
}

func (a *aggate) parse(r *http.Request) (map[string]*dto.MetricFamily, error) {
	var metricFamilies map[string]*dto.MetricFamily
	ctMediatype, ctParams, ctErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ctErr == nil && ctMediatype == "application/vnd.google.protobuf" &&
		ctParams["encoding"] == "delimited" &&
		ctParams["proto"] == "io.prometheus.client.MetricFamily" {
		metricFamilies = map[string]*dto.MetricFamily{}
		var err error
		for {
			mf := &dto.MetricFamily{}
			if _, err = pbutil.ReadDelimited(r.Body, mf); err != nil {
				if err == io.EOF {
					err = nil
				}
				break
			}
			metricFamilies[mf.GetName()] = mf
		}
		return metricFamilies, err
	}
	// We could do further content-type checks here, but the
	// fallback for now will anyway be the text format
	// version 0.0.4, so just go for it and see if it works.
	var parser expfmt.TextParser
	return parser.TextToMetricFamilies(r.Body)
}

func (a *aggate) merge(inFamilies map[string]*dto.MetricFamily, job string) error {
	a.familiesLock.Lock()
	defer a.familiesLock.Unlock()
	for name, family := range inFamilies {
		// Discard unsupported types
		if *family.Type == dto.MetricType_SUMMARY {
			continue
		}
		if err := validateFamily(family); err != nil {
			return err
		}

		// Sort labels in case source sends them inconsistently
		for _, m := range family.Metric {
			if job != "" {
				jobName := "job"
				jobLabel := dto.LabelPair{Name: &jobName, Value: &job}
				m.Label = append(m.Label, &jobLabel)
			}
			sort.Sort(byName(m.Label))
		}

		// family must be sorted for the merge
		sort.Sort(byLabel(family.Metric))

		existingFamily, ok := a.families[name]
		if !ok {
			a.families[name] = family
			continue
		}

		merged, err := mergeFamily(existingFamily, family)
		if err != nil {
			return err
		}

		a.families[name] = merged
	}

	return nil
}

func (a *aggate) handler(w http.ResponseWriter, r *http.Request) {
	contentType := expfmt.Negotiate(r.Header)
	w.Header().Set("Content-Type", string(contentType))
	enc := expfmt.NewEncoder(w, contentType)

	a.familiesLock.RLock()
	defer a.familiesLock.RUnlock()
	metricNames := []string{}
	for name := range a.families {
		metricNames = append(metricNames, name)
	}
	sort.Sort(sort.StringSlice(metricNames))

	for _, name := range metricNames {
		if err := enc.Encode(a.families[name]); err != nil {
			http.Error(w, "An error has occurred during metrics encoding:\n\n"+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// TODO reset gauges
}

func handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"alive": true}`)
}

func main() {
	listen := flag.String("listen", ":80", "Address and port to listen on.")
	cors := flag.String("cors", "*", "The 'Access-Control-Allow-Origin' value to be returned.")
	pushPath := flag.String("push-path", "/metrics/job/:job", "HTTP path to accept pushed metrics.")
	flag.Parse()

	a := newAggate()

	r := route.New()
	r.Get("/metrics", a.handler)
	r.Get("/-/ready", handleHealthCheck)
	r.Get("/-/healthy", handleHealthCheck)
	pushHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", *cors)
		if err := a.parseAndMerge(r); err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	r.Post(*pushPath, pushHandler)
	r.Put(*pushPath, pushHandler)
	log.Fatal(http.ListenAndServe(*listen, r))
}
