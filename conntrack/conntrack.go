package conntrack

import (
	"context"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// Entry is one flow in the conntrack table.
type Entry struct {
	Family   Family
	Protocol Protocol
	// Original is the flow as its first packet had it: from the client, to
	// the address and port the client aimed at.
	Original Tuple
	// Reply is what an answer to it looks like, as it arrives: Original
	// reversed, unless NAT rewrote it - a published container port's reply
	// comes from the container's own address and port.
	Reply  Tuple
	Status Status
	// TCPState is a TCP flow's state, and NONE, the zero value, for every
	// other protocol. The states of SCTP and DCCP are not read.
	TCPState TCPState
	// Timeout is how long the flow has left before conntrack forgets it,
	// unless more of its traffic comes, to the second.
	Timeout time.Duration
	// Mark is the flow's connmark, 0 if never set.
	Mark uint32
	// Zone is the flow's conntrack zone, 0 for the default.
	Zone uint16
	// ID is the kernel's id for the flow, which stays the same for as long as
	// the flow is tracked.
	ID uint32
	// The packets and bytes in each direction, which the kernel counts only
	// when accounting is on (net.netfilter.nf_conntrack_acct): zero otherwise.
	OriginalCounters, ReplyCounters Counters
}

// Tuple is one direction of a flow: who it is from and to.
type Tuple struct {
	Source, Destination netip.Addr
	// The ports, for the protocols that have them - TCP, UDP, UDP-Lite, SCTP,
	// DCCP - and 0 for the rest.
	SourcePort, DestinationPort int
	// The type, code and identifier, for ICMP and ICMPv6, and 0 for the rest.
	ICMPType, ICMPCode uint8
	ICMPID             uint16
}

// Counters is a direction's traffic.
type Counters struct {
	Packets, Bytes uint64
}

// Family is a flow's address family.
type Family uint8

const (
	IPv4 Family = syscall.AF_INET
	IPv6 Family = syscall.AF_INET6
)

// String is the family as conntrack -L writes it: ipv4 or ipv6.
func (f Family) String() string {
	switch f {
	case IPv4:
		return "ipv4"
	case IPv6:
		return "ipv6"
	}
	return "family(" + strconv.Itoa(int(f)) + ")"
}

// Protocol is a flow's transport, by its IP protocol number.
type Protocol uint8

const (
	ICMP    Protocol = syscall.IPPROTO_ICMP
	TCP     Protocol = syscall.IPPROTO_TCP
	UDP     Protocol = syscall.IPPROTO_UDP
	DCCP    Protocol = syscall.IPPROTO_DCCP
	GRE     Protocol = syscall.IPPROTO_GRE
	ICMPv6  Protocol = syscall.IPPROTO_ICMPV6
	SCTP    Protocol = syscall.IPPROTO_SCTP
	UDPLite Protocol = syscall.IPPROTO_UDPLITE
)

var protocolNames = map[Protocol]string{
	ICMP: "icmp", TCP: "tcp", UDP: "udp", DCCP: "dccp", GRE: "gre", ICMPv6: "icmpv6", SCTP: "sctp", UDPLite: "udplite",
}

// String is the protocol lower case, as conntrack -L and most configuration
// write it - tcp, udp, icmp - or its number for one without a name here.
func (p Protocol) String() string {
	if name, ok := protocolNames[p]; ok {
		return name
	}
	return strconv.Itoa(int(p))
}

// Status is a flow's IPS_ flags, from linux/netfilter/nf_conntrack_common.h.
type Status uint32

const (
	// Expected is a flow a helper expected, such as FTP's data connection.
	Expected Status = 1 << iota
	// SeenReply is traffic having been seen in the reply direction.
	SeenReply
	// Assured is conntrack having seen enough both ways to keep the flow
	// when the table is full - a real exchange rather than a lone probe, or a
	// SYN that went nowhere.
	Assured
	// Confirmed is the flow's first packet having left the box.
	Confirmed
	SrcNAT
	DstNAT
	SeqAdjust
	SrcNATDone
	DstNATDone
	Dying
	FixedTimeout
	Template
	NATClash
	Helper
	Offload
	HWOffload
)

// Has is whether every one of these flags is set.
func (s Status) Has(flags Status) bool { return s&flags == flags }

// TCPState is a TCP flow's state, the kernel's enum tcp_conntrack.
type TCPState uint8

const (
	TCPNone TCPState = iota
	TCPSynSent
	TCPSynRecv
	TCPEstablished
	TCPFinWait
	TCPCloseWait
	TCPLastAck
	TCPTimeWait
	TCPClose
	TCPSynSent2
)

// The names conntrack-tools prints, so a state reads as `conntrack -L`
// writes it.
var tcpStates = [...]string{
	"NONE", "SYN_SENT", "SYN_RECV", "ESTABLISHED", "FIN_WAIT",
	"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE", "SYN_SENT2",
}

// String is the state as conntrack-tools names it, such as ESTABLISHED or
// TIME_WAIT, or TCPState(n) for one this package does not know.
func (s TCPState) String() string {
	if int(s) < len(tcpStates) {
		return tcpStates[s]
	}
	return "TCPState(" + strconv.Itoa(int(s)) + ")"
}

// Dumper reads the conntrack table.
//
// An interface, so an application's tests hand it entries of their own with a
// DumperFunc, and need no netlink and no CAP_NET_ADMIN.
//
// An error is "could not tell", never an empty table: a table with no flows
// is an empty, non-nil slice.
type Dumper interface {
	Dump(ctx context.Context) ([]Entry, error)
}

// DumperFunc is a function as a Dumper.
type DumperFunc func(ctx context.Context) ([]Entry, error)

func (f DumperFunc) Dump(ctx context.Context) ([]Entry, error) { return f(ctx) }
