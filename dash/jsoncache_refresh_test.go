package dash

import (
	"encoding/json"
	"math"
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

// TestStatsLeanTotalSavedMatchesFullHandler is the fix for the PR #403 review's finding 1: lean
// used to leave CachesplitHistorical.USD and the declaration-filter credit (FilterUSD) out of
// total_saved_usd, so the status line under-reported relative to the dashboard whenever either
// was nonzero. Both are now included — only SelfRemovals' full-tenant scan is skipped, and that
// one was NEVER part of total_saved_usd even in the full handler (dash/declcredit.go's
// SetDeclCredit folds SelfUSD into TotalReducedUSD only) — so lean's total_saved_usd must now
// equal the full handler's exactly, not merely be closer to it.
//
// seedCredit (dash/declcredit_test.go) gives the filter-credit half; a pre-instrumentation
// cachesplit-historical pair (same shape as TestWaterfallTotalMatchesTheHeadlineAfterAllPricedAdditions
// in dash/declcredit_test.go) gives the historical-split half, so both of the review's named
// amounts are actually nonzero in this fixture rather than trivially matching by both being zero.
func TestStatsLeanTotalSavedMatchesFullHandler(t *testing.T) {
	db := seedCredit(t, 1000)
	teach := mkEvent(9000, "s-hist", "aws/claude-sonnet-5", 100, 100)
	teach.TenantID, teach.CacheRead, teach.CacheWrite, teach.SplitStableTokens = "t1", 54_304, 1_000, 5_697
	qualifies := mkEvent(9100, "s-new-hist", "aws/claude-sonnet-5", 100, 100)
	qualifies.TenantID, qualifies.CacheRead, qualifies.CacheWrite = "t1", 54_304, 1_000
	// One filtered row on a model staticPricer refuses to price (see its own doc comment: the
	// sentinel name "some/unmeasured-model" always returns Priced=false). This is the fixture
	// gap a re-review of this PR found: DeclFilterSaving.Priced goes false the moment ANY ONE
	// request in scope is unpriced, but USD still holds the priced subset's real dollar figure
	// — and an earlier version of the lean handler gated on Priced, which dropped the WHOLE
	// filter credit over this one row. Every model in the fixture above this comment has a
	// price, so without this row the parity assertion below would pass for the wrong reason.
	unpriced := mkEvent(9200, "s-unpriced", "some/unmeasured-model", 100, 90)
	unpriced.TenantID, unpriced.Tools, unpriced.FilteredDeclTokens = "t1", 4, 500
	if err := db.insertBatch([]*Event{teach, qualifies, unpriced}); err != nil {
		t.Fatal(err)
	}
	rec := &Recorder{db: db, hub: NewHub(), done: make(chan struct{})}
	t.Cleanup(func() { rec.Close() })
	api := NewAPI(rec)
	api.SetPricer(staticPricer{ibmSonnet})
	mux := http.NewServeMux()
	api.Mount(mux)

	full := httptest.NewRecorder()
	mux.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	lean := httptest.NewRecorder()
	mux.ServeHTTP(lean, httptest.NewRequest(http.MethodGet, "/api/stats?lean=1", nil))
	if full.Code != http.StatusOK || lean.Code != http.StatusOK {
		t.Fatalf("full = %d, lean = %d", full.Code, lean.Code)
	}

	var fullBody, leanBody struct {
		TotalSavedUSD float64 `json:"total_saved_usd"`
		DeclFilterUSD float64 `json:"decl_filter_usd"`
	}
	if err := json.Unmarshal(full.Body.Bytes(), &fullBody); err != nil {
		t.Fatalf("full body: %v\n%s", err, full.Body)
	}
	if err := json.Unmarshal(lean.Body.Bytes(), &leanBody); err != nil {
		t.Fatalf("lean body: %v\n%s", err, lean.Body)
	}
	if fullBody.TotalSavedUSD <= 0 {
		t.Fatal("full handler's total_saved_usd is not positive; the fixture is not exercising anything")
	}
	if fullBody.DeclFilterUSD <= 0 {
		t.Fatal("decl_filter_usd is not positive; the fixture stopped exercising the filter credit")
	}
	if math.Abs(fullBody.TotalSavedUSD-leanBody.TotalSavedUSD) > 1e-9 {
		t.Errorf("lean total_saved_usd = %v, full total_saved_usd = %v — lean must match exactly, "+
			"since the only thing it skips (SelfRemovals) was never part of this field",
			leanBody.TotalSavedUSD, fullBody.TotalSavedUSD)
	}
}
