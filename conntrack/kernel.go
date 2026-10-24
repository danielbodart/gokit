package conntrack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"time"

	"github.com/danielbodart/gokit/internal/netlink"
)

// Kernel is the conntrack table of the network namespace this process is in,
// asked of the kernel directly over NETLINK_NETFILTER - the ctnetlink
// interface `conntrack -L` itself uses - so nothing has to be installed, run
// or parsed as text. Host-networked, that is the host's table, where NAT and
// routing happen whichever namespace a flow ends up in.
//
// One dump per address family, IPv4 then IPv6, of every flow in it.
//
// Every failure is an error, never a shorter table. In particular:
//
//   - Without CAP_NET_ADMIN the kernel refuses with EPERM, which
//     errors.Is(err, os.ErrPermission) recognises.
//   - A kernel without nfnetlink cannot open the socket (EPROTONOSUPPORT),
//     and one without nf_conntrack_netlink - not built, or a module that
//     cannot be loaded - refuses the request (EINVAL, or EOPNOTSUPP).
//   - A namespace whose conntrack has never been set up answers ENOENT on
//     some kernels.
//
// All are syscall.Errno values, wrapped, for errors.Is to find.
func Kernel() Dumper { return kernel{timeout: dumpTimeout} }

// dumpTimeout bounds each receive of a dump that ctx does not: a kernel that
// stopped answering part way must not stall the caller for ever.
const dumpTimeout = 10 * time.Second

type kernel struct{ timeout time.Duration }

// From linux/netlink.h, linux/netfilter/nfnetlink.h,
// linux/netfilter/nfnetlink_conntrack.h and
// linux/netfilter/nf_conntrack_tcp.h; the syscall package has the netlink
// basics but none of netfilter.
const (
	netlinkNetfilter = 12

	nfnlSubsysCTNetlink = 1
	ipctnlMsgCTNew      = 0
	ipctnlMsgCTGet      = 1

	// struct nfgenmsg: family, version, resource id - before every
	// ctnetlink message's attributes.
	nfgenmsgLength = 4

	// enum ctattr_type
	ctaTupleOrig     = 1
	ctaTupleReply    = 2
	ctaStatus        = 3
	ctaProtoinfo     = 4
	ctaTimeout       = 7
	ctaMark          = 8
	ctaCountersOrig  = 9
	ctaCountersReply = 10
	ctaID            = 12
	ctaZone          = 18

	// enum ctattr_tuple, ctattr_ip and ctattr_l4proto
	ctaTupleIP         = 1
	ctaTupleProto      = 2
	ctaIPv4Src         = 1
	ctaIPv4Dst         = 2
	ctaIPv6Src         = 3
	ctaIPv6Dst         = 4
	ctaProtoNum        = 1
	ctaProtoSrcPort    = 2
	ctaProtoDstPort    = 3
	ctaProtoICMPID     = 4
	ctaProtoICMPType   = 5
	ctaProtoICMPCode   = 6
	ctaProtoICMPv6ID   = 7
	ctaProtoICMPv6Type = 8
	ctaProtoICMPv6Code = 9

	// enum ctattr_protoinfo, ctattr_protoinfo_tcp and ctattr_counters
	ctaProtoinfoTCP      = 1
	ctaProtoinfoTCPState = 1

	ctaCountersPackets = 1
	ctaCountersBytes   = 2

	flowNew = nfnlSubsysCTNetlink<<8 | ipctnlMsgCTNew
	flowGet = nfnlSubsysCTNetlink<<8 | ipctnlMsgCTGet
)

// families is what is dumped, one request each: the kernel answers a
// ctnetlink dump for the family it is asked about.
var families = []Family{IPv4, IPv6}

func (k kernel) Dump(ctx context.Context) ([]Entry, error) {
	socket, err := netlink.Open(ctx, "conntrack", netlinkNetfilter, k.timeout)
	if err != nil {
		return nil, err
	}
	defer socket.Close()

	// Non-nil from the start: an empty table is "no flows", which is not the
	// same thing as the nil of "could not tell".
	entries := []Entry{}
	for i, family := range families {
		seq := uint32(i + 1)
		if err := socket.Send(request(seq, family)); err != nil {
			return nil, fmt.Errorf("asking for the conntrack table: %w", err)
		}
		found, err := collect(ctx, socket.Receive, seq)
		if err != nil {
			return nil, fmt.Errorf("reading the %s conntrack table: %w", family, err)
		}
		entries = append(entries, found...)
	}
	return entries, nil
}

// request is an IPCTNL_MSG_CT_GET dump request for every flow of one family.
func request(seq uint32, family Family) []byte {
	// nfgenmsg: the family, NFNETLINK_V0, and a resource id of 0.
	return netlink.Request(flowGet, seq, []byte{byte(family), 0, 0, 0})
}

// collect reads one dump's answer, datagram by datagram, until the kernel
// says it is done or that it failed.
//
// Separate from the socket, so it is tested against messages built in the
// test: the parsing is where the interesting mistakes live.
func collect(ctx context.Context, receive func() ([]byte, error), seq uint32) ([]Entry, error) {
	entries := []Entry{}
	err := netlink.Collect(ctx, receive, seq, func(message syscall.NetlinkMessage) error {
		switch message.Header.Type {
		case flowGet, flowNew:
			// Current kernels answer a GET as GET, older ones as NEW.
			entry, err := parseFlow(message.Data)
			if err != nil {
				return err
			}
			entries = append(entries, entry)
			return nil
		}
		return fmt.Errorf("unexpected netlink message of type %#x in a conntrack dump", message.Header.Type)
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// parseFlow reads one ctnetlink flow.
//
// Strictly: what the kernel always sends - both tuples, their addresses and
// protocol, the ports of TCP and UDP, ICMP's type, code and id, and TCP's
// state - must be there, and every attribute read must be its own size. A
// flow that does not hold together is an error rather than skipped, as the
// dump as a whole is then not to be trusted. Attributes this does not read,
// which newer kernels add to, are passed over.
func parseFlow(data []byte) (Entry, error) {
	if len(data) < nfgenmsgLength {
		return Entry{}, fmt.Errorf("short ctnetlink message: %d bytes", len(data))
	}
	entry := Entry{Family: Family(data[0])}
	if entry.Family != IPv4 && entry.Family != IPv6 {
		return Entry{}, fmt.Errorf("a conntrack flow of unexpected address family %d", data[0])
	}
	top, err := netlink.Attributes(data[nfgenmsgLength:])
	if err != nil {
		return Entry{}, err
	}

	var protocol Protocol
	if entry.Original, protocol, err = parseTuple(top, ctaTupleOrig, entry.Family); err != nil {
		return Entry{}, fmt.Errorf("the original tuple: %w", err)
	}
	entry.Protocol = protocol
	if entry.Reply, protocol, err = parseTuple(top, ctaTupleReply, entry.Family); err != nil {
		return Entry{}, fmt.Errorf("the reply tuple: %w", err)
	}
	if protocol != entry.Protocol {
		return Entry{}, fmt.Errorf("a conntrack flow of protocol %s one way and %s the other", entry.Protocol, protocol)
	}

	// Everything ctnetlink sends but nested addresses is in network order.
	be := binary.BigEndian
	if value, ok, err := fixed(top, ctaStatus, 4, "status"); err != nil {
		return Entry{}, err
	} else if ok {
		entry.Status = Status(be.Uint32(value))
	}
	if value, ok, err := fixed(top, ctaTimeout, 4, "timeout"); err != nil {
		return Entry{}, err
	} else if ok {
		entry.Timeout = time.Duration(be.Uint32(value)) * time.Second
	}
	if value, ok, err := fixed(top, ctaMark, 4, "mark"); err != nil {
		return Entry{}, err
	} else if ok {
		entry.Mark = be.Uint32(value)
	}
	if value, ok, err := fixed(top, ctaZone, 2, "zone"); err != nil {
		return Entry{}, err
	} else if ok {
		entry.Zone = be.Uint16(value)
	}
	if value, ok, err := fixed(top, ctaID, 4, "id"); err != nil {
		return Entry{}, err
	} else if ok {
		entry.ID = be.Uint32(value)
	}
	if entry.OriginalCounters, err = parseCounters(top, ctaCountersOrig); err != nil {
		return Entry{}, err
	}
	if entry.ReplyCounters, err = parseCounters(top, ctaCountersReply); err != nil {
		return Entry{}, err
	}

	if entry.Protocol == TCP {
		info, err := netlink.Attributes(top[ctaProtoinfo])
		if err != nil {
			return Entry{}, err
		}
		tcp, err := netlink.Attributes(info[ctaProtoinfoTCP])
		if err != nil {
			return Entry{}, err
		}
		state, ok, err := fixed(tcp, ctaProtoinfoTCPState, 1, "TCP state")
		if err != nil {
			return Entry{}, err
		}
		if !ok {
			return Entry{}, errors.New("a TCP conntrack flow with no state")
		}
		entry.TCPState = TCPState(state[0])
	}
	return entry, nil
}

// parseTuple reads a CTA_TUPLE_ORIG or CTA_TUPLE_REPLY, which must be there.
func parseTuple(top map[uint16][]byte, kind uint16, family Family) (Tuple, Protocol, error) {
	nest, ok := top[kind]
	if !ok {
		return Tuple{}, 0, errors.New("missing")
	}
	tuple, err := netlink.Attributes(nest)
	if err != nil {
		return Tuple{}, 0, err
	}
	ip, err := netlink.Attributes(tuple[ctaTupleIP])
	if err != nil {
		return Tuple{}, 0, err
	}
	var t Tuple
	if family == IPv4 {
		t.Source, err = address(ip, ctaIPv4Src, 4, "source")
		if err == nil {
			t.Destination, err = address(ip, ctaIPv4Dst, 4, "destination")
		}
	} else {
		t.Source, err = address(ip, ctaIPv6Src, 16, "source")
		if err == nil {
			t.Destination, err = address(ip, ctaIPv6Dst, 16, "destination")
		}
	}
	if err != nil {
		return Tuple{}, 0, err
	}

	proto, err := netlink.Attributes(tuple[ctaTupleProto])
	if err != nil {
		return Tuple{}, 0, err
	}
	number, ok, err := fixed(proto, ctaProtoNum, 1, "protocol")
	if err != nil {
		return Tuple{}, 0, err
	}
	if !ok {
		return Tuple{}, 0, errors.New("no protocol")
	}
	protocol := Protocol(number[0])

	be := binary.BigEndian
	ports := protocol == TCP || protocol == UDP
	if t.SourcePort, err = port(proto, ctaProtoSrcPort, "source", ports); err != nil {
		return Tuple{}, 0, err
	}
	if t.DestinationPort, err = port(proto, ctaProtoDstPort, "destination", ports); err != nil {
		return Tuple{}, 0, err
	}

	if protocol == ICMP || protocol == ICMPv6 {
		id, icmpType, code := uint16(ctaProtoICMPID), uint16(ctaProtoICMPType), uint16(ctaProtoICMPCode)
		if protocol == ICMPv6 {
			id, icmpType, code = ctaProtoICMPv6ID, ctaProtoICMPv6Type, ctaProtoICMPv6Code
		}
		for _, field := range []struct {
			kind   uint16
			length int
			name   string
			set    func([]byte)
		}{
			{id, 2, "ICMP id", func(v []byte) { t.ICMPID = be.Uint16(v) }},
			{icmpType, 1, "ICMP type", func(v []byte) { t.ICMPType = v[0] }},
			{code, 1, "ICMP code", func(v []byte) { t.ICMPCode = v[0] }},
		} {
			value, ok, err := fixed(proto, field.kind, field.length, field.name)
			if err != nil {
				return Tuple{}, 0, err
			}
			if !ok {
				return Tuple{}, 0, fmt.Errorf("an %s flow with no %s", protocol, field.name)
			}
			field.set(value)
		}
	}
	return t, protocol, nil
}

func address(ip map[uint16][]byte, kind uint16, length int, name string) (netip.Addr, error) {
	value, ok, err := fixed(ip, kind, length, name+" address")
	if err != nil {
		return netip.Addr{}, err
	}
	if !ok {
		return netip.Addr{}, fmt.Errorf("no %s address", name)
	}
	address, _ := netip.AddrFromSlice(value)
	return address, nil
}

// port is a tuple's port, required of a protocol that must have one.
func port(proto map[uint16][]byte, kind uint16, name string, required bool) (int, error) {
	value, ok, err := fixed(proto, kind, 2, name+" port")
	if err != nil {
		return 0, err
	}
	if !ok {
		if required {
			return 0, fmt.Errorf("no %s port", name)
		}
		return 0, nil
	}
	return int(binary.BigEndian.Uint16(value)), nil
}

func parseCounters(top map[uint16][]byte, kind uint16) (Counters, error) {
	counters, err := netlink.Attributes(top[kind])
	if err != nil {
		return Counters{}, err
	}
	var c Counters
	if value, ok, err := fixed(counters, ctaCountersPackets, 8, "packet count"); err != nil {
		return Counters{}, err
	} else if ok {
		c.Packets = binary.BigEndian.Uint64(value)
	}
	if value, ok, err := fixed(counters, ctaCountersBytes, 8, "byte count"); err != nil {
		return Counters{}, err
	} else if ok {
		c.Bytes = binary.BigEndian.Uint64(value)
	}
	return c, nil
}

// fixed is an attribute that must be exactly length bytes if it is there at
// all.
func fixed(attributes map[uint16][]byte, kind uint16, length int, name string) ([]byte, bool, error) {
	value, ok := attributes[kind]
	if !ok {
		return nil, false, nil
	}
	if len(value) != length {
		return nil, false, fmt.Errorf("a conntrack %s of %d bytes, not %d", name, len(value), length)
	}
	return value, true, nil
}
