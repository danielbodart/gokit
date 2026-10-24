package conntrack

import (
	"context"
	"testing"
)

func TestTCPStatesAreNamedAsConntrackToolsNamesThem(t *testing.T) {
	assertEqual(t, TCPNone.String(), "NONE")
	assertEqual(t, TCPEstablished.String(), "ESTABLISHED")
	assertEqual(t, TCPTimeWait.String(), "TIME_WAIT")
	assertEqual(t, TCPCloseWait.String(), "CLOSE_WAIT")
	assertEqual(t, TCPSynSent2.String(), "SYN_SENT2")
	assertEqual(t, TCPState(10).String(), "TCPState(10)")
}

func TestProtocolsAndFamiliesAreWrittenAsConntrackWritesThem(t *testing.T) {
	assertEqual(t, TCP.String(), "tcp")
	assertEqual(t, UDP.String(), "udp")
	assertEqual(t, ICMPv6.String(), "icmpv6")
	assertEqual(t, Protocol(115).String(), "115")
	assertEqual(t, IPv4.String(), "ipv4")
	assertEqual(t, IPv6.String(), "ipv6")
	assertEqual(t, Family(0).String(), "family(0)")
}

func TestStatusHasEveryFlagAskedFor(t *testing.T) {
	status := SeenReply | Assured | Confirmed
	assertEqual(t, status.Has(Assured), true)
	assertEqual(t, status.Has(Assured|SeenReply), true)
	assertEqual(t, status.Has(Assured|DstNAT), false)
	assertEqual(t, Status(0).Has(Assured), false)
	// The kernel's own bit numbers.
	assertEqual(t, SeenReply, Status(1<<1))
	assertEqual(t, Assured, Status(1<<2))
	assertEqual(t, Offload, Status(1<<14))
}

func TestDumperFuncIsADumper(t *testing.T) {
	want := []Entry{{Family: IPv4, Protocol: UDP}}
	var dumper Dumper = DumperFunc(func(context.Context) ([]Entry, error) { return want, nil })
	got, err := dumper.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, want)
}
