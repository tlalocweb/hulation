package config

import "testing"

// validateProxyServer is the per-server proxy_only/proxy_pass gate that
// LoadConfig runs; a failure here surfaces as a config load error.
func TestValidateProxyServer(t *testing.T) {
	cases := []struct {
		name    string
		s       *Server
		wantErr bool
	}{
		{"ordinary vhost (neither field set)", &Server{Host: "a.test"}, false},
		{"nil server", nil, false},
		{"valid proxy_only http", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://127.0.0.1:8080"}, false},
		{"valid proxy_only https with port", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "https://up.internal:8443"}, false},
		{"proxy_only without proxy_pass", &Server{Host: "p.test", ProxyOnly: true}, true},
		{"proxy_only with blank proxy_pass", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "   "}, true},
		{"proxy_pass set but proxy_only false", &Server{Host: "p.test", ProxyPass: "http://127.0.0.1:8080"}, true},
		{"proxy_pass not a URL", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "://nope"}, true},
		{"proxy_pass missing scheme", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "127.0.0.1:8080"}, true},
		{"proxy_pass non-http scheme", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "ftp://127.0.0.1:8080"}, true},
		{"proxy_pass missing host", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://"}, true},
		// The upstream path/query is ignored (request path is preserved), and
		// credentials aren't forwarded — reject them rather than mislead.
		{"proxy_pass with path rejected", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://up.internal:8080/foo"}, true},
		{"proxy_pass with query rejected", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://up.internal:8080?x=1"}, true},
		{"proxy_pass with fragment rejected", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://up.internal:8080#frag"}, true},
		{"proxy_pass with credentials rejected", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://user:pw@up.internal:8080"}, true},
		{"proxy_pass trailing slash allowed", &Server{Host: "p.test", ProxyOnly: true, ProxyPass: "http://up.internal:8080/"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProxyServer(tc.s)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateProxyServer() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
