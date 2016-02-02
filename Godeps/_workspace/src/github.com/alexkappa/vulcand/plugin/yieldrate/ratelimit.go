package yieldrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexkappa/vulcand/pkg/cache"
	gocache "github.com/patrickmn/go-cache"
	"github.com/vulcand/vulcand/Godeps/_workspace/src/github.com/codegangsta/cli"
	"github.com/vulcand/vulcand/Godeps/_workspace/src/github.com/mailgun/log"
	"github.com/vulcand/vulcand/plugin"
)

func GetSpec() *plugin.MiddlewareSpec {
	return &plugin.MiddlewareSpec{
		Type:      "yieldrate",
		FromOther: FromOther,
		FromCli:   FromCli,
		CliFlags:  CliFlags(),
	}
}

func CliFlags() []cli.Flag {
	return []cli.Flag{
		cli.IntFlag{
			Name:  "requests",
			Usage: "Rate limit number of requests",
			Value: 100,
		},
		cli.DurationFlag{
			Name:  "duration",
			Usage: "Rate limit reset duration",
			Value: 1 * time.Minute,
		},
	}
}

// FromOther constructs the middleware from another middleware struct, typically
// originating from a JSON object.
func FromOther(o RateLimitSpec) (plugin.Middleware, error) {
	return NewRateLimitSpec(o.Requests, o.Duration), nil
}

// FromCli constructs the middleware from the command line
func FromCli(c *cli.Context) (plugin.Middleware, error) {
	return NewRateLimitSpec(
		c.Int("requests"),
		c.Duration("duration"),
	), nil
}

type RateLimitSpec struct {
	Requests int
	Duration time.Duration
}

// NewRateLimitSpec creates the ratelimit middleware.
func NewRateLimitSpec(requests int, duration time.Duration) *RateLimitSpec {
	return &RateLimitSpec{requests, duration}
}

// NewHandler is required by vulcand to register itself as frontend middleware.
func (rls *RateLimitSpec) NewHandler(next http.Handler) (http.Handler, error) {
	return NewRateLimitHandler(rls, next), nil
}

// String is a helper method used by vulcand log.
func (rls *RateLimitSpec) String() string {
	return fmt.Sprintf("ratelimit=%dreq/%s", rls.Requests, rls.Duration)
}

// RateLimitHandler is a http.Handler that executes the ratelimit middleware.
type RateLimitHandler struct {
	spec  *RateLimitSpec
	cache cache.Cache
	next  http.Handler
}

// ServeHTTP satifies the http.Handler interface.
func (rlh *RateLimitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Debugf("%d items in cache", rlh.cache.(*gocache.Cache).ItemCount())
	rl := rlh.rateLimit(r)
	rl.Check()
	if rl.IsExceeded() {
		respond(w, rl, 429)
		return
	}
	rl.Decrement()
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(rl.Limit()))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(rl.Remaining()))
	h.Set("X-RateLimit-Reset", strconv.Itoa(rl.Reset()))
	rlh.next.ServeHTTP(w, r)
}

func (rlh *RateLimitHandler) rateLimit(r *http.Request) (rl *RateLimit) {
	key := clientIP(r)
	log.Debugf("cache key: %s", key)
	if value, ok := rlh.cache.Get(key); ok {
		rl = value.(*RateLimit)
	} else {
		rl = NewRateLimit(rlh.spec)
	}
	rlh.cache.Set(key, rl, cache.DefaultExpiration)
	return
}

// NewRateLimitHandler creates a new ratelimit handler with the given spec and
// the next handler in the chain.
func NewRateLimitHandler(spec *RateLimitSpec, next http.Handler) *RateLimitHandler {
	return &RateLimitHandler{
		spec:  spec,
		cache: cache.NewCache(1 * time.Minute),
		next:  next,
	}
}

// RateLimit represents a users rate limit state.
type RateLimit struct {
	spec         *RateLimitSpec
	resetTime    time.Time
	remainingReq int
	mux          sync.Mutex
}

// Check compares the reset time against the current time and if the former is
// earlier than the latter the reset time is reset to the current time plus the
// configured duration.
func (r *RateLimit) Check() {
	r.mux.Lock()
	defer r.mux.Unlock()
	t := time.Now()
	if r.resetTime.Before(t) {
		r.resetTime = t.Add(r.spec.Duration)
		r.remainingReq = r.spec.Requests
	}
}

// Decrement the remaining requests by one.
func (r *RateLimit) Decrement() {
	r.mux.Lock()
	defer r.mux.Unlock()
	r.remainingReq--
}

// IsExceeded reports whether the rate limit was exceeded, i.e. there are no
// more remaining requests.
func (r *RateLimit) IsExceeded() bool {
	r.mux.Lock()
	defer r.mux.Unlock()
	return r.remainingReq < 1
}

// Limit returns the number of allowed requests.
func (r *RateLimit) Limit() int {
	r.mux.Lock()
	defer r.mux.Unlock()
	return r.spec.Requests
}

// Remaining returns the number of remaining requests.
func (r *RateLimit) Remaining() int {
	r.mux.Lock()
	defer r.mux.Unlock()
	return r.remainingReq
}

// Reset returns the number of seconds remaining until the rate limit is reset.
func (r *RateLimit) Reset() int {
	r.mux.Lock()
	defer r.mux.Unlock()
	return int(r.resetTime.Sub(time.Now()).Seconds())
}

// MarshalJSON marshals the rate limit struct in a format appropriate for a user
// to consume.
func (r *RateLimit) MarshalJSON() ([]byte, error) {
	r.mux.Lock()
	defer r.mux.Unlock()
	var buffer bytes.Buffer
	fmt.Fprintf(
		&buffer,
		`{"rate":{"limit":%d,"remaining":%d, "reset":%d}}`,
		r.spec.Requests,
		r.remainingReq,
		int(r.resetTime.Sub(time.Now()).Seconds()))
	return buffer.Bytes(), nil
}

// NewRateLimit creates a new RateLimit.
func NewRateLimit(spec *RateLimitSpec) *RateLimit {
	return &RateLimit{
		spec:         spec,
		resetTime:    time.Now().Add(spec.Duration),
		remainingReq: spec.Requests,
	}
}

func respond(w http.ResponseWriter, v interface{}, code int) {
	b, _ := json.Marshal(v)
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}

func clientIP(r *http.Request) string {
	if ips := r.Header.Get("X-Forwarded-For"); ips != "" {
		for _, ip := range strings.Split(ips, ",") {
			return discardPort(strings.Trim(ip, " "))
		}
	}
	return discardPort(r.RemoteAddr)
}

func discardPort(s string) string {
	i := strings.LastIndex(s, ":")
	if i == -1 {
		return s
	}
	return s[:i]
}
