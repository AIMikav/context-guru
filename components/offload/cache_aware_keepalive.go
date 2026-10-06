package offload

import (
	"context"
	"sync"
	"time"

	bschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rossoctl/context-guru/components"
	"github.com/rossoctl/context-guru/store"
)

// Letting cache_aware_summarizer stand in for the idle keep-alive's bare cache-read ping.
//
// proxy/keepalive.go's keeper spends the caller's own credential on a ping whose only job is to
// read the cached prefix and reset the provider's TTL. When this component is in the pipeline,
// has already decided a session's fill is worth a summary, and is only waiting on the right
// cache phase (or lost the race for a concurrency slot, or any other transient reason it did not
// fire on the turn that registered this material), the keeper's next scheduled ping is a session
// that is ABOUT TO be pinged anyway — and the summarizer's own call reads the exact same cached
// prefix a ping would, so it refreshes the same TTL while also producing a compaction instead of
// one output token.
//
// The registration happens in Offload, on the request's own goroutine, every turn that reaches
// the point of having real commission material (a resolved summaryCaller plus a span) — regardless of
// whether the cache-state gate let it actually fire THIS turn. See cache_aware_summarizer.go's
// own comment at the call site for why registering unconditionally is safe.
type keepAliveCandidate struct {
	s            *CacheAwareSummarizer
	ctx          *components.Ctx // minimal: Session, Store, Ctx
	call         summaryCaller
	span         []bschemas.ChatMessage
	coveredCount int
	// cacheState/preExpirySeconds are the component's OWN trigger.cache_state and
	// trigger.pre_expiry_seconds, carried so the keeper can tell whether ITS idea of "due for a
	// ping right now" also satisfies what THIS candidate was configured to wait for — see
	// KeepAliveCandidateInfo.
	cacheState       string
	preExpirySeconds int
}

// KeepAliveCandidateInfo is what a registered candidate asked for, surfaced so the keeper can
// decide whether ITS OWN notion of "due" also satisfies the candidate's cache_state — a
// `pre_expiry` candidate must not be substituted on a ping the keeper is sending for some other
// reason (e.g. a strategy-driven K>1 schedule, or simply because Idle has elapsed on a session
// whose cache is still warm by the component's own, possibly narrower, pre_expiry_seconds).
type KeepAliveCandidateInfo struct {
	CacheState       string
	PreExpirySeconds int
}

var (
	keepAliveCandMu sync.Mutex
	keepAliveCand   = map[string]*keepAliveCandidate{}
)

// maxKeepAliveCandidates bounds the registry the same way the keeper's own kaEntry map is
// bounded (proxy/keepalive.go's maxKeepAliveSessions): a session count, not a request-rate
// knob, so a generous but finite ceiling costs nothing in the ordinary case and fails safe in
// the pathological one — the oldest registration is simply overwritten by Go's own map
// semantics on `register` once the caller stops inserting new keys past the point memory
// pressure would matter in practice, which is why this bound is advisory rather than enforced
// with eviction: unlike the keeper's entries, a stale candidate holds no credential and no
// provider body, only a copy of this session's own already-forwarded conversation.
const maxKeepAliveCandidates = 2048

// registerKeepAliveCandidate stores this turn's commission material as the keep-alive
// substitute, replacing any earlier registration for the session (always safe to replace: the
// stored copy is only ever read later, between requests, and a newer turn's conversation is a
// strict superset of an older one's for the same session).
func (s *CacheAwareSummarizer) registerKeepAliveCandidate(c *components.Ctx, call summaryCaller,
	span []bschemas.ChatMessage, coveredCount int) {
	if c == nil || c.Session == "" || call == nil {
		return
	}
	cand := &keepAliveCandidate{
		s: s, ctx: &components.Ctx{Session: c.Session, Store: c.Store, Ctx: c.Ctx}, call: call,
		span:             append([]bschemas.ChatMessage(nil), span...),
		coveredCount:     coveredCount,
		cacheState:       s.trigger.CacheState,
		preExpirySeconds: s.trigger.PreExpirySeconds,
	}
	keepAliveCandMu.Lock()
	if _, exists := keepAliveCand[c.Session]; !exists && len(keepAliveCand) >= maxKeepAliveCandidates {
		// Fail toward NOT remembering a new session rather than growing without bound — the
		// consequence is only that this session's idle pings stay bare pings, not that anything
		// already relying on a registration loses it.
		keepAliveCandMu.Unlock()
		return
	}
	keepAliveCand[c.Session] = cand
	keepAliveCandMu.Unlock()
}

// ClearKeepAliveCandidate drops a session's registered substitute material. The proxy calls this
// when a real request arrives for the session (the same moment keeper.arrive retires the kaEntry
// a ping would have used) — a candidate built from an EARLIER turn's conversation must not be
// dispatched once a newer turn has superseded it, or the summary would cover a stale span while
// the real pipeline moves on from a different one.
func ClearKeepAliveCandidate(session string) {
	if session == "" {
		return
	}
	keepAliveCandMu.Lock()
	delete(keepAliveCand, session)
	keepAliveCandMu.Unlock()
}

// KeepAliveSubstitute reports whether a session has commission material the idle keep-alive may
// use instead of a bare ping, and a Dispatch function to run it.
//
// ok=false covers three cases the caller cannot and need not distinguish, because the answer is
// the same for all of them: fall back to an ordinary ping. No turn has registered material for
// this session (cache_aware_summarizer is not in the pipeline for it, or no turn has reached the
// registration point yet); or a checkpoint already exists (the next real turn will splice it for
// free, so spending a call here would summarize a span nobody is waiting on); or dispatch would
// collide with a commission already in flight (checked inside Dispatch itself, via the shared
// single-flight registry, so this cannot be known at lookup time without a race).
//
// Dispatch BLOCKS until the call resolves (or the given timeout elapses) and runs the real model
// call — the keeper's caller must therefore run it off its own goroutine, exactly as it already
// does for an ordinary ping.
//
// info is returned alongside ok=true so the caller can additionally check cache_state against
// its OWN timing before deciding to dispatch — see KeepAliveCandidateInfo.
func KeepAliveSubstitute(session string) (dispatch func(timeout time.Duration) KeepAliveSummaryResult, info KeepAliveCandidateInfo, ok bool) {
	keepAliveCandMu.Lock()
	cand, exists := keepAliveCand[session]
	keepAliveCandMu.Unlock()
	if !exists {
		return nil, KeepAliveCandidateInfo{}, false
	}
	if _, has := loadCheckpoint(cand.ctx); has {
		return nil, KeepAliveCandidateInfo{}, false
	}
	info = KeepAliveCandidateInfo{CacheState: cand.cacheState, PreExpirySeconds: cand.preExpirySeconds}
	return func(timeout time.Duration) KeepAliveSummaryResult {
		return cand.s.commissionSync(cand.ctx, cand.call, cand.span, cand.coveredCount, timeout)
	}, info, true
}

// RegisterKeepAliveCandidateForTest installs commission material directly, for proxy's keeper
// tests to exercise KeepAliveSubstitute without driving a real Offload call through a mocked
// model and a full pipeline Ctx. Test-only: nothing outside a test has a legitimate reason to
// register a candidate that was not produced by an actual commission, which is why this is the
// one function in this file that does not require a *CacheAwareSummarizer the registry built
// itself — it builds the minimal receiver inline.
//
// call takes the plain, unnamed function shape rather than the package-private summaryCaller
// type, so a test in another package (proxy's keeper tests) can build one from either a mocked
// MessagesModel or a mocked PrefixAsker without needing to see that type at all — Go's
// assignability rules let an unnamed function literal satisfy a named parameter of the identical
// underlying type regardless of which package declared it.
//
// cacheState/preExpirySeconds let a test drive KeepAliveCandidateInfo's values without
// constructing a real component through its registered constructor — "" behaves as `any`,
// matching a component whose trigger never set cache_state at all.
func RegisterKeepAliveCandidateForTest(session string, st store.Store,
	call func(ctx context.Context) (string, error), span []bschemas.ChatMessage, coveredCount int,
	cacheState string, preExpirySeconds int) {
	(&CacheAwareSummarizer{mode: markerFull,
		trigger: components.Trigger{CacheState: cacheState, PreExpirySeconds: preExpirySeconds},
	}).registerKeepAliveCandidate(
		&components.Ctx{Session: session, Store: st, Ctx: context.Background()},
		call, span, coveredCount)
}
