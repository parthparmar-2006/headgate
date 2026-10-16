package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

type tally struct {
	mu     sync.Mutex
	counts map[string]map[int64]int
}

func newTally() *tally {
	return &tally{counts: make(map[string]map[int64]int)}
}

func (t *tally) record(key string, second int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	perSecond, found := t.counts[key]
	if !found {
		perSecond = make(map[int64]int)
		t.counts[key] = perSecond
	}
	perSecond[second]++
}

func (t *tally) snapshot() map[string]map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make(map[string]map[string]int)
	for key, perSecond := range t.counts {
		copied := make(map[string]int)
		for second, count := range perSecond {
			copied[strconv.FormatInt(second, 10)] = count
		}
		result[key] = copied
	}
	return result
}

func (t *tally) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts = make(map[string]map[int64]int)
}

type oracle struct {
	byArrival  *tally
	byDecision *tally
}

func newOracle() *oracle {
	return &oracle{byArrival: newTally(), byDecision: newTally()}
}

func (o *oracle) record(r *http.Request) {
	key := r.Header.Get("X-API-Key")
	o.byArrival.record(key, time.Now().Unix())

	decisionMs, err := strconv.ParseInt(r.Header.Get("X-Headgate-Decision-Ms"), 10, 64)
	if err == nil {
		o.byDecision.record(key, decisionMs/1000)
	}
}

func (o *oracle) snapshot() map[string]map[string]map[string]int {
	return map[string]map[string]map[string]int{
		"arrival":  o.byArrival.snapshot(),
		"decision": o.byDecision.snapshot(),
	}
}

func (o *oracle) reset() {
	o.byArrival.reset()
	o.byDecision.reset()
}

func main() {
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":9000"
	}

	counts := newOracle()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(counts.snapshot())
	})
	mux.HandleFunc("POST /_reset", func(w http.ResponseWriter, r *http.Request) {
		counts.reset()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		counts.record(r)
		w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("upstream listening on %s", listenAddr)
	log.Fatal(server.ListenAndServe())
}
