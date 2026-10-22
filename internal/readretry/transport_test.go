package readretry

// Failure inventory, recorded before implementation: retries can amplify
// library retries; retry permanent HTTP/DNS/TLS errors or unsafe writes;
// ignore cancellation before/during a delay; restart a caller's deadline;
// overflow or shorten Retry-After; leak discarded bodies; consume a final
// error body; or alter credentials. CLI scenarios cover wiring and artifacts.
// This injected-RoundTripper seam covers cancellation and timing deterministically
// with synctest, which a CLI process signal cannot prove about a caller context.
import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestDecisionsAndAttemptLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		err      error
		attempts int
	}{
		{"429", 429, nil, 3}, {"500", 500, nil, 3}, {"502", 502, nil, 3}, {"503", 503, nil, 3}, {"504", 504, nil, 3},
		{"401", 401, nil, 1}, {"403", 403, nil, 1}, {"404", 404, nil, 1}, {"501", 501, nil, 1}, {"505", 505, nil, 1},
		{"reset", 0, syscall.ECONNRESET, 3}, {"pipe", 0, syscall.EPIPE, 3}, {"EOF", 0, io.EOF, 3},
		{"timeout", 0, &net.DNSError{IsTimeout: true}, 3}, {"temporary DNS", 0, &net.DNSError{IsTemporary: true}, 3},
		{"permanent DNS", 0, &net.DNSError{IsNotFound: true}, 1}, {"certificate", 0, x509.UnknownAuthorityError{}, 1},
		{"cancelled error", 0, context.Canceled, 1}, {"deadline error", 0, context.DeadlineExceeded, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var bodies []*trackedBody
				rt := Transport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if tc.err != nil {
						return nil, tc.err
					}
					b := &trackedBody{Reader: strings.NewReader("final error body")}
					bodies = append(bodies, b)
					return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: b}, nil
				}))
				req, _ := http.NewRequest(http.MethodGet, "https://example.test/read", nil)
				start := time.Now()
				resp, err := rt.RoundTrip(req)
				if calls != tc.attempts {
					t.Fatalf("attempts=%d, want %d", calls, tc.attempts)
				}
				wantDelay := time.Duration(0)
				if calls == 3 {
					wantDelay = 600 * time.Millisecond
				}
				if elapsed := time.Since(start); elapsed != wantDelay {
					t.Errorf("delay=%s, want %s", elapsed, wantDelay)
				}
				if tc.err != nil {
					if !errors.Is(err, tc.err) {
						t.Fatalf("error=%v, want %v", err, tc.err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, b := range bodies[:len(bodies)-1] {
					if !b.closed {
						t.Error("discarded body not closed")
					}
				}
				if bodies[len(bodies)-1].closed {
					t.Error("final body already closed")
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if string(body) != "final error body" {
					t.Errorf("final body=%q", body)
				}
			})
		})
	}
}

func TestRetryAfter(t *testing.T) {
	for _, name := range []string{"seconds", "date", "past", "invalid", "negative", "too long", "overflow"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				after := map[string]string{"seconds": "1", "date": time.Now().Add(2 * time.Second).Format(http.TimeFormat), "past": time.Now().Add(-time.Hour).Format(http.TimeFormat), "invalid": "oops", "negative": "-1", "too long": "6", "overflow": "999999999999999999999999"}[name]
				calls := 0
				rt := Transport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					status := 200
					if calls == 1 {
						status = 429
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{after}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}))
				req, _ := http.NewRequest(http.MethodGet, "https://example.test/read", nil)
				start := time.Now()
				resp, err := rt.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				want := 200 * time.Millisecond
				wantCalls := 2
				switch name {
				case "seconds":
					want = time.Second
				case "date":
					want = 2 * time.Second
				case "too long", "overflow":
					want = 0
					wantCalls = 1
				}
				if calls != wantCalls || time.Since(start) != want {
					t.Errorf("attempts=%d delay=%s, want %d %s", calls, time.Since(start), wantCalls, want)
				}
			})
		})
	}
}

func TestContextAndSafeReads(t *testing.T) {
	for _, mode := range []string{"already cancelled", "during wait", "deadline", "POST", "GET body", "HEAD"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "already cancelled" {
					cancel()
				}
				if mode == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
					defer stop()
				}
				method := http.MethodGet
				if mode == "POST" {
					method = http.MethodPost
				}
				if mode == "HEAD" {
					method = http.MethodHead
				}
				var body io.Reader
				if mode == "GET body" {
					body = strings.NewReader("unsafe body")
				}
				req, _ := http.NewRequestWithContext(ctx, method, "https://example.test/read", body)
				req.Header.Set("Authorization", "Bearer fixture-token")
				calls := 0
				rt := Transport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "Bearer fixture-token" {
						t.Error("credentials lost")
					}
					if r.Context() != ctx {
						t.Error("caller context replaced")
					}
					if mode == "during wait" {
						go func() { time.Sleep(50 * time.Millisecond); cancel() }()
					}
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
				}))
				start := time.Now()
				resp, err := rt.RoundTrip(req)
				if resp != nil {
					_ = resp.Body.Close()
				}
				wantCalls := 1
				switch mode {
				case "already cancelled":
					wantCalls = 0
					if !errors.Is(err, context.Canceled) {
						t.Errorf("error=%v", err)
					}
				case "during wait":
					if !errors.Is(err, context.Canceled) || time.Since(start) != 50*time.Millisecond {
						t.Errorf("error=%v delay=%s", err, time.Since(start))
					}
				case "deadline":
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
						t.Errorf("error=%v delay=%s", err, time.Since(start))
					}
				case "HEAD":
					wantCalls = 3
				}
				if calls != wantCalls {
					t.Errorf("attempts=%d, want %d", calls, wantCalls)
				}
			})
		})
	}
}
