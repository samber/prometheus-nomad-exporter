package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"
	"time"

        "github.com/prometheus/client_golang/prometheus"
        nomad "github.com/hashicorp/nomad/api"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
)

const (
	namespace = "nomad"
)

type nomadHealth struct {
	//Pid                    int            `json:"pid"`
	//UpTime                 string         `json:"uptime"`
	UpTimeSec float64 `json:"uptime_sec"`
	//Time                   string         `json:"time"`
	//TimeUnix               int64          `json:"unixtime"`
	StatusCodeCount      map[string]float64 `json:"status_code_count"`
	TotalStatusCodeCount map[string]float64 `json:"total_status_code_count"`
	//Count                  float64            `json:"count"`
	//TotalCount             float64            `json:"total_count"`
	//TotalResponseTime      string         `json:"total_response_time"`
	TotalResponseTimeSec float64 `json:"total_response_time_sec"`
	//AverageResponseTime    string         `json:"average_response_time"`
	AverageResponseTimeSec float64 `json:"average_response_time_sec"`
}

func newCounter(metricName string, docString string, constLabels prometheus.Labels) prometheus.Counter {
	return prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace:   namespace,
			Name:        metricName,
			Help:        docString,
			ConstLabels: constLabels,
		},
	)
}

func newGauge(metricName string, docString string, constLabels prometheus.Labels) prometheus.Gauge {
	return prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace:   namespace,
			Name:        metricName,
			Help:        docString,
			ConstLabels: constLabels,
		},
	)
}

var (
        nomad_up                           = newGauge("up", "Is Nomad up ?", nil)
	metric_uptime                      = newGauge("uptime", "Current Nomad uptime", nil)
	metric_request_response_time_total = newGauge("request_response_time_total", "Total response time of Nomad requests", nil)
	metric_request_response_time_avg   = newGauge("request_response_time_avg", "Average response time of Nomad requests", nil)

	// Labeled metrics
	// Set at runtime
	metric_request_status_count_current = map[string]prometheus.Gauge{} // newGauge("request_count_current", "Number of request Nomad is handling", nil)
	metric_request_status_count_total   = map[string]prometheus.Gauge{} // newGauge("request_count_total", "Number of request handled by Nomad", nil)
)

func init() {
     	nomad_up.Set(0)
}

// Exporter collects Nomad stats from the given hostname and exports them using
// the prometheus metrics package.
type Exporter struct {
	mutex  sync.RWMutex
	URI    string
	client *nomad.Client
}

// NewExporter returns an initialized Exporter.
func NewExporter(uri string, region string) *Exporter {
	// Set up our Nomad client connection.
        client := nomad.NewClient(&nomad.Config{
                Address: uri,
                Region: region,
        })
	return &Exporter{
		URI: uri,
		client: client,
	}
}

// Describe describes all the metrics ever exported by the Nomad exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- metric_uptime.Desc()
	ch <- nomad_up.Desc()
	ch <- metric_request_response_time_total.Desc()
	ch <- metric_request_response_time_avg.Desc()

	for _, metric := range metric_request_status_count_current {
		ch <- metric.Desc()
	}
	for _, metric := range metric_request_status_count_total {
		ch <- metric.Desc()
	}
}

// Collect fetches the stats from configured Nomad location and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock() // To protect metrics from concurrent collects.
	defer e.mutex.Unlock()

	if err := e.scrape(); err != nil {
		log.Error(err)
		nomad_up.Set(0)
		ch <- nomad_up
		return
	}

	ch <- nomad_up
	ch <- metric_uptime
	ch <- metric_request_response_time_total
	ch <- metric_request_response_time_avg

	for _, metric := range metric_request_status_count_current {
		ch <- metric
	}
	for _, metric := range metric_request_status_count_total {
		ch <- metric
	}
}

func (e *Exporter) scrape() error {
	resp, err := e.client.Get(e.URI)
	if err != nil {
		return errors.New(fmt.Sprintf("Can't scrape Nomad: %v", err))
	}
	defer resp.Body.Close()
	if !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return errors.New(fmt.Sprintf("Can't scrape Nomad: status %d", resp.StatusCode))
	}

	var data nomadHealth
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&data); err != nil {
		return errors.New(fmt.Sprintf("Can't scrape Nomad: json.Unmarshal %v", err))
	}

	nomad_up.Set(1)
	metric_uptime.Set(data.UpTimeSec)
	metric_request_response_time_total.Set(data.TotalResponseTimeSec)
	metric_request_response_time_avg.Set(data.AverageResponseTimeSec)

	// Current request count, labeled by statusCode
	// Must be reset for missing status code metrics in data
	for _, metric := range metric_request_status_count_current {
		metric.Set(0)
	}
	for statusCode, nbr := range data.StatusCodeCount {
		if _, ok := metric_request_status_count_current[statusCode]; ok == false {
			metric_request_status_count_current[statusCode] = newGauge("request_count_current", "Number of request handled by Nomad", prometheus.Labels{"statusCode": statusCode})
		}
		metric_request_status_count_current[statusCode].Set(nbr)
	}

	// Total request count, labeled by statusCode
	for statusCode, nbr := range data.TotalStatusCodeCount {
		if _, ok := metric_request_status_count_total[statusCode]; ok == false {
			metric_request_status_count_total[statusCode] = newGauge("request_count_total", "Number of request handled by Nomad", prometheus.Labels{"statusCode": statusCode})
		}
		metric_request_status_count_total[statusCode].Set(nbr)
	}

	return nil
}

func init() {
	prometheus.MustRegister(version.NewCollector("nomad_exporter"))
}

func main() {
	var (
		showVersion   = flag.Bool("version", false, "Print version information.")
		listenAddress = flag.String("web.listen-address", ":9000", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		nomadAddr     = flag.String("nomad.address", "", "HTTP API address of a Nomad agent")
		nomadRegion   = flag.String("nomad.region", "", "Nomad region to track.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("nomad_exporter"))
		os.Exit(0)
	}

	log.Infoln("Starting nomad_exporter", version.Info())
	log.Infoln("Build context", version.BuildContext())

	exporter := NewExporter(*nomadAddr, *nomadRegion)
	prometheus.MustRegister(exporter)
	prometheus.Unregister(prometheus.NewGoCollector())
	prometheus.Unregister(prometheus.NewProcessCollector(os.Getpid(), ""))

	http.Handle(*metricsPath, prometheus.UninstrumentedHandler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Nomad Exporter</title></head>
             <body>
             <h1>Nomad Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})

	log.Infoln("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
