// Package logtest is a logx.Logger for tests: it keeps every line it is
// given, and whether each was a Log or an Error, for a test to assert on
// what was said - and on what went wrong, specifically.
//
// Waiting for a line that a goroutine the test does not own has yet to say
// is await.Until's job, with Contains as its condition: most of what a test
// waits for is not a line at all.
package logtest
