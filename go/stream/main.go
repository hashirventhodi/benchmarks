package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type hub struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
	count   atomic.Int64
}

func newHub() *hub {
	return &hub{clients: make(map[chan string]struct{})}
}

func (h *hub) add() chan string {
	ch := make(chan string, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	h.count.Add(1)
	return ch
}

func (h *hub) remove(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
	h.count.Add(-1)
}

func (h *hub) broadcast(msg string) (sent, dropped int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
			sent++
		default:
			dropped++
		}
	}
	return
}

func main() {
	h := newHub()

	go func() {
		t := time.NewTicker(1 * time.Second)
		for range t.C {
			msg := fmt.Sprintf("tick %d", time.Now().Unix())
			sent, dropped := h.broadcast(msg)
			log.Printf("clients=%d sent=%d dropped=%d", h.count.Load(), sent, dropped)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := h.add()
		defer h.remove(ch)

		fmt.Fprintf(w, "event: hello\ndata: ok\n\n")
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d", h.count.Load())
	})

	addr := ":8182"
	log.Println("go stream listening on", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
