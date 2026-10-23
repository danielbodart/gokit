// Package clock is the single source of time for everything that waits.
//
// A daemon waits for its next pass, before retrying, before reopening a
// stream that dropped. Each of those goes through a Clock, so a test holds
// time still with a Stopped clock and moves it on with Advance, rather than
// sleeping for real and hoping the timing works out.
//
// It is here rather than borrowed because the standard library has no clock
// to fake, and what a daemon needs of one - Now, After, and a test clock that
// says when a goroutine has reached its wait - is smaller than any of the
// libraries that offer it.
package clock
