//go:build client && server

package buildguard

// The identifier below is intentionally undefined. Compiling this file is the
// error: a build that sets both the client and the server tag fails with this
// name in the message, which is far clearer than "build constraints exclude all
// Go files". Do not define it.
var _ = BOTH_BUILD_TAGS_client_and_server_are_mutually_exclusive
