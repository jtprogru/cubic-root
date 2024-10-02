package main

import (
	"encoding/json"
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	Version     = "0.1.0"
	articleLink = "https://jtprog.ru/interview-task-0003/"
)

var (
	debugMode     bool
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cubic_root_requests_total",
			Help: "Total number of requests to the cubic root endpoint",
		},
		[]string{"status"},
	)
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cubic_root_request_duration_seconds",
			Help:    "Histogram of response time for cubic root requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status"},
	)
	requestSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cubic_root_request_size_bytes",
			Help:    "Histogram of request sizes in bytes.",
			Buckets: prometheus.ExponentialBuckets(10, 2, 10), // Example buckets from 10B to ~50KB
		},
	)
	responseSize = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cubic_root_response_size_bytes",
			Help:    "Histogram of response sizes in bytes.",
			Buckets: prometheus.ExponentialBuckets(10, 2, 10), // Example buckets from 10B to ~50KB
		},
	)
	activeRequests = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "cubic_root_active_requests",
			Help: "Number of active requests being processed.",
		},
	)
)

func init() {
	debugEnv := os.Getenv("DEBUG")
	debugMode, _ = strconv.ParseBool(debugEnv)
	// Register new metrics
	prometheus.MustRegister(requestSize)
	prometheus.MustRegister(responseSize)
	prometheus.MustRegister(activeRequests)
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(requestDuration)
}

func main() {
	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8080"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Fatalf("Invalid port: %v", err)
	}

	http.HandleFunc("/cubic-root", cubicRootHandler)
	http.Handle("/metrics", promhttp.Handler())

	log.Printf("Server version %s is running on http://localhost:%d", Version, port)
	log.Printf("Send requests to http://localhost:%d/cubic-root?d=<value>", port)
	log.Printf("Debug mode is %t", debugMode)
	log.Printf("For more information, visit: %s", articleLink)
	log.Println("To exit, press Ctrl+C")

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}
func cubicRootHandler(w http.ResponseWriter, r *http.Request) {
	activeRequests.Inc()
	defer activeRequests.Dec()

	startTime := time.Now()
	var status string

	defer func() {
		duration := time.Since(startTime).Seconds()
		requestDuration.WithLabelValues(status).Observe(duration)
	}()

	// Measure request size
	requestSize.Observe(float64(r.ContentLength))

	var req CubicRootRequest
	if err := parseQueryParamsToStruct(r.URL.Query(), &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		status = "400"
		requestsTotal.WithLabelValues(status).Inc()
		return
	}

	debugLog("Received request: %f", req.D)

	result := calculateCubicRoot(req.D)

	debugLog("Calculated result: %f, message: %s", result.Result, result.Message)

	// Encode response and measure size
	w.Header().Set("Content-Type", "application/json")
	responseBytes, err := json.Marshal(result)
	if err != nil {
		log.Printf("Error encoding response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		status = "500"
		requestsTotal.WithLabelValues(status).Inc()
		return
	}

	responseSize.Observe(float64(len(responseBytes)))

	_, _ = w.Write(responseBytes)
	status = "200"
	requestsTotal.WithLabelValues(status).Inc()
}

type CubicRootRequest struct {
	D float64 `json:"d"`
}

type CubicRootResponse struct {
	Result  float64 `json:"result"`
	Message string  `json:"message"`
}

func calculateCubicRoot(d float64) CubicRootResponse {
	result := cubeRoot(d)

	debugLog("Cubic root of %.6f is %.6f", d, result)

	return CubicRootResponse{
		Result:  result,
		Message: "Done",
	}
}

func debugLog(format string, v ...interface{}) {
	if debugMode {
		log.Printf(format, v...)
	}
}

func parseQueryParamsToStruct(values url.Values, target interface{}) error {
	req, ok := target.(*CubicRootRequest)
	if !ok {
		return fmt.Errorf("Target must be of type *CubicRootRequest")
	}

	dParam := values.Get("d")
	if dParam == "" {
		return fmt.Errorf("Missing parameter 'd'")
	}

	d, err := strconv.ParseFloat(dParam, 64)
	if err != nil {
		return fmt.Errorf("Invalid parameter 'd': %v", err)
	}

	req.D = d
	return nil
}

func cubeRoot(x float64) float64 {
	if x == 0 {
		return 0
	}

	z := x / 3
	precision := 1e-10

	for {
		nextZ := (2*z + x/(z*z)) / 3
		if math.Abs(nextZ-z) < precision {
			break
		}
		z = nextZ
	}

	return z
}
