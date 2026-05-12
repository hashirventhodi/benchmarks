package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
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

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.GET("/stream", func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")

		ch := h.add()
		defer h.remove(ch)

		fmt.Fprintf(c.Writer, "event: hello\ndata: ok\n\n")
		c.Writer.Flush()

		c.Stream(func(w io.Writer) bool {
			select {
			case <-c.Request.Context().Done():
				return false
			case msg, ok := <-ch:
				if !ok {
					return false
				}
				fmt.Fprintf(w, "data: %s\n\n", msg)
				return true
			}
		})
	})

	r.GET("/count", func(c *gin.Context) {
		c.String(http.StatusOK, "%d", h.count.Load())
	})

	addr := ":8183"
	log.Println("go stream-gin listening on", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}
