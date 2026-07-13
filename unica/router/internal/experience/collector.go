// Package experience feeds customer-service interaction outcomes back into the
// acest experience knowledge base. Submissions are asynchronous and lossy by
// design: the message-processing hot path enqueues and moves on; a background
// worker delivers to the kb-server, and a full queue drops (with a metric)
// rather than ever blocking routing.
package experience

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kefu/unica/router/internal/bridge"
)

var (
	submittedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_experience_submitted_total",
		Help: "Experiences submitted to the acest kb-server by outcome",
	}, []string{"outcome", "status"})

	droppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_experience_dropped_total",
		Help: "Experiences dropped because the submission queue was full",
	})
)

// submitTimeout bounds a single delivery attempt to the kb-server.
const submitTimeout = 15 * time.Second

// Collector queues experiences and submits them to acest in the background.
type Collector struct {
	client *bridge.AcestClient
	cfg    bridge.AcestConfig
	queue  chan bridge.Experience
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewCollector creates a Collector with the given queue capacity (defaults to
// 256 when non-positive).
func NewCollector(client *bridge.AcestClient, cfg bridge.AcestConfig, queueSize int) *Collector {
	if queueSize <= 0 {
		queueSize = 256
	}
	return &Collector{
		client: client,
		cfg:    cfg,
		queue:  make(chan bridge.Experience, queueSize),
		stopCh: make(chan struct{}),
	}
}

// Start launches the background submission worker.
func (c *Collector) Start() {
	c.wg.Add(1)
	go c.run()
	log.Printf("[experience] collector started (queue=%d, target=%s)", cap(c.queue), c.cfg.BaseURL)
}

// Stop drains nothing further and waits for the worker to exit. Experiences
// still in the queue at shutdown are dropped; this feedback loop is
// best-effort by design.
func (c *Collector) Stop() {
	close(c.stopCh)
	c.wg.Wait()
	log.Printf("[experience] collector stopped (%d undelivered)", len(c.queue))
}

// Submit enqueues an experience without blocking. Returns false if the queue
// is full and the experience was dropped.
func (c *Collector) Submit(exp bridge.Experience) bool {
	select {
	case c.queue <- exp:
		return true
	default:
		droppedTotal.Inc()
		return false
	}
}

func (c *Collector) run() {
	defer c.wg.Done()
	for {
		select {
		case <-c.stopCh:
			return
		case exp := <-c.queue:
			c.deliver(exp)
		}
	}
}

func (c *Collector) deliver(exp bridge.Experience) {
	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()

	outcome := "success"
	if !exp.Success {
		outcome = "failure"
	}

	taskID, err := c.client.SubmitExperience(ctx, c.cfg, exp)
	if err != nil {
		submittedTotal.WithLabelValues(outcome, "error").Inc()
		log.Printf("[experience] submission failed (session=%s): %v", exp.SessionID, err)
		return
	}
	submittedTotal.WithLabelValues(outcome, "accepted").Inc()
	log.Printf("[experience] submitted (session=%s, outcome=%s, task=%s)", exp.SessionID, outcome, taskID)
}
