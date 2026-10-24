// Package await is for a test that must wait on a goroutine it does not
// own: Until polls a condition until it holds.
//
// A daemon's test starts it and then needs to know that it has got round to
// something - opened its event stream, reached its first pass, said a line -
// which nothing hands the test directly. Until is for that, and never for
// letting time pass: a backoff or an interval is a clock.Stopped's to move
// on, so no test sits through one.
//
// It is a package of its own rather than part of logtest because most of
// what a test waits for is not a line - a stream counted open, a fake's
// calls, a file written - and a log's Contains is only one condition among
// them.
package await
