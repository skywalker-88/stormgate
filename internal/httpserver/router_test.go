package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/skywalker-88/stormgate/internal/httpserver"
)

func newProxy(t *testing.T, target string) *httputil.ReverseProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"bad_gateway"}`))
	}
	return rp
}

func Test_LocalRoutes(t *testing.T) {
	t.Setenv("PROXY_PREFIX", "/api") // consistent with compose
	router := httpserver.NewRouter(nil)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	for _, p := range []string{"/health", "/read", "/search", "/metrics"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", p, resp.StatusCode)
		}
	}
}

func Test_ProxyOK_WithPrefix(t *testing.T) {
	t.Setenv("PROXY_PREFIX", "/api")

	// backend serves /hello (not /api/hello)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backend.Close)

	proxy := newProxy(t, backend.URL)
	router := httpserver.NewRouter(proxy)
	gw := httptest.NewServer(router)
	t.Cleanup(gw.Close)

	resp, err := http.Get(gw.URL + "/api/hello") // prefix gets stripped
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func Test_ApiWithoutProxy_Returns502(t *testing.T) {
	t.Setenv("PROXY_PREFIX", "/api")
	router := httpserver.NewRouter(nil)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/anything")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
}

func Test_NonApiUnknown_Is404(t *testing.T) {
	t.Setenv("PROXY_PREFIX", "/api")
	router := httpserver.NewRouter(nil)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/favicon.ico") // not local, not under /api
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}
