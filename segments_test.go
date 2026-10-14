package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// Tests with a fake IntroDB server: query parameters and response conversion, caching, throttling, submit conditions and content. Names and IDs are all made up.

type fakeIntroDB struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string       // "METHOD path?query"
	headers  []http.Header  // same order as requests
	bodies   []string       // POST bodies
	segments map[string]any // "imdb|season|episode" or "imdb|movie" -> response (a nil value returns 404)
	status   map[string]int // path -> fixed response status code (takes precedence)
	submit   map[string]any // segment_type -> /submit response
}

func newFakeIntroDB(t *testing.T) *fakeIntroDB {
	f := &fakeIntroDB{segments: map[string]any{}, status: map[string]int{}, submit: map[string]any{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode())
		f.headers = append(f.headers, r.Header.Clone())
		b, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(b))
		if code := f.status[r.URL.Path]; code != 0 {
			if code == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "42")
			}
			w.WriteHeader(code)
			return
		}
		switch r.URL.Path {
		case "/segments":
			q := r.URL.Query()
			key := q.Get("imdb_id") + "|" + q.Get("season") + "|" + q.Get("episode")
			if q.Get("is_movie") == "true" {
				key = q.Get("imdb_id") + "|movie"
			}
			v, ok := f.segments[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		case "/submit":
			var in struct {
				SegmentType string `json:"segment_type"`
			}
			_ = json.Unmarshal(b, &in)
			v, ok := f.submit[in.SegmentType]
			if !ok {
				v = map[string]any{"ok": true, "submission": map[string]any{"status": "pending"}}
			}
			_ = json.NewEncoder(w).Encode(v)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIntroDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeIntroDB) plugin(t *testing.T, values map[string]string) *plugin {
	t.Helper()
	p := newPlugin()
	p.api.http = f.Client()
	p.api.interval = 0
	v := map[string]string{keyBaseURL: f.URL}
	for k, x := range values {
		v[k] = x
	}
	resp, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: v})
	if err != nil || len(resp.GetFieldErrors()) > 0 {
		t.Fatalf("Configure = %v, %v", resp, err)
	}
	return p
}

func episode(imdb string, s, e int32) *pluginv1.SegmentTarget {
	return &pluginv1.SegmentTarget{Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, ProviderIds: map[string]string{"imdb": imdb, "tmdb": "1234"}, SeasonNumber: &s, EpisodeNumber: &e, DurationMs: 3_000_000}
}

func seg(startMS, endMS int64, conf float64, n int) map[string]any {
	return map[string]any{"start_ms": startMS, "end_ms": endMS, "start_sec": float64(startMS) / 1000, "end_sec": float64(endMS) / 1000, "confidence": conf, "submission_count": n}
}

func TestGetSegments(t *testing.T) {
	f := newFakeIntroDB(t)
	f.segments["tt0000001|2|5"] = map[string]any{"imdb_id": "tt0000001", "season": 2, "episode": 5,
		"intro": seg(12_000, 92_000, 0.8, 7), "recap": seg(0, 11_000, 0.4, 2), "outro": seg(2_700_000, 2_790_000, 5, 1), "post_credits": seg(2_800_000, 2_850_000, 1, 1)}
	f.segments["tt0000002|movie"] = map[string]any{"intro": nil, "recap": nil, "outro": seg(7_000_000, 7_100_000, 0.9, 3),
		"post_credits": nil}
	f.segments["tt0000003|1|1"] = map[string]any{"intro": seg(5_000, 5_000, 1, 1), "recap": map[string]any{"start_sec": 3.5, "end_sec": 9.25}, "outro": seg(-5, 100, 1, 1)}
	p := f.plugin(t, nil)
	ctx := context.Background()

	resp, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000001", 2, 5)})
	if err != nil || len(resp.GetCandidates()) != 3 {
		t.Fatalf("candidates = %v, %v; want intro, recap and outro (post-credits is not mapped)", resp.GetCandidates(), err)
	}
	by := map[pluginv1.SegmentKind]*pluginv1.SegmentCandidate{}
	for _, c := range resp.GetCandidates() {
		by[c.GetKind()] = c
	}
	in := by[pluginv1.SegmentKind_SEGMENT_KIND_INTRO]
	if in.GetStartMs() != 12_000 || in.GetEndMs() != 92_000 || in.GetDurationMs() != 0 || in.GetConfidence() != 0.8 || in.GetVotes() != 7 {
		t.Errorf("intro = %v; the file duration is unknown so the host must verify it", in)
	}
	if by[pluginv1.SegmentKind_SEGMENT_KIND_OUTRO].GetConfidence() != 0 {
		t.Errorf("a confidence outside 0–1 must be reported as unknown, got %v", by[pluginv1.SegmentKind_SEGMENT_KIND_OUTRO].GetConfidence())
	}
	if !strings.Contains(f.requests[0], "GET /segments?episode=5&imdb_id=tt0000001&season=2") {
		t.Errorf("request = %q", f.requests[0])
	}
	if f.headers[0].Get("X-API-Key") != "" || !strings.HasPrefix(f.headers[0].Get("User-Agent"), "KryspalMedia-introdb/") {
		t.Errorf("a lookup must not send a key; headers = %v", f.headers[0])
	}
	// Cache: asking about the same episode again sends no request.
	if _, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000001", 2, 5)}); err != nil || f.count() != 1 {
		t.Errorf("the repeated lookup made %d requests", f.count())
	}
	// Movie: is_movie=true, without season and episode.
	mv, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: &pluginv1.SegmentTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"imdb": "TT0000002"}}})
	if err != nil || len(mv.GetCandidates()) != 1 || mv.GetCandidates()[0].GetKind() != pluginv1.SegmentKind_SEGMENT_KIND_OUTRO || !strings.Contains(f.requests[1], "imdb_id=tt0000002&is_movie=true") {
		t.Errorf("movie = %v, %v, request %q", mv.GetCandidates(), err, f.requests[1])
	}
	// Implausible ranges are dropped; with only start_sec/end_sec, convert from seconds.
	odd, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000003", 1, 1)})
	if err != nil || len(odd.GetCandidates()) != 1 || odd.GetCandidates()[0].GetStartMs() != 3500 || odd.GetCandidates()[0].GetEndMs() != 9250 {
		t.Errorf("odd = %v, %v", odd.GetCandidates(), err)
	}
	// No data (404) is an empty list, also cached.
	before := f.count()
	for i := 0; i < 2; i++ {
		none, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000009", 1, 1)})
		if err != nil || len(none.GetCandidates()) != 0 {
			t.Errorf("no data = %v, %v", none.GetCandidates(), err)
		}
	}
	if f.count() != before+1 {
		t.Errorf("a 404 was not cached: %d requests", f.count()-before)
	}
}

// Targets that cannot be queried send no request: no IMDb ID, wrong format, an episode without season/episode, season/episode below 1.
func TestGetSegmentsSkipsWhatItCannotLookUp(t *testing.T) {
	f := newFakeIntroDB(t)
	p := f.plugin(t, nil)
	one, zero := int32(1), int32(0)
	for name, tg := range map[string]*pluginv1.SegmentTarget{
		"no imdb":       {Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, ProviderIds: map[string]string{"tmdb": "1"}, SeasonNumber: &one, EpisodeNumber: &one},
		"bad imdb":      {Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, ProviderIds: map[string]string{"imdb": "12345"}, SeasonNumber: &one, EpisodeNumber: &one},
		"no season":     {Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, ProviderIds: map[string]string{"imdb": "tt0000001"}},
		"season zero":   {Type: pluginv1.ItemType_ITEM_TYPE_EPISODE, ProviderIds: map[string]string{"imdb": "tt0000001"}, SeasonNumber: &zero, EpisodeNumber: &one},
		"series target": {Type: pluginv1.ItemType_ITEM_TYPE_SERIES, ProviderIds: map[string]string{"imdb": "tt0000001"}},
		"nil target":    nil,
	} {
		resp, err := p.GetSegments(context.Background(), &pluginv1.GetSegmentsRequest{Target: tg})
		if err != nil || len(resp.GetCandidates()) != 0 {
			t.Errorf("%s: %v, %v", name, resp, err)
		}
	}
	if f.count() != 0 {
		t.Errorf("%d requests for targets that cannot be looked up", f.count())
	}
}

// Throttling and upstream errors: 429 returns RESOURCE_EXHAUSTED (with Retry-After), 5xx returns unavailable, neither cached; at least interval between two requests, one at a time.
func TestGetSegmentsErrorsAndPacing(t *testing.T) {
	f := newFakeIntroDB(t)
	p := f.plugin(t, nil)
	ctx := context.Background()
	f.status["/segments"] = http.StatusTooManyRequests
	_, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000001", 1, 1)})
	if rl, ok := errors.AsType[*sdk.RateLimitError](err); !ok || rl.RetryAfter != 42*time.Second {
		t.Errorf("429 = %v", err)
	}
	f.status["/segments"] = http.StatusBadGateway
	if _, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000001", 1, 1)}); err == nil {
		t.Error("a 502 must be an error")
	}
	delete(f.status, "/segments")
	f.segments["tt0000001|1|1"] = map[string]any{"intro": seg(1000, 2000, 1, 1)}
	if resp, err := p.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000001", 1, 1)}); err != nil || len(resp.GetCandidates()) != 1 {
		t.Errorf("errors must not be cached: %v %v", resp, err)
	}

	// Spacing: at least interval between two requests (also for concurrent calls).
	q := f.plugin(t, nil)
	q.api.interval = 120 * time.Millisecond
	start := time.Now()
	var wg sync.WaitGroup
	for i := int32(1); i <= 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: episode("tt0000005", 1, i)})
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < 220*time.Millisecond {
		t.Errorf("three requests took %v, want at least two intervals", d)
	}
}

func TestSubmitSegments(t *testing.T) {
	ctx := context.Background()
	f := newFakeIntroDB(t)
	segs := []*pluginv1.SegmentCandidate{
		{Kind: pluginv1.SegmentKind_SEGMENT_KIND_INTRO, StartMs: 12_500, EndMs: 92_000},
		{Kind: pluginv1.SegmentKind_SEGMENT_KIND_OUTRO, StartMs: 2_700_000, EndMs: 2_790_000},
		{Kind: pluginv1.SegmentKind_SEGMENT_KIND_PREVIEW, StartMs: 1, EndMs: 2}, // no matching kind
		{Kind: pluginv1.SegmentKind_SEGMENT_KIND_RECAP, StartMs: 9, EndMs: 3},   // implausible
	}
	req := &pluginv1.SubmitSegmentsRequest{Target: episode("tt0000001", 2, 5), Segments: segs}

	// Off by default; no submit without a key either (Configure blocks submit being on without a key).
	if _, err := f.plugin(t, nil).SubmitSegments(ctx, req); err == nil {
		t.Error("uploads are off by default")
	}
	bad := newPlugin()
	resp, err := bad.Configure(ctx, &pluginv1.ConfigureRequest{Values: map[string]string{keySubmit: "true"}})
	if err != nil || resp.GetFieldErrors()[keySubmit] == "" {
		t.Errorf("turning uploads on without a key must be refused: %v %v", resp, err)
	}
	if f.count() != 0 {
		t.Fatal("something was sent while uploads were off")
	}

	p := f.plugin(t, map[string]string{keySubmit: "true", keyAPIKey: "idb_test_key_1234"})
	f.submit["outro"] = map[string]any{"ok": true, "submission": map[string]any{"status": "rejected"}}
	got, err := p.SubmitSegments(ctx, req)
	if err != nil || got.GetAccepted() != 1 {
		t.Fatalf("SubmitSegments = %v, %v; want the intro accepted, the outro rejected, the others not sent", got, err)
	}
	if f.count() != 2 {
		t.Fatalf("%d requests, want 2 (intro and outro only): %v", f.count(), f.requests)
	}
	if f.headers[0].Get("X-API-Key") != "idb_test_key_1234" || f.headers[0].Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", f.headers[0])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"segment_type": "intro", "imdb_id": "tt0000001", "start_sec": 12.5, "end_sec": 92.0, "is_movie": false, "season": 2.0, "episode": 5.0, "tmdb_id": 1234.0}
	if len(body) != len(want) {
		t.Errorf("body = %v; only these fields may be sent: %v (no file names, paths or durations)", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}

	// Movie: is_movie is true, no season/episode.
	f.mu.Lock()
	f.bodies = nil
	f.mu.Unlock()
	mv := &pluginv1.SubmitSegmentsRequest{Target: &pluginv1.SegmentTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE, ProviderIds: map[string]string{"imdb": "tt0000002"}}, Segments: segs[:1]}
	if _, err := p.SubmitSegments(ctx, mv); err != nil || strings.Contains(f.bodies[0], "season") || !strings.Contains(f.bodies[0], `"is_movie":true`) {
		t.Errorf("movie upload: %v, body %s", err, f.bodies)
	}
	// The target has no IMDb ID: not sent.
	if _, err := p.SubmitSegments(ctx, &pluginv1.SubmitSegmentsRequest{Target: episode("", 1, 1), Segments: segs[:1]}); err == nil {
		t.Error("an upload without an IMDb id must be refused")
	}
	// A rejected key (401) returns an error; the per-episode per-kind rate limit (429) and rejected content (400) skip that entry.
	f.status["/submit"] = http.StatusUnauthorized
	if _, err := p.SubmitSegments(ctx, req); err == nil {
		t.Error("a rejected key must be an error")
	}
	f.status["/submit"] = http.StatusTooManyRequests
	if got, err := p.SubmitSegments(ctx, req); err != nil || got.GetAccepted() != 0 {
		t.Errorf("rate limited upload = %v, %v", got, err)
	}
	f.status["/submit"] = http.StatusBadRequest
	if got, err := p.SubmitSegments(ctx, req); err != nil || got.GetAccepted() != 0 {
		t.Errorf("a refused payload = %v, %v", got, err)
	}
}
