package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryAfterOn503(t *testing.T) {
	const secret = "hm_dev_fixture_secret"
	cases := []struct {
		name   string
		header string
		set    bool
		want   time.Duration
		slack  time.Duration
	}{
		{name: "delta", header: "35", set: true, want: 35 * time.Second},
		{name: "zero", header: "0", set: true, want: 0},
		{name: "missing", set: false},
		{name: "junk", header: "soon", set: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.set {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(srv.Close)
			c := NewClient(srv.URL, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
			c.DeviceSecret = secret
			_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err %v", err)
			}
			if se.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status %d", se.StatusCode)
			}
			if strings.Contains(se.Error(), "Retry-After") || strings.Contains(se.Error(), "soon") {
				t.Fatalf("status error must not echo the header: %s", se.Error())
			}
			if tc.name == "missing" || tc.name == "junk" {
				if se.HasRetryAfter {
					t.Fatalf("header %q parsed as %s", tc.header, se.RetryAfter)
				}
				return
			}
			if !se.HasRetryAfter {
				t.Fatal("Retry-After not recorded")
			}
			if se.RetryAfter != tc.want {
				t.Fatalf("delay %s want %s", se.RetryAfter, tc.want)
			}
		})
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	when := time.Now().UTC().Add(35 * time.Second).Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", when.Format(http.TimeFormat))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	c.DeviceSecret = "hm_dev_fixture_secret"
	_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	var se *StatusError
	if !errors.As(err, &se) || !se.HasRetryAfter {
		t.Fatalf("err %v", err)
	}
	if se.RetryAfter < 30*time.Second || se.RetryAfter > 40*time.Second {
		t.Fatalf("date delay %s", se.RetryAfter)
	}
}

func TestRetryAfterIgnoredUnless503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "35")
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	c.DeviceSecret = "hm_dev_fixture_secret"
	_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	if se.StatusCode != http.StatusInternalServerError || se.HasRetryAfter || se.RetryAfter != 0 {
		t.Fatalf("500 must not carry Retry-After: %+v", se)
	}
	if strings.Contains(se.Error(), "nope") {
		t.Fatalf("body leaked: %s", se.Error())
	}
}
