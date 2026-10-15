// Package client talks to winunitd's control pipes from Go.
//
// Dial connects to the system manager on \\.\pipe\winunitd\control. DialUser
// connects to a user manager on \\.\pipe\winunitd\user\<SID>\control, and
// DialCurrentUser to the caller's own. Each dial verifies the identity of the
// process serving the pipe before any request is sent: LocalSystem or an
// administrator for the system manager, the named user for a user manager. A
// squatted pipe is refused.
//
// Every manager authorizes LocalSystem and administrators. A user manager
// also authorizes its own user. A SYSTEM service can therefore control a
// user's manager with DialUser, for example after EnableLinger has started it.
//
//	c, err := client.Dial(ctx)
//	if err != nil {
//		return err
//	}
//	defer c.Close()
//	if _, err := c.Capabilities(ctx); err == nil && c.Supports(client.MethodUnitOfProcess) {
//		r, err := c.UnitOfProcess(ctx, pid)
//		...
//	}
//
// A Client is safe for concurrent use; its requests take turns on the one
// connection, and a request waiting for its turn honours its context. A
// context deadline or cancellation interrupts the request in flight; after
// that, or after any other transport failure, the Client is closed and the
// caller dials again. Close interrupts a request in flight. A request whose
// reply was lost may still have been carried out; the client never retries.
// Manager refusals are *Error values with a Code such as CodeNotFound and
// leave the Client open.
//
// The request and result types are the control protocol's, version 1. They
// are type aliases, so values are interchangeable with what winctl and the
// managers use. The package follows the product version and, until 1.0, may
// change between minor releases. Dialing is only available on Windows; on
// other systems the dial functions return an error.
package client
