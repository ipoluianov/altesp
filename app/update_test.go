package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.10", "v1.2.9", true}, {"v1.2.9", "v1.2.10", false}, {"v1.0.0", "v1.0.0", false},
		{"v0.2.0", "dev", true}, {"v0.2.0", "v0.1.0-3-gabc", true}, {"dev", "v0.1.0", false},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

func TestLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"https://github.com/ipoluianov/altesp/releases/tag/v9.9.9"}`))
	}))
	defer srv.Close()
	old := releasesURL
	releasesURL = srv.URL
	defer func() { releasesURL = old }()
	r, err := LatestRelease(context.Background())
	if err != nil || r.Tag != "v9.9.9" || !r.Newer {
		t.Fatalf("%+v, %v", r, err)
	}
}
