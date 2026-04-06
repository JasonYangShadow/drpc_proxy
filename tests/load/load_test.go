//go:build load

// Package load drives the DRPC proxy at a configurable request rate using a
// token-bucket dispatcher. By default it targets 5 000 RPS with 500 concurrent
// users. Runs until Ctrl+C or -duration.
//
// Usage:
//
//	task test:load
//	task test:load USERS=500 RATE=5000 DURATION=2m
//
//	# or directly:
//	PROXY_ADDR=http://<ip>:8545 go test -v -tags load -timeout 0 ./tests/load/ \
//	  -users 500 -rate 5000 -duration 2m
//
// Flags:
//
//	-users      concurrent user goroutines (default 500)
//	-rate       target RPS via token-bucket; 0 = gap-limited (default 5000)
//	-gap        max random sleep between requests — ignored when -rate > 0 (default 0)
//	-duration   how long to run; 0 = run until Ctrl+C (default 0)
package load

import (
	"bytes"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ── flags ─────────────────────────────────────────────────────────────────────

var (
	fUsers    = flag.Int("users", 500, "number of simulated concurrent users")
	fGap      = flag.Duration("gap", 0, "max random gap between requests per user (ignored when -rate > 0)")
	fDuration = flag.Duration("duration", 0, "how long to run (0 = until Ctrl+C)")
	fRate     = flag.Int("rate", 5000, "target requests per second (0 = gap-limited / max throughput)")
)

// ── helpers ───────────────────────────────────────────────────────────────────

func proxyAddr() string {
	if v := os.Getenv("PROXY_ADDR"); v != "" {
		return v
	}
	return "http://localhost:8545"
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 1000,
		MaxConnsPerHost:     1000,
	},
}

// sendRPC fires a single POST /rpc and returns the round-trip ms (or -1 on error).
func sendRPC(payload []byte) float64 {
	start := time.Now()
	resp, err := httpClient.Post(proxyAddr()+"/rpc", "application/json", bytes.NewReader(payload))
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	// drain body so the connection is reused
	buf := make([]byte, 256)
	for {
		_, err := resp.Body.Read(buf)
		if err != nil {
			break
		}
	}
	return float64(time.Since(start).Microseconds()) / 1000.0
}

// percentile returns the p-th percentile (0–100) of a sorted float64 slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printHistogram(sorted []float64, buckets []float64) {
	if len(sorted) == 0 {
		return
	}
	counts := make([]int, len(buckets)+1)
	for _, v := range sorted {
		placed := false
		for i, b := range buckets {
			if v <= b {
				counts[i]++
				placed = true
				break
			}
		}
		if !placed {
			counts[len(buckets)]++
		}
	}
	total := len(sorted)
	labels := make([]string, len(buckets)+1)
	for i, b := range buckets {
		labels[i] = fmt.Sprintf("≤%4.0fms", b)
	}
	labels[len(buckets)] = fmt.Sprintf(" >%4.0fms", buckets[len(buckets)-1])
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	const barWidth = 40
	for i, label := range labels {
		bar := ""
		if max > 0 {
			n := int(float64(counts[i]) / float64(max) * barWidth)
			for j := 0; j < n; j++ {
				bar += "█"
			}
		}
		pct := 100.0 * float64(counts[i]) / float64(total)
		fmt.Printf("  %s │%-40s │ %5d  %5.1f%%\n", label, bar, counts[i], pct)
	}
}

// ── test ──────────────────────────────────────────────────────────────────────

func TestLoad(t *testing.T) {
	// Check proxy reachability
	resp, err := http.Get(proxyAddr() + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skipf("proxy not reachable at %s — skipping load test", proxyAddr())
	}
	resp.Body.Close()

	users := *fUsers
	gap := *fGap
	dur := *fDuration
	targetRate := *fRate

	// Build stop channel: Ctrl+C, SIGTERM, or optional duration deadline.
	stopCh := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		if dur > 0 {
			select {
			case <-time.After(dur):
			case <-sig:
			}
		} else {
			<-sig
		}
		close(stopCh)
	}()
	defer signal.Stop(sig)

	// Token-bucket rate limiter: when targetRate > 0 a feeder goroutine emits
	// tokens in batches every 1ms. A sub-millisecond ticker interval is useless
	// on Linux where the OS timer resolution floor is ~1ms, so instead we fire
	// every 1ms and issue (targetRate/1000) tokens per tick. This correctly
	// sustains any target rate without being capped by timer granularity.
	var permitCh chan struct{}
	if targetRate > 0 {
		tokensPerMs := targetRate / 1000
		if tokensPerMs < 1 {
			tokensPerMs = 1
		}
		permitCh = make(chan struct{}, tokensPerMs*20) // ~20ms burst buffer
		go func() {
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					for i := 0; i < tokensPerMs; i++ {
						select {
						case permitCh <- struct{}{}:
						default: // drop if users can't keep up
						}
					}
				}
			}
		}()
	}

	// Rotating RPC methods
	methods := []string{"eth_blockNumber", "eth_gasPrice", "eth_chainId", "eth_getBlockByNumber", "net_version"}
	params := []string{"[]", "[]", "[]", `["latest", false]`, "[]"}

	if dur > 0 {
		t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		t.Logf("  DRPC Proxy Load Test")
		t.Logf("  target   : %s", proxyAddr())
		t.Logf("  users    : %d", users)
		if targetRate > 0 {
			t.Logf("  rate     : %d rps (token-bucket)", targetRate)
		} else {
			t.Logf("  gap      : 0 – %s per user", gap)
		}
		t.Logf("  duration : %s", dur)
		t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	} else {
		t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		t.Logf("  DRPC Proxy Load Test  (Ctrl+C to stop)")
		t.Logf("  target   : %s", proxyAddr())
		t.Logf("  users    : %d", users)
		if targetRate > 0 {
			t.Logf("  rate     : %d rps (token-bucket)", targetRate)
		} else {
			t.Logf("  gap      : 0 – %s per user", gap)
		}
		t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}

	// Shared counters
	var totalSent, totalOK, totalErr int64

	// Per-window latency samples
	var latMu sync.Mutex
	var latSamples []float64

	// Launch one goroutine per user
	var wg sync.WaitGroup
	for u := 0; u < users; u++ {
		wg.Add(1)
		go func(userID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(userID)))
			seq := 0
			for {
				select {
				case <-stopCh:
					return
				default:
				}

				mi := seq % len(methods)
				payload := []byte(fmt.Sprintf(
					`{"jsonrpc":"2.0","method":%q,"params":%s,"id":%d}`,
					methods[mi], params[mi], seq+1,
				))
				ms := sendRPC(payload)
				atomic.AddInt64(&totalSent, 1)
				if ms >= 0 {
					atomic.AddInt64(&totalOK, 1)
					latMu.Lock()
					latSamples = append(latSamples, ms)
					latMu.Unlock()
				} else {
					atomic.AddInt64(&totalErr, 1)
				}
				seq++

				if permitCh != nil {
					// Rate-limited: wait for the next token.
					select {
					case <-permitCh:
					case <-stopCh:
						return
					}
				} else if gap > 0 {
					// Gap-limited: random sleep [0, gap).
					slp := time.Duration(rng.Int63n(int64(gap)))
					select {
					case <-time.After(slp):
					case <-stopCh:
						return
					}
				}
			}
		}(u)
	}

	// Stats reporter — prints a summary every 10 s
	start := time.Now()
	prevSent := int64(0)
	prevTime := start
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

statsLoop:
	for {
		select {
		case <-stopCh:
			break statsLoop
		case now := <-ticker.C:
			sentNow := atomic.LoadInt64(&totalSent)
			okNow := atomic.LoadInt64(&totalOK)
			errNow := atomic.LoadInt64(&totalErr)
			intervalRPS := float64(sentNow-prevSent) / now.Sub(prevTime).Seconds()
			overallRPS := float64(sentNow) / now.Sub(start).Seconds()
			prevSent = sentNow
			prevTime = now

			latMu.Lock()
			window := append([]float64(nil), latSamples...)
			latSamples = latSamples[:0]
			latMu.Unlock()

			sort.Float64s(window)
			t.Logf("── %s elapsed ─────────────────────────────────────────",
				now.Sub(start).Round(time.Second))
			t.Logf("  sent=%d  ok=%d  err=%d  rps(10s)=%.0f  rps(total)=%.0f",
				sentNow, okNow, errNow, intervalRPS, overallRPS)
			if len(window) > 0 {
				t.Logf("  POST /rpc latency last 10s (ms):")
				t.Logf("    p50=%.1f  p90=%.1f  p95=%.1f  p99=%.1f  max=%.1f",
					percentile(window, 50),
					percentile(window, 90),
					percentile(window, 95),
					percentile(window, 99),
					window[len(window)-1],
				)
				printHistogram(window, []float64{5, 10, 25, 50, 100, 250, 500})
			}
		}
	}

	wg.Wait()

	// Final summary
	total := time.Since(start)
	sentFinal := atomic.LoadInt64(&totalSent)
	okFinal := atomic.LoadInt64(&totalOK)
	errFinal := atomic.LoadInt64(&totalErr)
	t.Logf("")
	t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	t.Logf("  FINAL SUMMARY  duration=%s", total.Round(time.Second))
	t.Logf("  total sent : %d", sentFinal)
	t.Logf("  ok         : %d (%.1f%%)", okFinal, 100*float64(okFinal)/float64(sentFinal))
	t.Logf("  errors     : %d", errFinal)
	t.Logf("  avg rps    : %.0f", float64(sentFinal)/total.Seconds())
	t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}
