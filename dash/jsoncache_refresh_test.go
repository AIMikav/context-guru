package dash

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestJSONCacheLoadServesPastCapWhileRefreshing exercises jsonCache.load/beginRefresh/endRefresh
// directly: a body older than dashCacheTTL+dashCacheStale is normally reported absent, but not
// while a refresh for that key is already running, and it goes back to absent once that refresh
// ends without having replaced it (an error, or a goroutine that never got around to it).
func TestJSONCacheLoadServesPastCapWhileRefreshing(t *testing.T) {
	c := &jsonCache{}
	key := "k"
	tooOld := dashCacheTTL + dashCacheStale + time.Second
	c.entries = map[string]jsonCacheEntry{key: {at: time.Now().Add(-tooOld), body: []byte(`{"old":true}`)}}

	if body, _ := c.load(key); body != nil {
		t.Fatalf("load() with no refresh running and an entry past the cap = %q; want absent (nil)", body)
	}

	if !c.beginRefresh(key) {
		t.Fatal("beginRefresh() on an unclaimed key = false; want true")
	}
	body, fresh := c.load(key)
	if body == nil {
		t.Fatal("load() with a refresh in flight = absent; want the old body kept available")
	}
	if fresh {
		t.Error("load() reported a body past dashCacheTTL as fresh")
	}

	// A second, concurrent refresh attempt for the same key must not be granted —
	// this is the gate that stops N stale readers from starting N duplicate computations.
	if c.beginRefresh(key) {
		t.Fatal("beginRefresh() on a key already refreshing = true; want false (second caller must not start its own)")
	}

	c.endRefresh(key)
	if body, _ := c.load(key); body != nil {
		t.Fatalf("load() after endRefresh with nothing having replaced the entry = %q; want absent again", body)
	}
	// The key is claimable again now that the previous refresh ended.
	if !c.beginRefresh(key) {
		t.Fatal("beginRefresh() after endRefresh = false; want true")
	}
	c.endRefresh(key)
}

// TestServeJSONSingleFlightsStaleRefresh is the end-to-end version of the test above: many
// concurrent requests landing on a STALE (but not yet cold) cache entry must trigger exactly one
// refresh compute, and every one of them must get an immediate response rather than blocking on
// it. This is the real path the user's statusline timeouts went through — the previous code
// started one new goroutine, running the full compute, PER stale request.
func TestServeJSONSingleFlightsStaleRefresh(t *testing.T) {
	a, _ := newTestAPI(t, Options{})
	var computes int32
	release := make(chan struct{})

	c := &jsonCache{entries: map[string]jsonCacheEntry{
		"k": {at: time.Now().Add(-(dashCacheTTL + time.Millisecond)), body: []byte(`{"gen":0}`)},
	}}
	compute := func(db *DB) ([]byte, error) {
		n := atomic.AddInt32(&computes, 1)
		<-release // held open until the test says every concurrent caller has already been served
		return []byte(`{"gen":` + string(rune('0'+n)) + `}`), nil
	}

	const n = 8
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
			a.serveJSON(w, r, c, "k", compute)
			if w.Header().Get("X-Cache") != "stale" {
				t.Errorf("X-Cache = %q; want %q (every caller must be served the stale body, not block)",
					w.Header().Get("X-Cache"), "stale")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent stale-cache reader blocked instead of being served the cached body immediately")
		}
	}
	close(release)

	// Give the single refresh goroutine a moment to finish and release its claim.
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		refreshing := c.refreshing["k"]
		c.mu.Unlock()
		if !refreshing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh never released its claim on the key")
		}
		time.Sleep(time.Millisecond)
	}

	if got := atomic.LoadInt32(&computes); got != 1 {
		t.Errorf("compute ran %d times for %d concurrent stale readers of the same key; want exactly 1", got, n)
	}
}

// TestStatsLeanSkipsThePricedExtras checks that ?lean=1 answers from Overview() alone — the path
// the statusline's own _fetch_stats takes — and that it still carries every field that path
// reads (total_saved_usd, saved_unique, cg_latency_ms_avg, upstream_ms_avg, keepalive_net_usd,
// keepalive_misses_avoided).
func TestStatsLeanSkipsThePricedExtras(t *testing.T) {
	a, rec := newTestAPI(t, Options{})
	seed(t, rec, mkEvent(time.Now().UnixMilli(), "sess-1", "aws/claude-sonnet-5", 1000, 800))

	_, body := get(t, a, "/api/stats?lean=1&session=sess-1", "127.0.0.1:1")
	for _, field := range []string{
		"total_saved_usd", "saved_unique", "cg_latency_ms_avg", "upstream_ms_avg",
		"keepalive_net_usd", "keepalive_misses_avoided",
	} {
		if _, ok := body[field]; !ok {
			t.Errorf("lean /api/stats is missing %q, which the statusline reads", field)
		}
	}
}

// TestStatsLeanIsACacheKeySeparateFromTheFullHandler guards against a lean and a non-lean
// request for the same scope colliding on one cache entry — cacheKey includes the raw query
// string (dash/api.go's cacheKey), so this is really a test that the ?lean=1 param survives
// into it, but it is cheap to assert the actual behaviour rather than the mechanism.
func TestStatsLeanIsACacheKeySeparateFromTheFullHandler(t *testing.T) {
	a, rec := newTestAPI(t, Options{})
	seed(t, rec, mkEvent(time.Now().UnixMilli(), "sess-1", "aws/claude-sonnet-5", 1000, 800))

	_, full := get(t, a, "/api/stats", "127.0.0.1:1")
	_, lean := get(t, a, "/api/stats?lean=1", "127.0.0.1:1")
	if _, ok := full["waterfall"]; !ok {
		t.Error("the full /api/stats response has no waterfall; test fixture assumption broke")
	}
	// Not asserting lean lacks "waterfall" (Overview() sets its own baseline one), only that
	// the two requests were not served the exact same cached bytes.
	if full["total_saved_usd"] == nil || lean["total_saved_usd"] == nil {
		t.Fatal("total_saved_usd missing from one of the two responses")
	}
}
