package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielbodart/gokit/clock"
	"github.com/danielbodart/gokit/logx"
)

// Decorator wraps a RoundTripper in another of the same shape: what a client
// does around every request - naming itself, retrying, logging - kept out of
// both the transport and the code that makes the requests.
type Decorator func(http.RoundTripper) http.RoundTripper

// Decorate wraps transport in decorators, in the order a request passes
// through them: the first is outermost, the last is next to transport.
//
// Decorate(transport, Logging(logger, nil), Retrying(policy)) logs once
// around all of a request's attempts; the other way round would log each.
func Decorate(transport http.RoundTripper, decorators ...Decorator) http.RoundTripper {
	for i := len(decorators) - 1; i >= 0; i-- {
		transport = decorators[i](transport)
	}
	return transport
}

// Identifying names the client to the server as product, such as
// "treesinger/1.2.0", rather than as Go's generic "Go-http-client/1.1". A
// request that names itself already is left as it is.
func Identifying(product string) Decorator {
	return func(inner http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("User-Agent") != "" {
				return inner.RoundTrip(request)
			}
			// A RoundTripper must not change the request it was given.
			identified := request.Clone(request.Context())
			identified.Header.Set("User-Agent", product)
			return inner.RoundTrip(identified)
		})
	}
}

// RetryPolicy says how often, and after how long, Retrying tries again.
type RetryPolicy struct {
	// How many times to try again after the first attempt.
	Retries int
	// The first backoff, doubled for each retry after it.
	BaseDelay time.Duration
	// The longest Retrying will wait before any one retry. A backoff is cut
	// to it; a Retry-After beyond it is not waited out at all, and the
	// response is returned. Zero is the longest backoff the policy would
	// wait of its own accord, so a server may never make it wait longer.
	MaxDelay time.Duration
	// What the waits are waited on; clock.System when nil.
	Clock clock.Clock
}

// DefaultRetryPolicy is two retries, 200ms then 400ms apart, waiting out a
// Retry-After of up to five seconds.
var DefaultRetryPolicy = RetryPolicy{Retries: 2, BaseDelay: 200 * time.Millisecond, MaxDelay: 5 * time.Second}

// Retrying tries a request again when it failed in a way that may pass: a
// transport error, a 429 or a 5xx. It waits BaseDelay before the first retry
// and twice as long before each after it, at most Retries times, and then
// returns the last response or error as it came.
//
// A 429 or 503 carrying Retry-After - seconds, or an HTTP-date - is waited
// out as the server asked, in place of the backoff: Docker Hub throttles
// anonymous pulls routinely, and its Retry-After is when it will answer. One
// beyond MaxDelay is not waited out, and the response is returned at once,
// Retry-After and all. Waiting the cap and asking anyway would only spend an
// attempt on an answer the server has already given, and returning lets the
// caller say "rate limited" now - a daemon tries again next pass - rather
// than hold whatever is waiting on it for as long as a server likes.
//
// The request's own context ends it: a request cancelled, or past its
// deadline, is never retried, and a wait gives up as soon as it is done.
// Under an http.Client with a Timeout, that bounds every attempt and every
// wait together.
//
// The body is read once and each attempt sends a fresh copy of it, so a
// retry resends a POST in full. Which requests are safe to send twice is the
// caller's to decide, by which client it sends them through. Each response
// given up on is drained and closed, so its connection goes back to the
// pool.
func Retrying(policy RetryPolicy) Decorator {
	waits := policy.Clock
	if waits == nil {
		waits = clock.System{}
	}
	limit := policy.MaxDelay
	if limit <= 0 {
		limit = backoff(policy.BaseDelay, policy.Retries-1, 0)
	}
	return func(inner http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, err := buffer(request)
			if err != nil {
				return nil, err
			}
			ctx := request.Context()
			for attempt := 0; ; attempt++ {
				response, err := inner.RoundTrip(withBody(request, body))
				if ctx.Err() != nil || attempt >= policy.Retries || !transient(response, err) {
					return response, err
				}
				wait := backoff(policy.BaseDelay, attempt, limit)
				if err == nil && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable) {
					if wait = RetryAfter(response.Header.Get("Retry-After"), waits.Now(), wait); wait > limit {
						return response, nil
					}
				}
				if response != nil {
					drain(response)
				}
				if err := clock.Sleep(ctx, waits, wait); err != nil {
					return nil, err
				}
			}
		})
	}
}

// transient is whether an attempt failed in a way that may pass.
func transient(response *http.Response, err error) bool {
	return err != nil || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
}

// backoff is base doubled attempt times, no more than limit when limit is
// positive, and never overflowing.
func backoff(base time.Duration, attempt int, limit time.Duration) time.Duration {
	wait := max(base, 0)
	for range max(attempt, 0) {
		if wait > math.MaxInt64/2 {
			wait = math.MaxInt64
			break
		}
		wait *= 2
	}
	if limit > 0 {
		wait = min(wait, limit)
	}
	return wait
}

// RetryAfter is how long a Retry-After header asks a client to wait.
//
// The header is a whole number of seconds or an HTTP-date, and nothing else:
// a fraction or a sign is not the grammar. Missing or unparseable is
// fallback, rather than no wait at all, so a malformed header never has a
// client hammer the server; a date in the past is no wait.
func RetryAfter(header string, now time.Time, fallback time.Duration) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return fallback
	}
	if strings.Trim(header, "0123456789") == "" {
		seconds, err := strconv.ParseInt(header, 10, 64)
		if err != nil || seconds > math.MaxInt64/int64(time.Second) {
			return math.MaxInt64 // more digits than a duration holds
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(0, at.Sub(now))
	}
	return fallback
}

// buffer reads and closes a request's body, nil when it has none.
func buffer(request *http.Request) ([]byte, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return nil, nil
	}
	defer request.Body.Close()
	return io.ReadAll(request.Body)
}

// withBody is a copy of request carrying its own reader over body.
func withBody(request *http.Request, body []byte) *http.Request {
	attempt := request.Clone(request.Context())
	if body != nil {
		attempt.Body = io.NopCloser(bytes.NewReader(body))
		attempt.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		attempt.ContentLength = int64(len(body))
	}
	return attempt
}

// drain reads what is left of a response given up on, up to a limit - a
// body that never ends must not hang the retry - and closes it.
func drain(response *http.Response) {
	io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
}

// Logging logs each request as `METHOD URL → status (Nms)`, through Log, and
// one that fails without a response as `METHOD URL → threw` and the error,
// through Error - except a request its own caller cancelled, which is no
// fault of the server's, and is passed on unlogged.
//
// The duration is measured on clk, clock.System when nil, so a test reads
// the time a stopped or instant clock says it took. Under an http.Client it
// sees each redirect as a request of its own.
func Logging(logger logx.Logger, clk clock.Clock) Decorator {
	if clk == nil {
		clk = clock.System{}
	}
	return func(inner http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
			started := clk.Now()
			response, err := inner.RoundTrip(request)
			if err != nil {
				if !cancelled(request, err) {
					logger.Error(fmt.Sprintf("%s %s → threw", request.Method, request.URL), err)
				}
				return nil, err
			}
			logger.Log(fmt.Sprintf("%s %s → %d (%dms)", request.Method, request.URL, response.StatusCode, clk.Now().Sub(started).Milliseconds()))
			return response, nil
		})
	}
}

// cancelled is whether err is the request's own context being cancelled.
func cancelled(request *http.Request, err error) bool {
	return errors.Is(err, context.Canceled) && request.Context().Err() != nil
}
