package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	reasonQueueFull = "queue_full"
	reasonExpired   = "expired"
	reasonPermanent = "permanent"
	reasonRestart   = "restart"

	failTimeout   = "timeout"
	failHTTP5xx   = "http_5xx"
	failHTTP429   = "http_429"
	failHTTP4xx   = "http_4xx_permanent"
	failNetwork   = "network"
	labelSink     = "sink"
	metricsPrefix = "tailwatch"
)

func latencyBuckets() []float64 {
	return []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 900, 3600}
}

// notifyMetrics holds the tailwatch_notify_* series. A nil value is a no-op.
type notifyMetrics struct {
	sentC      *prometheus.CounterVec
	failedC    *prometheus.CounterVec
	droppedC   *prometheus.CounterVec
	persistErr *prometheus.CounterVec
	batchSizeH *prometheus.HistogramVec
	queueLenG  *prometheus.GaugeVec
	latencyH   *prometheus.HistogramVec
	openG      prometheus.Gauge
}

func newNotifyMetrics(reg prometheus.Registerer, withOpen bool) *notifyMetrics {
	m := &notifyMetrics{
		sentC: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsPrefix, Name: "notify_sent_total",
			Help: "Notifications delivered, by sink and event type.",
		}, []string{labelSink, "type"}),
		failedC: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsPrefix, Name: "notify_failed_total",
			Help: "Failed delivery attempts, by sink and reason.",
		}, []string{labelSink, "reason"}),
		droppedC: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsPrefix, Name: "notify_dropped_total",
			Help: "Notifications dropped without delivery, by sink and reason.",
		}, []string{labelSink, "reason"}),
		persistErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsPrefix, Name: "notify_persist_errors_total",
			Help: "Failed writes of notification state.",
		}, []string{labelSink}),
		batchSizeH: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metricsPrefix, Name: "notify_batch_size",
			Help:    "Events per batched webhook request.",
			Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500},
		}, []string{labelSink}),
		queueLenG: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: metricsPrefix, Name: "notify_queue_len",
			Help: "Events waiting in a sink queue.",
		}, []string{labelSink}),
		latencyH: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metricsPrefix, Name: "notify_latency_seconds",
			Help:    "Seconds from event creation to confirmed delivery.",
			Buckets: latencyBuckets(),
		}, []string{labelSink}),
		openG: nil,
	}
	cols := []prometheus.Collector{m.sentC, m.failedC, m.droppedC, m.persistErr, m.batchSizeH, m.queueLenG, m.latencyH}
	if withOpen {
		m.openG = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsPrefix, Name: "open_incidents",
			Help: "Incidents opened and not yet closed.",
		})
		cols = append(cols, m.openG)
	}
	if reg != nil {
		for _, c := range cols {
			reg.MustRegister(c)
		}
	}
	return m
}

func (m *notifyMetrics) sent(sink string, typ EventType, latency time.Duration) {
	if m == nil {
		return
	}
	m.sentC.WithLabelValues(sink, string(typ)).Inc()
	m.latencyH.WithLabelValues(sink).Observe(max(latency, 0).Seconds())
}

func (m *notifyMetrics) failed(sink, reason string) {
	if m != nil {
		m.failedC.WithLabelValues(sink, reason).Inc()
	}
}

func (m *notifyMetrics) dropped(sink, reason string) {
	if m != nil {
		m.droppedC.WithLabelValues(sink, reason).Inc()
	}
}

func (m *notifyMetrics) persistFailed() {
	if m != nil {
		m.persistErr.WithLabelValues(sinkState).Inc()
	}
}

func (m *notifyMetrics) batch(sink string, n int) {
	if m != nil {
		m.batchSizeH.WithLabelValues(sink).Observe(float64(n))
	}
}

func (m *notifyMetrics) queueLen(sink string, n int) {
	if m != nil {
		m.queueLenG.WithLabelValues(sink).Set(float64(n))
	}
}

func (m *notifyMetrics) openIncidents(n int) {
	if m != nil && m.openG != nil {
		m.openG.Set(float64(n))
	}
}
