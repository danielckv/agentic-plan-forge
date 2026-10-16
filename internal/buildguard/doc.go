// Package buildguard makes the client and server build tags mutually exclusive.
//
// Plan Forge ships a client and a relay from one repository, but never from one
// binary. Building with both tags must fail loudly rather than silently picking
// a side, so cmd/client and cmd/relay each compile a guard file when both tags
// are set. The guard file references an undefined identifier, so the build error
// names the condition instead of reporting that no Go files matched.
package buildguard
