package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerificationAdapter(t *testing.T) {
	t.Run("successful verification", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		v := NewVerificationAdapter()
		ok, err := v.Verify(context.Background(), "test", srv.URL, []byte("valid-key"))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("expected verification success")
		}
	})

	t.Run("empty key is insufficient", func(t *testing.T) {
		v := NewVerificationAdapter()
		ok, err := v.Verify(context.Background(), "test", "", []byte(""))
		if err == nil {
			t.Error("expected error for empty key")
		}
		if ok {
			t.Error("expected verification failure")
		}
	})

	t.Run("unauthorized key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		v := NewVerificationAdapter()
		ok, err := v.Verify(context.Background(), "test", srv.URL, []byte("bad-key"))
		if err == nil {
			t.Error("expected error for unauthorized key")
		}
		if ok {
			t.Error("expected verification failure")
		}
	})
}
