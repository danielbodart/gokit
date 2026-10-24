// Package httpx is the seam everything that talks HTTP goes through.
//
// Every client is an *http.Client over an http.RoundTripper - a Docker Engine
// API over its unix socket, a router's UPnP description and SOAP - and every
// fake is an http.Handler. The adapters here join the two, so a test hands a
// client an in-memory server with no listener, no port and no socket, and the
// client cannot tell the difference. That is what makes a daemon testable end
// to end, and it is why nothing above this line knows whether it is talking
// over a unix socket or TCP.
//
// What a client does around every request is a Decorator over its
// RoundTripper, applied in order by Decorate: Identifying names the client,
// Retrying tries again what may pass - honouring a server's Retry-After, up
// to a cap - and Logging says what each request came to.
//
// It is here rather than borrowed because the standard library stops just
// short: http.RoundTripper has no func adapter, httptest.ResponseRecorder
// links the testing packages into a production binary and records a stream
// that never ends forever, and the unix socket transport is a few lines no
// dependency is worth.
package httpx
