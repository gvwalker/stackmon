// Package readretry supplies the shared retry policy for remote HTTP reads.
// It delegates to the caller's transport and keeps the caller's context intact;
// operation budgets must encompass the entire exchange, including body reads.
package readretry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	attempts = 3
	backoff  = 200 * time.Millisecond
	maxWait  = 5 * time.Second
)

// Transport retries bodyless GET/HEAD requests on 429, 500, 502, 503, 504,
// and transient errors before response headers arrive. Successful response
// bodies remain streamed by the caller: decoding, verification, and local
// writes are never replayed. A nil base uses http.DefaultTransport.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base}
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	safe := (req.Method == http.MethodGet || req.Method == http.MethodHead) && (req.Body == nil || req.Body == http.NoBody)
	for attempt := 0; ; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		resp, err := t.base.RoundTrip(req.Clone(req.Context()))
		if ctxErr := req.Context().Err(); ctxErr != nil {
			closeBody(resp)
			return nil, ctxErr
		}
		if !safe || attempt == attempts-1 || !retryable(resp, err) {
			return resp, err
		}

		delay := backoff << attempt
		if resp != nil {
			delay = max(delay, retryAfter(resp.Header.Get("Retry-After")))
		}
		// Never shorten the server's requested wait. If it is beyond our
		// backoff ceiling, surface the final failure without another request.
		if delay > maxWait {
			return resp, err
		}
		// Do not drain an error body: it too can stall. Closing discards the
		// connection while keeping the delay inside the operation's budget.
		closeBody(resp)
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func retryable(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		var network net.Error
		return (errors.As(err, &network) && (network.Timeout() || network.Temporary())) ||
			errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
	}
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

func retryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	// Check digits before parsing to distinguish invalid syntax (use normal
	// backoff) from an overflowing but valid delta (do not retry early).
	digits := strings.Trim(raw, "0123456789") == ""
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > uint64(maxWait/time.Second) {
			return maxWait + 1
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(raw); err == nil {
		return max(0, time.Until(date))
	}
	return 0
}
