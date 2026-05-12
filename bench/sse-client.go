// SSE load client. Opens N concurrent SSE connections to a URL and holds
// them open for the given duration, counting received events.
//
//	go run sse-client.go -url=http://localhost:8082/stream -n=5000 -duration=20s
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "", "SSE endpoint")
	n := flag.Int("n", 1000, "concurrent connections")
	dur := flag.Duration("duration", 20*time.Second, "hold duration")
	flag.Parse()
	if *url == "" {
		panic("need -url")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()

	var (
		active   atomic.Int64
		events   atomic.Int64
		failed   atomic.Int64
		hellos   atomic.Int64
	)

	transport := &http.Transport{
		MaxIdleConnsPerHost: *n,
		MaxConnsPerHost:     *n,
		DisableCompression:  true,
	}
	cli := &http.Client{Transport: transport}

	stagger := time.Millisecond * 1
	for i := 0; i < *n; i++ {
		go func(id int) {
			req, _ := http.NewRequestWithContext(ctx, "GET", *url, nil)
			req.Header.Set("Accept", "text/event-stream")
			resp, err := cli.Do(req)
			if err != nil {
				failed.Add(1)
				return
			}
			defer resp.Body.Close()
			active.Add(1)
			defer active.Add(-1)

			br := bufio.NewReader(resp.Body)
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if len(line) > 5 && line[:5] == "data:" {
					events.Add(1)
				}
				if len(line) > 6 && line[:6] == "event:" {
					hellos.Add(1)
				}
			}
		}(i)
		time.Sleep(stagger)
	}

	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("FINAL active=%d events=%d failed=%d hellos=%d\n",
				active.Load(), events.Load(), failed.Load(), hellos.Load())
			return
		case <-t.C:
			fmt.Printf("active=%d events=%d failed=%d hellos=%d\n",
				active.Load(), events.Load(), failed.Load(), hellos.Load())
		}
	}
}
