#!/bin/bash
# Suite 19: HTTP/2 support on the unified listener.
#
# After the Phase-0 Fiber → net/http cutover, hula serves HTTP/2 over
# ALPN. Verify with curl's --http2 flag.

# --http2 requires libcurl to be built with HTTP/2 support. Alpine's
# libcurl-openssl package includes it; the test-runner already has curl
# installed.

# Use -v to catch the "ALPN: server accepted http/2" line. We pipe
# stderr to the output string because curl writes the protocol
# negotiation there.
probe=$(runner_shell "curl --http2 -sv -k https://${HULA_HOST}/hulastatus 2>&1" || true)

assert_contains "$probe" "ALPN" "TLS ALPN negotiation present in verbose output"
assert_contains "$probe" "HTTP/2" "connection negotiated as HTTP/2"

# And the response itself should still come through cleanly.
assert_contains "$probe" "200" "/hulastatus returns 200"

# HTTP/1.1 fallback should also work (for older clients without ALPN).
probe11=$(runner_shell "curl --http1.1 -s -k https://${HULA_HOST}/hulastatus" || true)
assert_contains "$probe11" "ok" "HTTP/1.1 fallback still works against /hulastatus"
