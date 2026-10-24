package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielbodart/gokit/clock"
	"github.com/danielbodart/gokit/logx/logtest"
)

var start = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

// answer is one scripted response: a status, or 0 for a dropped connection,
// and any headers.
type answer struct {
	status int
	header map[string]string
}

// upstream is a fake server answering each request with the next of its
// answers, the last one again once they run out, and recording each body it
// was sent.
type upstream struct {
	mu      sync.Mutex
	answers []answer
	bodies  []string
}

func answering(answers ...answer) *upstream { return &upstream{answers: answers} }

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, string(body))
	next := u.answers[min(len(u.bodies), len(u.answers))-1]
	u.mu.Unlock()
	if next.status == 0 {
		// HandlerTransport makes this a transport error, as a dropped
		// connection is.
		panic("connection reset")
	}
	for name, value := range next.header {
		w.Header().Set(name, value)
	}
	w.WriteHeader(next.status)
	io.WriteString(w, http.StatusText(next.status))
}

func (u *upstream) attempts() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func (u *upstream) sent() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.bodies)
}

func post(t *testing.T, transport http.RoundTripper, ctx context.Context, body string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/token", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return transport.RoundTrip(request)
}

// instantly is DefaultRetryPolicy on a clock whose waits are over at once.
func instantly() (RetryPolicy, *clock.Instant) {
	waits := clock.NewInstant(start)
	policy := DefaultRetryPolicy
	policy.Clock = waits
	return policy, waits
}

func TestDecorateAppliesTheFirstOutermost(t *testing.T) {
	var order []string
	naming := func(name string) Decorator {
		return func(inner http.RoundTripper) http.RoundTripper {
			return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
				order = append(order, name)
				return inner.RoundTrip(request)
			})
		}
	}
	transport := Decorate(HandlerTransport(answering(answer{status: 200})), naming("first"), naming("second"), naming("third"))
	if _, err := get(t, transport, context.Background(), "http://x/"); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, order, []string{"first", "second", "third"})
}

func TestDecorateWithNothingIsTheTransport(t *testing.T) {
	transport := HandlerTransport(answering(answer{status: 200}))
	if reflect.ValueOf(Decorate(transport)).Pointer() != reflect.ValueOf(transport).Pointer() {
		t.Error("wrapped in something")
	}
}

func TestIdentifyingNamesTheClient(t *testing.T) {
	var seen string
	transport := Identifying("treesinger/1.2.3")(HandlerTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
	})))
	request, _ := http.NewRequest(http.MethodGet, "http://x/", nil)

	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, seen, "treesinger/1.2.3")
	assertEqual(t, request.Header.Get("User-Agent"), "") // the caller's request is not changed
}

func TestIdentifyingLeavesARequestThatNamesItself(t *testing.T) {
	var seen string
	transport := Identifying("treesinger/1.2.3")(HandlerTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
	})))
	request, _ := http.NewRequest(http.MethodGet, "http://x/", nil)
	request.Header.Set("User-Agent", "curl/8")

	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, seen, "curl/8")
}

func TestRetrying(t *testing.T) {
	for _, test := range []struct {
		name     string
		answers  []answer
		attempts int
		status   int // 0 is the transport's error
		waits    []time.Duration
	}{
		{"a success is not retried", []answer{{status: 200}}, 1, 200, nil},
		{"a 4xx is not retried", []answer{{status: 404}}, 1, 404, nil},
		{"a 5xx is retried with exponential backoff", []answer{{status: 503}, {status: 502}, {status: 200}}, 3, 200, []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}},
		{"a 429 is retried", []answer{{status: 429}, {status: 200}}, 2, 200, []time.Duration{200 * time.Millisecond}},
		{"a transport error is retried", []answer{{status: 0}, {status: 200}}, 2, 200, []time.Duration{200 * time.Millisecond}},
		{"the last 5xx is returned once the retries run out", []answer{{status: 500}}, 3, 500, []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}},
		{"the last error is returned once the retries run out", []answer{{status: 0}}, 3, 0, []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := answering(test.answers...)
			policy, waits := instantly()

			response, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), "grant_type=refresh_token")

			if test.status == 0 {
				if err == nil || !strings.Contains(err.Error(), "connection reset") {
					t.Errorf("got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertEqual(t, response.StatusCode, test.status)
				assertEqual(t, read(t, response), http.StatusText(test.status)) // the response returned is whole
			}
			assertEqual(t, server.attempts(), test.attempts)
			assertEqual(t, waits.Waits(), test.waits)
		})
	}
}

// A request body is a stream read once; every attempt must send all of it.
func TestRetryingResendsTheWholeBodyEachAttempt(t *testing.T) {
	var getBody []bool
	server := answering(answer{status: 503}, answer{status: 503}, answer{status: 200})
	recording := RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		getBody = append(getBody, request.GetBody != nil)
		return HandlerTransport(server).RoundTrip(request)
	})
	policy, _ := instantly()

	if _, err := post(t, Retrying(policy)(recording), context.Background(), "grant_type=refresh_token"); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, server.sent(), []string{"grant_type=refresh_token", "grant_type=refresh_token", "grant_type=refresh_token"})
	assertEqual(t, getBody, []bool{true, true, true})
}

// closes records each response body closed, as a pooled connection would
// need.
type closes struct {
	mu     sync.Mutex
	closed []bool
}

func (c *closes) around(inner http.RoundTripper) http.RoundTripper {
	return RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response, err := inner.RoundTrip(request)
		if err == nil {
			c.mu.Lock()
			i := len(c.closed)
			c.closed = append(c.closed, false)
			c.mu.Unlock()
			response.Body = &closing{ReadCloser: response.Body, close: func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.closed[i] = true
			}}
		}
		return response, err
	})
}

type closing struct {
	io.ReadCloser
	close func()
}

func (c *closing) Close() error { c.close(); return c.ReadCloser.Close() }

func TestRetryingClosesEachResponseItGivesUpOn(t *testing.T) {
	bodies := &closes{}
	policy, _ := instantly()
	transport := Retrying(policy)(bodies.around(HandlerTransport(answering(answer{status: 503}, answer{status: 502}, answer{status: 200}))))

	response, err := post(t, transport, context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, bodies.closed, []bool{true, true, false}) // the one returned is the caller's to close
	response.Body.Close()
}

func TestRetryingWaitsOutA429sRetryAfterInSeconds(t *testing.T) {
	server := answering(answer{status: 429, header: map[string]string{"Retry-After": "3"}}, answer{status: 200})
	policy, waits := instantly()

	response, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, response.StatusCode, 200)
	assertEqual(t, waits.Waits(), []time.Duration{3 * time.Second})
}

func TestRetryingWaitsOutA503sRetryAfterAsADate(t *testing.T) {
	server := answering(answer{status: 503, header: map[string]string{"Retry-After": "Fri, 23 Oct 2026 12:00:02 GMT"}}, answer{status: 200})
	policy, waits := instantly()

	if _, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, waits.Waits(), []time.Duration{2 * time.Second})
}

// A server asking for no wait at all gets its retry at once.
func TestRetryingTakesARetryAfterOfNothingAtItsWord(t *testing.T) {
	server := answering(answer{status: 429, header: map[string]string{"Retry-After": "0"}}, answer{status: 200})
	policy, waits := instantly()

	if _, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, waits.Waits(), []time.Duration{0})
}

// Retry-After is for a 429 or a 503; on any other 5xx it is the backoff.
func TestRetryingBacksOffAsUsualWhenAnotherStatusCarriesRetryAfter(t *testing.T) {
	server := answering(answer{status: 500, header: map[string]string{"Retry-After": "3"}}, answer{status: 200})
	policy, waits := instantly()

	if _, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, waits.Waits(), []time.Duration{200 * time.Millisecond})
}

// An hour's Retry-After is not waited out, and not waited a capped while
// and asked anyway: the answer goes back to the caller now, Retry-After and
// all.
func TestRetryingReturnsAResponseWhoseRetryAfterIsBeyondTheCap(t *testing.T) {
	server := answering(answer{status: 429, header: map[string]string{"Retry-After": "3600"}}, answer{status: 200})
	policy, waits := instantly()

	response, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, response.StatusCode, 429)
	assertEqual(t, response.Header.Get("Retry-After"), "3600")
	assertEqual(t, read(t, response), "Too Many Requests")
	assertEqual(t, server.attempts(), 1)
	assertEqual(t, len(waits.Waits()), 0)
}

func TestRetryingCutsItsBackoffToTheCap(t *testing.T) {
	server := answering(answer{status: 500})
	waits := clock.NewInstant(start)
	policy := RetryPolicy{Retries: 4, BaseDelay: time.Second, MaxDelay: 3 * time.Second, Clock: waits}

	if _, err := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, waits.Waits(), []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second})
}

// No MaxDelay: a server may not make it wait longer than it would have
// waited of its own accord, here 400ms.
func TestRetryingWithoutACapWaitsNoLongerThanItsOwnLongestBackoff(t *testing.T) {
	waits := clock.NewInstant(start)
	policy := RetryPolicy{Retries: 2, BaseDelay: 200 * time.Millisecond, Clock: waits}

	within := answering(answer{status: 503, header: map[string]string{"Retry-After": "0"}}, answer{status: 200})
	if response, _ := post(t, Retrying(policy)(HandlerTransport(within)), context.Background(), ""); response.StatusCode != 200 {
		t.Errorf("got %d", response.StatusCode)
	}
	beyond := answering(answer{status: 503, header: map[string]string{"Retry-After": "1"}}, answer{status: 200})
	if response, _ := post(t, Retrying(policy)(HandlerTransport(beyond)), context.Background(), ""); response.StatusCode != 503 {
		t.Errorf("got %d", response.StatusCode)
	}
	assertEqual(t, waits.Waits(), []time.Duration{0})
}

// The waits are the clock's: nothing is retried until it moves on.
func TestRetryingWaitsOnTheClock(t *testing.T) {
	server := answering(answer{status: 503}, answer{status: 200})
	stopped := clock.NewStopped(start)
	policy := DefaultRetryPolicy
	policy.Clock = stopped
	done := make(chan *http.Response, 1)
	go func() {
		response, _ := post(t, Retrying(policy)(HandlerTransport(server)), context.Background(), "")
		done <- response
	}()

	if err := stopped.BlockUntil(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, server.attempts(), 1)
	stopped.Advance(200 * time.Millisecond)
	if response := <-done; response == nil || response.StatusCode != 200 {
		t.Errorf("got %v", response)
	}
	assertEqual(t, server.attempts(), 2)
}

func TestRetryingStopsWaitingWhenTheRequestIsCancelled(t *testing.T) {
	server := answering(answer{status: 503})
	stopped := clock.NewStopped(start)
	policy := DefaultRetryPolicy
	policy.Clock = stopped
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := post(t, Retrying(policy)(HandlerTransport(server)), ctx, "")
		done <- err
	}()

	if err := stopped.BlockUntil(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	assertEqual(t, server.attempts(), 1)
}

// A caller that hung up is not a failure that may pass.
func TestRetryingNeverRetriesTheRequestsOwnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	cancelling := RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		cancel()
		return nil, request.Context().Err()
	})
	policy, waits := instantly()

	if _, err := post(t, Retrying(policy)(cancelling), ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	assertEqual(t, attempts, 1)
	assertEqual(t, len(waits.Waits()), 0)
}

func TestRetryAfter(t *testing.T) {
	now := start
	for _, c := range []struct {
		header string
		want   time.Duration
	}{
		{"5", 5 * time.Second},
		{" 5 ", 5 * time.Second},
		{"0", 0},
		{"", 2 * time.Second},
		{"not-a-date", 2 * time.Second},
		// Strict: a fraction, or a sign, is not the grammar.
		{"1.5", 2 * time.Second},
		{"+5", 2 * time.Second},
		{"-1", 2 * time.Second},
		{"99999999999999999999", time.Duration(1<<63 - 1)},
		{"Thu, 01 Jan 1970 00:00:00 GMT", 0},
		{"Fri, 23 Oct 2026 12:00:30 GMT", 30 * time.Second},
	} {
		if got := RetryAfter(c.header, now, 2*time.Second); got != c.want {
			t.Errorf("RetryAfter(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestLoggingLogsMethodURLStatusAndDuration(t *testing.T) {
	var log logtest.Logger
	stopped := clock.NewStopped(start)
	transport := Logging(&log, stopped)(HandlerTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stopped.Advance(42 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})))

	if _, err := post(t, transport, context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	assertEqual(t, log.Entries(), []logtest.Entry{{Text: "POST https://upstream.example/token → 201 (42ms)"}})
}

func TestLoggingLogsATransportErrorAsAnErrorAndPassesItOn(t *testing.T) {
	var log logtest.Logger
	transport := Logging(&log, nil)(RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	}))

	if _, err := get(t, transport, context.Background(), "https://upstream.example/"); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("got %v", err)
	}
	assertEqual(t, log.Entries(), []logtest.Entry{{Error: true, Text: "GET https://upstream.example/ → threw connection reset"}})
}

func TestLoggingSaysNothingOfARequestItsCallerCancelled(t *testing.T) {
	var log logtest.Logger
	transport := Logging(&log, nil)(StreamingHandlerTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := get(t, transport, ctx, "https://upstream.example/"); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
	assertEqual(t, log.Entries(), nil)
}

// Logging outermost, as Decorate puts it first: one line for the call, its
// retries and their waits inside it.
func TestLoggingAroundRetryingLogsOnceForAllTheAttempts(t *testing.T) {
	var log logtest.Logger
	server := answering(answer{status: 503}, answer{status: 200})
	policy, waits := instantly()
	transport := Decorate(HandlerTransport(server), Logging(&log, waits), Retrying(policy), Identifying("gokit/test"))

	response, err := get(t, transport, context.Background(), "https://upstream.example/profiles")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, response.StatusCode, 200)
	assertEqual(t, server.attempts(), 2)
	assertEqual(t, log.Lines(), []string{"GET https://upstream.example/profiles → 200 (200ms)"})
}
