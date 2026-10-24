package conntrack

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

var ctx = context.Background()

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

const nlaFNested = 1 << 15

// attribute is one netlink attribute, padded as the kernel pads it.
func attribute(kind uint16, payload []byte) []byte {
	length := syscall.SizeofRtAttr + len(payload)
	out := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(out[0:], uint16(length))
	binary.NativeEndian.PutUint16(out[2:], kind)
	copy(out[syscall.SizeofRtAttr:], payload)
	return out
}

// nest is a nested attribute, flagged NLA_F_NESTED as current kernels flag it.
func nest(kind uint16, children ...[]byte) []byte {
	return attribute(kind|nlaFNested, slices.Concat(children...))
}

func be16(value uint16) []byte { return binary.BigEndian.AppendUint16(nil, value) }
func be32(value uint32) []byte { return binary.BigEndian.AppendUint32(nil, value) }
func be64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

// flow describes one conntrack entry the way a test thinks of it.
type flow struct {
	src, dst     string
	protocol     Protocol
	sport, dport uint16
	status       Status
	tcpState     int // -1 for none, as UDP and ICMP have
	// The reply's source, where NAT rewrote it, and its port; the original's
	// destination otherwise.
	natSrc  string
	natPort uint16
	extra   [][]byte
}

// tuple is a CTA_TUPLE_ORIG or CTA_TUPLE_REPLY.
func tuple(kind uint16, src, dst netip.Addr, protocol Protocol, sport, dport uint16) []byte {
	ip := nest(ctaTupleIP, attribute(ctaIPv4Src, src.AsSlice()), attribute(ctaIPv4Dst, dst.AsSlice()))
	if src.Is6() {
		ip = nest(ctaTupleIP, attribute(ctaIPv6Src, src.AsSlice()), attribute(ctaIPv6Dst, dst.AsSlice()))
	}
	proto := [][]byte{attribute(ctaProtoNum, []byte{byte(protocol)})}
	switch protocol {
	case TCP, UDP:
		proto = append(proto, attribute(ctaProtoSrcPort, be16(sport)), attribute(ctaProtoDstPort, be16(dport)))
	case ICMP:
		// An echo request one way, its reply the other; the id is the same.
		icmpType := byte(8)
		if kind == ctaTupleReply {
			icmpType = 0
		}
		proto = append(proto, attribute(ctaProtoICMPID, be16(77)), attribute(ctaProtoICMPType, []byte{icmpType}),
			attribute(ctaProtoICMPCode, []byte{0}))
	}
	return nest(kind, ip, nest(ctaTupleProto, proto...))
}

// body is a flow as ctnetlink sends one: nfgenmsg, then the original and reply
// tuples (the reply's ports swapped, so its destination is the client's
// ephemeral port), the status, the timeout and, for TCP, its state.
func (f flow) body() []byte {
	src, dst := netip.MustParseAddr(f.src), netip.MustParseAddr(f.dst)
	replySrc, replyPort := dst, f.dport
	if f.natSrc != "" {
		replySrc, replyPort = netip.MustParseAddr(f.natSrc), f.natPort
	}
	family := byte(syscall.AF_INET)
	if src.Is6() {
		family = syscall.AF_INET6
	}
	out := []byte{family, 0, 0, 0}
	out = append(out, tuple(ctaTupleOrig, src, dst, f.protocol, f.sport, f.dport)...)
	out = append(out, tuple(ctaTupleReply, replySrc, src, f.protocol, replyPort, f.sport)...)
	out = append(out, attribute(ctaStatus, be32(uint32(f.status)))...)
	out = append(out, attribute(ctaTimeout, be32(120))...)
	if f.tcpState >= 0 {
		out = append(out, nest(ctaProtoinfo, nest(ctaProtoinfoTCP,
			attribute(ctaProtoinfoTCPState, []byte{uint8(f.tcpState)}),
			attribute(2 /* CTA_PROTOINFO_TCP_WSCALE_ORIGINAL, not read */, []byte{7})))...)
	}
	for _, extra := range f.extra {
		out = append(out, extra...)
	}
	return out
}

// message is one netlink message.
func message(kind uint16, seq uint32, data []byte) []byte {
	out := make([]byte, syscall.NLMSG_HDRLEN, syscall.NLMSG_HDRLEN+len(data)+3)
	binary.NativeEndian.PutUint32(out[0:], uint32(syscall.NLMSG_HDRLEN+len(data)))
	binary.NativeEndian.PutUint16(out[4:], kind)
	binary.NativeEndian.PutUint16(out[6:], syscall.NLM_F_MULTI)
	binary.NativeEndian.PutUint32(out[8:], seq)
	out = append(out, data...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	return out
}

func flowMessage(seq uint32, f flow) []byte { return message(flowGet, seq, f.body()) }

// done ends a dump, with the errno the kernel puts in it (0, or negated).
func done(seq uint32, errno int32) []byte {
	return message(syscall.NLMSG_DONE, seq, binary.NativeEndian.AppendUint32(nil, uint32(errno)))
}

// failure is an NLMSG_ERROR: the negated errno, then the request it answers.
func failure(seq uint32, errno syscall.Errno) []byte {
	return message(syscall.NLMSG_ERROR, seq, slices.Concat(
		binary.NativeEndian.AppendUint32(nil, uint32(-int32(errno))), request(seq, IPv4)))
}

// datagrams is a fake socket: each receive hands over the next datagram.
func datagrams(all ...[]byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(all) == 0 {
			return nil, errors.New("read past the end of the dump")
		}
		next := all[0]
		all = all[1:]
		return next, nil
	}
}

var (
	established = flow{src: "10.0.0.50", dst: "10.0.0.1", protocol: TCP, sport: 51000, dport: 25565,
		status: SeenReply | Assured | Confirmed, tcpState: int(TCPEstablished)}
	synSent = flow{src: "10.0.0.51", dst: "10.0.0.1", protocol: TCP, sport: 52000, dport: 25565,
		status: Confirmed, tcpState: int(TCPSynSent)}
	udpAssured = flow{src: "203.0.113.9", dst: "10.0.0.1", protocol: UDP, sport: 40000, dport: 19132,
		status: SeenReply | Assured | Confirmed, tcpState: -1}
	// A published container port: the reply comes from the container.
	natted = flow{src: "10.0.0.60", dst: "10.0.0.1", protocol: TCP, sport: 44000, dport: 8080,
		natSrc: "172.17.0.2", natPort: 80,
		status: SeenReply | Assured | Confirmed | DstNAT | DstNATDone, tcpState: int(TCPEstablished)}
	timeWait = flow{src: "127.0.0.1", dst: "127.0.0.1", protocol: TCP, sport: 52336, dport: 28080,
		status: SeenReply | Assured | Confirmed, tcpState: int(TCPTimeWait)}
	ping = flow{src: "10.0.0.70", dst: "10.0.0.1", protocol: ICMP,
		status: SeenReply | Confirmed, tcpState: -1}
	gre  = flow{src: "10.0.0.80", dst: "10.0.0.1", protocol: GRE, status: Confirmed, tcpState: -1}
	ipv6 = flow{src: "2001:db8::9", dst: "2001:db8::1", protocol: TCP, sport: 60000, dport: 443,
		status: SeenReply | Assured | Confirmed, tcpState: int(TCPEstablished)}
)

// table is the fixture as one dump, in the two datagrams a real dump of it
// might take, ending in NLMSG_DONE.
func table(seq uint32) func() ([]byte, error) {
	return datagrams(
		slices.Concat(flowMessage(seq, established), flowMessage(seq, synSent), flowMessage(seq, udpAssured),
			flowMessage(seq, natted)),
		slices.Concat(flowMessage(seq, timeWait), flowMessage(seq, ping), flowMessage(seq, gre),
			flowMessage(seq, ipv6), done(seq, 0)),
	)
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestReadsEveryFlowOfEveryProtocol(t *testing.T) {
	entries, err := collect(ctx, table(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	var protocols []string
	for _, entry := range entries {
		protocols = append(protocols, entry.Family.String()+"/"+entry.Protocol.String()+"/"+entry.TCPState.String())
	}
	assertEqual(t, protocols, []string{
		"ipv4/tcp/ESTABLISHED", "ipv4/tcp/SYN_SENT", "ipv4/udp/NONE", "ipv4/tcp/ESTABLISHED",
		"ipv4/tcp/TIME_WAIT", "ipv4/icmp/NONE", "ipv4/gre/NONE", "ipv6/tcp/ESTABLISHED",
	})
}

func TestReadsBothDirectionsOfAFlow(t *testing.T) {
	entries, err := collect(ctx, table(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entries[0], Entry{
		Family: IPv4, Protocol: TCP,
		Original: Tuple{Source: addr("10.0.0.50"), Destination: addr("10.0.0.1"), SourcePort: 51000, DestinationPort: 25565},
		Reply:    Tuple{Source: addr("10.0.0.1"), Destination: addr("10.0.0.50"), SourcePort: 25565, DestinationPort: 51000},
		Status:   SeenReply | Assured | Confirmed, TCPState: TCPEstablished, Timeout: 120 * time.Second,
	})
	// The client aimed at the host's 8080; the container answered from its
	// own 80.
	assertEqual(t, entries[3].Original, Tuple{Source: addr("10.0.0.60"), Destination: addr("10.0.0.1"), SourcePort: 44000, DestinationPort: 8080})
	assertEqual(t, entries[3].Reply, Tuple{Source: addr("172.17.0.2"), Destination: addr("10.0.0.60"), SourcePort: 80, DestinationPort: 44000})
	assertEqual(t, entries[3].Status.Has(DstNAT), true)
	assertEqual(t, entries[3].Status.Has(SrcNAT), false)
	assertEqual(t, entries[7].Original, Tuple{Source: addr("2001:db8::9"), Destination: addr("2001:db8::1"), SourcePort: 60000, DestinationPort: 443})
}

func TestReadsICMPsTypeCodeAndIdInPlaceOfPorts(t *testing.T) {
	entries, err := collect(ctx, table(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entries[5].Original, Tuple{Source: addr("10.0.0.70"), Destination: addr("10.0.0.1"), ICMPType: 8, ICMPID: 77})
	assertEqual(t, entries[5].Reply, Tuple{Source: addr("10.0.0.1"), Destination: addr("10.0.0.70"), ICMPType: 0, ICMPID: 77})
	// GRE's keys are not read: no ports, nothing else.
	assertEqual(t, entries[6].Original, Tuple{Source: addr("10.0.0.80"), Destination: addr("10.0.0.1")})
}

func TestReadsTheMarkZoneIdAndCounters(t *testing.T) {
	counted := established
	counted.extra = [][]byte{
		attribute(ctaMark, be32(0x10)),
		attribute(ctaZone, be16(3)),
		attribute(ctaID, be32(0xdeadbeef)),
		// CTA_COUNTERS_PAD, which the kernel puts before a u64 to align it.
		nest(ctaCountersOrig, attribute(3, nil), attribute(ctaCountersPackets, be64(5)), attribute(ctaCountersBytes, be64(300))),
		nest(ctaCountersReply, attribute(ctaCountersPackets, be64(4)), attribute(ctaCountersBytes, be64(1<<33))),
		attribute(20 /* CTA_TIMESTAMP, not read */, be64(1)),
	}
	entry, err := parseFlow(counted.body())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entry.Mark, uint32(0x10))
	assertEqual(t, entry.Zone, uint16(3))
	assertEqual(t, entry.ID, uint32(0xdeadbeef))
	assertEqual(t, entry.OriginalCounters, Counters{Packets: 5, Bytes: 300})
	assertEqual(t, entry.ReplyCounters, Counters{Packets: 4, Bytes: 1 << 33})
}

func TestAnOlderKernelsAnswerOfNewIsReadTheSame(t *testing.T) {
	entries, err := collect(ctx, datagrams(slices.Concat(message(flowNew, 1, natted.body()), done(1, 0))), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, len(entries), 1)
	assertEqual(t, entries[0].Original.DestinationPort, 8080)
}

func TestATCPStateTheKernelDoesNotDefineIsKeptByNumber(t *testing.T) {
	odd := natted
	odd.tcpState = 200
	entry, err := parseFlow(odd.body())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entry.TCPState, TCPState(200))
	assertEqual(t, entry.TCPState.String(), "TCPState(200)")
}

func TestAnEmptyTableIsNoFlowsNotCouldNotTell(t *testing.T) {
	entries, err := collect(ctx, datagrams(done(1, 0)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("got %#v, want a non-nil empty table", entries)
	}
}

func TestIgnoresAnswersToOtherRequests(t *testing.T) {
	entries, err := collect(ctx, datagrams(
		slices.Concat(flowMessage(1, natted), done(1, 0)),
		slices.Concat(flowMessage(2, established), done(2, 0)),
	), 2)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, len(entries), 1)
	assertEqual(t, entries[0].Original.SourcePort, 51000)
}

func TestWithoutCAP_NET_ADMINItIsAPermissionError(t *testing.T) {
	entries, err := collect(ctx, datagrams(failure(1, syscall.EPERM)), 1)
	if !errors.Is(err, syscall.EPERM) || !errors.Is(err, os.ErrPermission) || entries != nil {
		t.Fatalf("got %v, %v; want EPERM and no table", entries, err)
	}
}

func TestAnAcknowledgementIsNotTheEnd(t *testing.T) {
	entries, err := collect(ctx, datagrams(failure(1, 0), slices.Concat(flowMessage(1, natted), done(1, 0))), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, len(entries), 1)
}

func TestADumpThatFailsPartWayIsAnError(t *testing.T) {
	_, err := collect(ctx, datagrams(slices.Concat(flowMessage(1, natted), done(1, -int32(syscall.EINTR)))), 1)
	if !errors.Is(err, syscall.EINTR) {
		t.Fatalf("got %v, want EINTR", err)
	}
}

func TestAReceiveFailureIsAnError(t *testing.T) {
	_, err := collect(ctx, datagrams(flowMessage(1, natted)), 1)
	if err == nil {
		t.Fatal("a dump that never finished read as a table")
	}
}

func TestACancelledContextStopsTheDump(t *testing.T) {
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := collect(cancelled, table(1), 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

// what is a flow, with these of its top-level attributes in place of the
// usual ones.
func with(f flow, replace map[uint16][]byte) []byte {
	data := f.body()
	out := data[:nfgenmsgLength:nfgenmsgLength]
	for rest := data[nfgenmsgLength:]; len(rest) > 0; {
		length := (int(binary.NativeEndian.Uint16(rest)) + 3) &^ 3
		kind := binary.NativeEndian.Uint16(rest[2:]) &^ nlaFNested
		if replacement, ok := replace[kind]; ok {
			out = append(out, replacement...)
		} else {
			out = append(out, rest[:length]...)
		}
		rest = rest[length:]
	}
	return out
}

// withExtra is a flow with these attributes after the usual ones.
func withExtra(f flow, extra ...[]byte) []byte {
	f.extra = extra
	return f.body()
}

func TestMalformedMessagesAreErrorsNotSkipped(t *testing.T) {
	overrun := natted.body()
	// The first attribute claims more bytes than the message has.
	binary.NativeEndian.PutUint16(overrun[nfgenmsgLength:], uint16(len(overrun)))
	v4, v6 := addr("10.0.0.1"), addr("2001:db8::1")
	for name, data := range map[string][]byte{
		"an attribute overrunning its message": overrun,
		"a message shorter than nfgenmsg":      {2},
		"an unknown address family":            append([]byte{7, 0, 0, 0}, natted.body()[nfgenmsgLength:]...),
		"no reply tuple":                       with(natted, map[uint16][]byte{ctaTupleReply: nil}),
		"IPv6 addresses in an IPv4 flow": with(natted, map[uint16][]byte{
			ctaTupleOrig: tuple(ctaTupleOrig, v6, v6, TCP, 1, 2)}),
		"a protocol each way": with(natted, map[uint16][]byte{
			ctaTupleReply: tuple(ctaTupleReply, v4, v4, UDP, 1, 2)}),
		"a TCP flow with no ports": with(natted, map[uint16][]byte{
			ctaTupleOrig: nest(ctaTupleOrig, nest(ctaTupleIP, attribute(ctaIPv4Src, v4.AsSlice()), attribute(ctaIPv4Dst, v4.AsSlice())),
				nest(ctaTupleProto, attribute(ctaProtoNum, []byte{byte(TCP)})))}),
		"a TCP flow with no state":       with(natted, map[uint16][]byte{ctaProtoinfo: nest(ctaProtoinfo)}),
		"a status of the wrong size":     with(natted, map[uint16][]byte{ctaStatus: attribute(ctaStatus, be16(1))}),
		"a counter of the wrong size":    withExtra(natted, nest(ctaCountersOrig, attribute(ctaCountersBytes, be32(1)))),
		"an ICMP flow with no type":      with(ping, map[uint16][]byte{ctaTupleOrig: nest(ctaTupleOrig, nest(ctaTupleIP, attribute(ctaIPv4Src, v4.AsSlice()), attribute(ctaIPv4Dst, v4.AsSlice())), nest(ctaTupleProto, attribute(ctaProtoNum, []byte{byte(ICMP)})))}),
		"stray bytes after the last one": append(natted.body(), 1, 2),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFlow(data); err == nil {
				t.Fatal("read a malformed flow")
			}
			if _, err := collect(ctx, datagrams(slices.Concat(message(flowGet, 1, data), done(1, 0))), 1); err == nil {
				t.Fatal("read a malformed dump as a table")
			}
		})
	}
	t.Run("a netlink header overrunning", func(t *testing.T) {
		if _, err := collect(ctx, datagrams(slices.Concat(flowMessage(1, natted)[:20], done(1, 0))), 1); err == nil {
			t.Fatal("read a malformed dump as a table")
		}
	})
	t.Run("a message that is not a flow", func(t *testing.T) {
		_, err := collect(ctx, datagrams(slices.Concat(message(nfnlSubsysCTNetlink<<8|2, 1, natted.body()), done(1, 0))), 1)
		if err == nil || !strings.Contains(err.Error(), "unexpected netlink message") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestTheRequestAsksForADumpOfOneFamily(t *testing.T) {
	got := request(7, IPv6)
	order := binary.NativeEndian
	assertEqual(t, len(got), 20)
	assertEqual(t, order.Uint32(got[0:]), 20)
	assertEqual(t, order.Uint16(got[4:]), 0x0101) // NFNL_SUBSYS_CTNETLINK << 8 | IPCTNL_MSG_CT_GET
	assertEqual(t, order.Uint16(got[6:]), syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	assertEqual(t, order.Uint32(got[8:]), 7)
	assertEqual(t, got[16:], []byte{syscall.AF_INET6, 0, 0, 0})
}

// The real kernel, where this user may read it: as root, or in a network
// namespace of its own. Anywhere without CAP_NET_ADMIN - a desktop, Nix's
// sandbox - the kernel says EPERM, and with no conntrack in the kernel at all
// something else that is not this code's fault.
func TestDumpsTheRealConntrackTable(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback to make a flow on: %v", err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer func() {
		if conn := <-accepted; conn != nil {
			conn.Close()
		}
	}()

	entries, err := Kernel().Dump(ctx)
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("no CAP_NET_ADMIN to read the table with: %v", err)
	}
	for _, unavailable := range []error{syscall.EPROTONOSUPPORT, syscall.EAFNOSUPPORT, syscall.EOPNOTSUPP, syscall.EINVAL, syscall.ENOENT} {
		if errors.Is(err, unavailable) {
			t.Skipf("no conntrack table to read here: %v", err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if entries == nil {
		t.Fatal("a successful dump returned nil, which reads as could-not-tell")
	}
	local := client.LocalAddr().(*net.TCPAddr).AddrPort()
	server := listener.Addr().(*net.TCPAddr).AddrPort()
	for _, entry := range entries {
		if entry.Protocol == TCP && entry.Original.SourcePort == int(local.Port()) && entry.Original.DestinationPort == int(server.Port()) {
			// Loopback is tracked only where some rule needs conntrack, so
			// the flow is checked when it is there rather than required.
			assertEqual(t, entry.Family, IPv4)
			assertEqual(t, entry.Original.Source, local.Addr())
			assertEqual(t, entry.Original.Destination, server.Addr())
			assertEqual(t, entry.Reply.SourcePort, int(server.Port()))
			assertEqual(t, entry.TCPState, TCPEstablished)
			return
		}
	}
	t.Logf("read %d flows; loopback is not tracked here", len(entries))
}
