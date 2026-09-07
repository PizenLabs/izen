package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The transport must carry explicit boundaries on every socket phase so a
// dead endpoint fails fast instead of hanging the turn on blocked I/O.
func TestStrictTransportBoundaries(t *testing.T) {
	tr := StrictTransport(0) // default → cloud bound
	if tr.ResponseHeaderTimeout != CloudResponseHeaderTimeout {
		t.Errorf("default ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, CloudResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 5s", tr.TLSHandshakeTimeout)
	}
	if tr.ExpectContinueTimeout != time.Second {
		t.Errorf("ExpectContinueTimeout = %v, want 1s", tr.ExpectContinueTimeout)
	}
	if tr.DialContext == nil {
		t.Error("DialContext is nil: dead endpoints would hang without a dial timeout")
	}

	local := StrictTransport(LocalResponseHeaderTimeout)
	if local.ResponseHeaderTimeout != 15*time.Second {
		t.Errorf("local ResponseHeaderTimeout = %v, want 15s", local.ResponseHeaderTimeout)
	}
}

// Phase classification must name the stalled socket phase for the error log.
func TestPhaseDetail(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"response headers", errors.New(`Get "https://x": net/http: timeout awaiting response headers`), "No response headers received from endpoint"},
		{"tls handshake", errors.New(`Get "https://x": net/http: TLS handshake timeout`), "TLS handshake with endpoint stalled"},
		{"dns", errors.New(`Get "https://x": dial tcp: lookup x: no such host`), "DNS resolution failed for endpoint"},
		{"refused", errors.New(`Get "https://x": dial tcp 1.2.3.4:443: connect: connection refused`), "TCP connection refused by endpoint"},
		{"dial timeout", errors.New(`Get "https://x": dial tcp 1.2.3.4:443: dial tcp: i/o timeout`), "TCP connection to endpoint timed out"},
		{"client timeout", errors.New(`Post "https://x": net/http: Client.Timeout exceeded while awaiting headers`), "No response headers received from endpoint"},
		{"unknown", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		if got := PhaseDetail(tc.err); got != tc.want {
			t.Errorf("%s: PhaseDetail = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A hanging socket (accepts the connection, never writes headers) must fail
// cleanly at the header bound with a phase-identifiable error — never hang.
func TestHangingSocketFailsAtHeaderBound(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second) // never writes headers within the bound
	}))
	defer hung.Close()

	client := &http.Client{Transport: StrictTransport(150 * time.Millisecond)}
	start := time.Now()
	_, err := client.Get(hung.URL)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected header-timeout error from hanging socket, got nil")
	}
	if got := PhaseDetail(err); got != "No response headers received from endpoint" {
		t.Errorf("PhaseDetail = %q, want header-stall diagnosis (err: %v)", got, err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("hanging socket blocked for %v, want fast fail near the 150ms bound", elapsed)
	}
}

// A slow-but-live provider (first byte inside the bound) must NOT be cut off.
func TestSlowProviderInsideBoundSucceeds(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer slow.Close()

	client := &http.Client{Transport: StrictTransport(5 * time.Second)}
	resp, err := client.Get(slow.URL)
	if err != nil {
		t.Fatalf("slow provider inside bound failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
