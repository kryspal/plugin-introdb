package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
	"github.com/kryspal/plugin-sdk/sdk"
)

// The IntroDB API (https://api.introdb.app, OpenAPI at /openapi.json):
//
//	GET  /segments?imdb_id=tt…&season=&episode= (movies is_movie=true)  no key needed; returns intro, recap, outro, post_credits (null or
//	     {start_ms, end_ms, start_sec, end_sec, confidence, submission_count}); 404 means no data.
//	POST /submit  header X-API-Key; {segment_type, imdb_id, start_sec, end_sec, is_movie, season, episode, tmdb_id};
//	     returns {ok, submission:{status: pending|accepted|rejected}}; 400 invalid content, 401 wrong key, 429 once per 5 minutes for the same episode and kind.

const (
	maxResponseBytes = 1 << 20
	requestTimeout   = 10 * time.Second
	// cacheTTL is how long query results (including "no data") stay in memory: the host asks again about an episode with no result only after a week, so this only blocks duplicate requests.
	cacheTTL     = 24 * time.Hour
	cacheEntries = 4096
	// defaultRetryAfter is the pause time when a 429 carries no Retry-After.
	defaultRetryAfter = time.Minute
)

var imdbRE = regexp.MustCompile(`^tt[0-9]{7,8}$`)

// api is the IntroDB HTTP client: one request at a time, at least interval between two requests.
type api struct {
	http     *http.Client
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newAPI() *api {
	return &api{http: &http.Client{Timeout: requestTimeout}, interval: minInterval}
}

// do sends the request and returns the status code; on 200 it parses the JSON into out. A 429 returns sdk.RateLimited; 5xx and connection errors return sdk.Unavailable.
func (a *api) do(ctx context.Context, method, rawURL string, header map[string]string, body []byte, out any) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d := time.Until(a.last.Add(a.interval)); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return 0, ctx.Err()
		}
	}
	a.last = time.Now()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return 0, sdk.Unavailable("introdb: bad request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "KryspalMedia-introdb/"+version())
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // url.Error carries the full URL; keep only the cause
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, sdk.Unavailable("introdb: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		d := defaultRetryAfter
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			d = time.Duration(s) * time.Second
		}
		return resp.StatusCode, sdk.RateLimited(d, "introdb: rate limit reached")
	case resp.StatusCode >= 500:
		return resp.StatusCode, sdk.Unavailable(fmt.Sprintf("introdb: %s", resp.Status))
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return resp.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, sdk.Unavailable("introdb: " + err.Error())
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, sdk.Unavailable("introdb: bad response")
		}
	}
	return resp.StatusCode, nil
}

// ---- Query ----

type introdbSegment struct {
	StartMS         int64   `json:"start_ms"`
	EndMS           int64   `json:"end_ms"`
	StartSec        float64 `json:"start_sec"`
	EndSec          float64 `json:"end_sec"`
	Confidence      float64 `json:"confidence"`
	SubmissionCount int32   `json:"submission_count"`
}

type segmentsResponse struct {
	Intro       *introdbSegment `json:"intro"`
	Recap       *introdbSegment `json:"recap"`
	Outro       *introdbSegment `json:"outro"`
	PostCredits *introdbSegment `json:"post_credits"`
}

func (s *introdbSegment) candidate(kind pluginv1.SegmentKind) (*pluginv1.SegmentCandidate, bool) {
	if s == nil {
		return nil, false
	}
	start, end := s.StartMS, s.EndMS
	if start == 0 && end == 0 {
		start, end = int64(s.StartSec*1000+0.5), int64(s.EndSec*1000+0.5)
	}
	if start < 0 || end <= start {
		return nil, false
	}
	// IntroDB does not distinguish versions and has no duration: DurationMs stays 0, and the host always verifies by spot-checking the audio fingerprint. The docs do not state the confidence scale; a value outside 0-1 is treated as not provided.
	conf := s.Confidence
	if conf < 0 || conf > 1 {
		conf = 0
	}
	return &pluginv1.SegmentCandidate{Kind: kind, StartMs: start, EndMs: end, Confidence: conf, Votes: max(0, s.SubmissionCount)}, true
}

type segmentEntry struct {
	cands   []*pluginv1.SegmentCandidate
	expires time.Time
}

// segmentCache is the in-memory cache of query results (local only, never exported); errors are not cached.
type segmentCache struct {
	mu      sync.Mutex
	entries map[string]segmentEntry
}

func (c *segmentCache) get(key string) ([]*pluginv1.SegmentCandidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !time.Now().Before(e.expires) {
		return nil, false
	}
	return e.cands, true
}

func (c *segmentCache) put(key string, cands []*pluginv1.SegmentCandidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= cacheEntries {
		c.entries = map[string]segmentEntry{}
	}
	c.entries[key] = segmentEntry{cands: cands, expires: time.Now().Add(cacheTTL)}
}

// targetKey turns the target into an IntroDB query: it needs an IMDb ID; an episode also needs season and episode (>= 1). Without them it cannot be queried (returns ok=false).
func targetKey(t *pluginv1.SegmentTarget) (imdb string, season, episode int32, movie, ok bool) {
	imdb = strings.ToLower(strings.TrimSpace(t.GetProviderIds()["imdb"]))
	if !imdbRE.MatchString(imdb) {
		return "", 0, 0, false, false
	}
	switch t.GetType() {
	case pluginv1.ItemType_ITEM_TYPE_MOVIE:
		return imdb, 0, 0, true, true
	case pluginv1.ItemType_ITEM_TYPE_EPISODE:
		if t.SeasonNumber == nil || t.EpisodeNumber == nil || t.GetSeasonNumber() < 1 || t.GetEpisodeNumber() < 1 {
			return "", 0, 0, false, false
		}
		return imdb, t.GetSeasonNumber(), t.GetEpisodeNumber(), false, true
	}
	return "", 0, 0, false, false
}

// GetSegments queries the candidates for an episode or a movie. No IMDb ID, an invalid season/episode, or no data at IntroDB all return an empty list (not an error).
func (p *plugin) GetSegments(ctx context.Context, req *pluginv1.GetSegmentsRequest) (*pluginv1.GetSegmentsResponse, error) {
	imdb, season, episode, movie, ok := targetKey(req.GetTarget())
	if !ok {
		return &pluginv1.GetSegmentsResponse{}, nil
	}
	_, _, base := p.settings()
	key := fmt.Sprintf("%s|%s|%d|%d|%v", base, imdb, season, episode, movie)
	if cands, hit := p.cache.get(key); hit {
		return &pluginv1.GetSegmentsResponse{Candidates: cands}, nil
	}
	q := url.Values{"imdb_id": {imdb}}
	if movie {
		q.Set("is_movie", "true")
	} else {
		q.Set("season", strconv.Itoa(int(season)))
		q.Set("episode", strconv.Itoa(int(episode)))
	}
	var body segmentsResponse
	status, err := p.api.do(ctx, http.MethodGet, base+"/segments?"+q.Encode(), nil, nil, &body)
	if err != nil {
		return nil, err
	}
	var cands []*pluginv1.SegmentCandidate
	if status == http.StatusOK {
		for _, x := range []struct {
			s *introdbSegment
			k pluginv1.SegmentKind
		}{{body.Intro, pluginv1.SegmentKind_SEGMENT_KIND_INTRO}, {body.Recap, pluginv1.SegmentKind_SEGMENT_KIND_RECAP}, {body.Outro, pluginv1.SegmentKind_SEGMENT_KIND_OUTRO}} {
			if c, ok := x.s.candidate(x.k); ok {
				cands = append(cands, c)
			}
		}
	}
	// 404 (no data) and other 4xx are all treated as no result; cache it so it is not asked again.
	p.cache.put(key, cands)
	return &pluginv1.GetSegmentsResponse{Candidates: cands}, nil
}

// ---- Submit ----

type submitBody struct {
	SegmentType string  `json:"segment_type"`
	IMDbID      string  `json:"imdb_id"`
	StartSec    float64 `json:"start_sec"`
	EndSec      float64 `json:"end_sec"`
	IsMovie     bool    `json:"is_movie"`
	Season      *int32  `json:"season,omitempty"`
	Episode     *int32  `json:"episode,omitempty"`
	TMDBID      int64   `json:"tmdb_id,omitempty"`
}

type submitResponse struct {
	OK         bool `json:"ok"`
	Submission struct {
		Status string `json:"status"`
	} `json:"submission"`
}

func segmentType(k pluginv1.SegmentKind) string {
	switch k {
	case pluginv1.SegmentKind_SEGMENT_KIND_INTRO:
		return "intro"
	case pluginv1.SegmentKind_SEGMENT_KIND_RECAP:
		return "recap"
	case pluginv1.SegmentKind_SEGMENT_KIND_OUTRO:
		return "outro"
	}
	return "" // Next-episode previews have no matching kind: not submitted
}

// SubmitSegments submits segments the administrator marked manually. It sends only when "submit" is turned on in the plugin settings and a key is set; it sends only the IMDb ID, (TMDB ID), season, episode, kind and start/end seconds,
// and never file names, paths or durations. It returns the number the other side accepted (status not rejected); rejected content (400) and the per-episode per-kind rate limit (429) skip that entry,
// and a wrong key (401) returns an error.
func (p *plugin) SubmitSegments(ctx context.Context, req *pluginv1.SubmitSegmentsRequest) (*pluginv1.SubmitSegmentsResponse, error) {
	key, enabled, base := p.settings()
	if !enabled || key == "" {
		return nil, sdk.InvalidArgument("introdb: uploads are not turned on in the plugin settings")
	}
	imdb, season, episode, movie, ok := targetKey(req.GetTarget())
	if !ok {
		return nil, sdk.InvalidArgument("introdb: uploads need an IMDb id and, for episodes, a season and an episode")
	}
	tmdb, _ := strconv.ParseInt(strings.TrimSpace(req.GetTarget().GetProviderIds()["tmdb"]), 10, 64)
	accepted := int32(0)
	for _, s := range req.GetSegments() {
		kind := segmentType(s.GetKind())
		if kind == "" || s.GetStartMs() < 0 || s.GetEndMs() <= s.GetStartMs() {
			continue
		}
		b := submitBody{SegmentType: kind, IMDbID: imdb, StartSec: float64(s.GetStartMs()) / 1000, EndSec: float64(s.GetEndMs()) / 1000, IsMovie: movie, TMDBID: max(0, tmdb)}
		if !movie {
			b.Season, b.Episode = &season, &episode
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, sdk.InvalidArgument("introdb: bad submission")
		}
		var out submitResponse
		status, err := p.api.do(ctx, http.MethodPost, base+"/submit", map[string]string{"X-API-Key": key, "Content-Type": "application/json"}, raw, &out)
		var rl *sdk.RateLimitError
		switch {
		case errors.As(err, &rl):
			return &pluginv1.SubmitSegmentsResponse{Accepted: accepted}, nil // Rate limited: those already accepted still count, the rest come next time
		case err != nil:
			return nil, err
		case status == http.StatusUnauthorized:
			return nil, sdk.InvalidArgument("introdb: IntroDB rejected the API key")
		case status == http.StatusOK && out.OK && out.Submission.Status != "rejected":
			accepted++
		}
	}
	return &pluginv1.SubmitSegmentsResponse{Accepted: accepted}, nil
}
