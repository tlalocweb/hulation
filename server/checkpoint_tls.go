package server

import (
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tlalocweb/hulation/config"
)

const checkpointBindingHeader = "X-Mitosu-Checkpoint-Binding"
const checkpointExporterLabel = "EXPORTER-Mitosu-Checkpoint-v2"

type checkpointBindingTransport struct {
	next    http.RoundTripper
	enabled bool
}

func (t *checkpointBindingTransport) RoundTrip(original *http.Request) (*http.Response, error) {
	request := original.Clone(original.Context())
	request.Header = original.Header.Clone()
	request.Header.Del(checkpointBindingHeader)
	if t.enabled {
		if request.TLS == nil || !request.TLS.HandshakeComplete || request.URL.Scheme != "https" ||
			request.Method != http.MethodGet || request.URL.Path != "/checkpoints" ||
			!strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
			return nil, errors.New("checkpoint proxy requires a TLS WebSocket upgrade")
		}
		exporter, err := request.TLS.ExportKeyingMaterial(checkpointExporterLabel, nil, 32)
		if err != nil {
			return nil, errors.New("checkpoint frontend TLS binding unavailable")
		}
		request.Header.Set(checkpointBindingHeader, "v1."+hex.EncodeToString(exporter))
	}
	return t.next.RoundTrip(request)
}

func newCheckpointProxy(target *url.URL, binding *config.ProxyUpstreamTLS) (http.Handler, error) {
	return checkpointProxyWithTransport(target, binding, http.DefaultTransport)
}

func checkpointProxyWithTransport(target *url.URL, binding *config.ProxyUpstreamTLS, base http.RoundTripper) (http.Handler, error) {
	if target.Scheme != "https" || binding.ClientCert == "" || binding.ClientKey == "" {
		return nil, errors.New("checkpoint proxy requires verified HTTPS and a client certificate/key pair")
	}
	certificate, err := tls.LoadX509KeyPair(binding.ClientCert, binding.ClientKey)
	if err != nil {
		return nil, errors.New("checkpoint proxy client certificate/key cannot be loaded")
	}
	plain, ok := base.(*http.Transport)
	if !ok {
		return nil, errors.New("checkpoint proxy requires a standard TLS transport")
	}
	transport := plain.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("checkpoint upstream certificate verification cannot be disabled")
	}
	transport.TLSClientConfig.Certificates = []tls.Certificate{certificate}
	return newPlainProxyWithTransport(target, &checkpointBindingTransport{next: transport, enabled: binding.CheckpointBinding}), nil
}
