package sockdiag

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"path/filepath"
	"syscall"
	"time"

	"github.com/danielbodart/gokit/internal/netlink"
)

// Kernel is the real machine's listening sockets, asked of the kernel
// directly over NETLINK_SOCK_DIAG - the interface `ss` itself uses - so
// nothing has to be installed, run or parsed. It needs no privilege: every
// socket, and its cgroup, is visible to any user.
//
// Each socket comes with the id of the cgroup that created it (Linux 5.7 and
// later), which is the inode of that cgroup's directory in the cgroup2 file
// system - or, on a 32-bit kernel, holds it in its low half; see cgroupPath.
// So the ids are put back to paths by walking that file system, as ss does.
func Kernel() Lister { return kernel{cgroupRoot: "/sys/fs/cgroup"} }

type kernel struct {
	// Where the cgroup2 file system is, or contains it at unified/ on a host
	// still mounting the v1 hierarchies.
	cgroupRoot string
}

// From linux/netlink.h, linux/sock_diag.h, linux/inet_diag.h and
// net/tcp_states.h; the syscall package has the netlink basics but none of
// sock_diag.
const (
	netlinkSockDiag  = 4
	sockDiagByFamily = 20

	inetDiagSkV6Only = 11
	inetDiagCgroupID = 21

	tcpListen = 10
	// An unconnected UDP socket is in TCP_CLOSE, which ss shows as UNCONN.
	tcpClose = 7

	cgroup2SuperMagic = 0x63677270

	// inet_diag_msg: family, state, timer, retrans, then inet_diag_sockid (48
	// bytes) and five u32s.
	inetDiagMsgLength = 72
)

// dumpTimeout bounds each receive of a dump that ctx does not: a kernel that
// stopped answering part way must not stall the caller for ever.
const dumpTimeout = 10 * time.Second

type query struct {
	protocol Protocol
	family   uint8
	number   uint8
	state    uint
}

// queries is everything listed, each a dump of its own: the kernel answers a
// sock_diag request for one family and one protocol.
var queries = []query{
	{TCP, syscall.AF_INET, syscall.IPPROTO_TCP, tcpListen},
	{TCP, syscall.AF_INET6, syscall.IPPROTO_TCP, tcpListen},
	{UDP, syscall.AF_INET, syscall.IPPROTO_UDP, tcpClose},
	{UDP, syscall.AF_INET6, syscall.IPPROTO_UDP, tcpClose},
}

func (k kernel) Listening(ctx context.Context) ([]Listener, error) {
	socket, err := netlink.Open(ctx, "sock_diag", netlinkSockDiag, dumpTimeout)
	if err != nil {
		return nil, err
	}
	defer socket.Close()

	var listeners []Listener
	for i, q := range queries {
		found, err := dump(ctx, socket, uint32(i+1), q)
		if err != nil {
			return nil, fmt.Errorf("listing %s sockets: %w", q.protocol, err)
		}
		listeners = append(listeners, found...)
	}

	err = nameCgroups(listeners, k.cgroupRoot)
	if err != nil && !errors.Is(err, ErrUnknownCgroups) {
		return nil, err
	}
	return listeners, err
}

// nameCgroups fills in each listener's Cgroup from its CgroupID, or says why
// it cannot with an error wrapping ErrUnknownCgroups.
func nameCgroups(listeners []Listener, root string) error {
	if len(listeners) == 0 {
		return nil
	}
	if !anyCgroup(listeners) {
		// Every socket was made in some cgroup, if only the root, so a kernel
		// that names none is one that does not say.
		return fmt.Errorf("the kernel reports no cgroup for any socket (it needs Linux 5.7 or later), so %w", ErrUnknownCgroups)
	}
	// A host on cgroup v1 alone still sends an id - its default hierarchy's
	// root - but there is no cgroup2 tree to look it up in, and cgroupPaths
	// says so.
	paths, err := cgroupPaths(root)
	if err != nil {
		return err
	}
	for i := range listeners {
		listeners[i].Cgroup = cgroupPath(paths, listeners[i].CgroupID)
	}
	return nil
}

func anyCgroup(listeners []Listener) bool {
	for _, listener := range listeners {
		if listener.CgroupID != 0 {
			return true
		}
	}
	return false
}

// request is an inet_diag_req_v2 dump request: every socket of one family and
// protocol in one state, from anywhere to anywhere.
func request(seq uint32, q query) []byte {
	body := make([]byte, 56)
	body[0] = q.family
	body[1] = q.number
	// idiag_ext asks only for the extensions numbered up to 8. The two read
	// here, v6only and the cgroup id, come with every socket regardless.
	binary.NativeEndian.PutUint32(body[4:], 1<<q.state)
	return netlink.Request(sockDiagByFamily, seq, body)
}

func dump(ctx context.Context, socket *netlink.Socket, seq uint32, q query) ([]Listener, error) {
	if err := socket.Send(request(seq, q)); err != nil {
		return nil, err
	}
	var listeners []Listener
	err := netlink.Collect(ctx, socket.Receive, seq, func(message syscall.NetlinkMessage) error {
		if message.Header.Type != sockDiagByFamily {
			return nil
		}
		listener, err := parseDiag(message.Data, q.protocol)
		if err != nil {
			return err
		}
		listeners = append(listeners, listener)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return listeners, nil
}

// parseDiag reads one inet_diag_msg and the attributes after it.
func parseDiag(data []byte, protocol Protocol) (Listener, error) {
	if len(data) < inetDiagMsgLength {
		return Listener{}, fmt.Errorf("short inet_diag_msg: %d bytes", len(data))
	}
	order := binary.NativeEndian
	listener := Listener{
		Protocol: protocol,
		// Ports and addresses are in network order, everything else in the
		// host's.
		Port:      int(binary.BigEndian.Uint16(data[4:6])),
		Interface: int(order.Uint32(data[40:44])),
	}
	switch data[0] {
	case syscall.AF_INET:
		listener.Address = netip.AddrFrom4([4]byte(data[8:12]))
	case syscall.AF_INET6:
		listener.Address = netip.AddrFrom16([16]byte(data[8:24]))
	default:
		return Listener{}, fmt.Errorf("unexpected address family %d", data[0])
	}

	attributes, err := netlink.Attributes(data[inetDiagMsgLength:])
	if err != nil {
		return Listener{}, err
	}
	if v6only := attributes[inetDiagSkV6Only]; len(v6only) >= 1 {
		listener.V6Only = v6only[0] != 0
	}
	if id := attributes[inetDiagCgroupID]; len(id) >= 8 {
		listener.CgroupID = order.Uint64(id)
	}
	return listener, nil
}

// cgroupPaths is every cgroup's path, such as /system.slice/sshd.service, by
// the inode of its directory - which on cgroup2 is its id.
func cgroupPaths(root string) (map[uint64]string, error) {
	root, err := cgroup2(root)
	if err != nil {
		return nil, err
	}
	paths := map[uint64]string{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			// A cgroup removed while walking, or one this user may not
			// list: its sockets go unmatched, as they would have been a
			// moment later or earlier.
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			paths[stat.Ino] = "/"
		} else {
			paths[stat.Ino] = "/" + filepath.ToSlash(relative)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading cgroups: %w", err)
	}
	return paths, nil
}

// cgroupPath is the path of the cgroup the kernel gave this id, or "" if it
// is not one of paths.
//
// The id is the cgroup directory's kernfs node id, and that is its inode
// number only where ino_t is 64 bits. A 32-bit kernel - armv7, such as 32-bit
// Raspberry Pi OS - keeps the inode in the low half and a generation,
// starting at 1, in the high half, so no id there ever equals the inode a stat
// reports. ss sidesteps this by asking for every directory's file handle;
// falling back to the low half gives the same answer without a syscall per
// cgroup, as no two live cgroups share an inode. On a 64-bit kernel the id is
// the inode, so the whole id is what matches there.
func cgroupPath(paths map[uint64]string, id uint64) string {
	if path, ok := paths[id]; ok {
		return path
	}
	return paths[uint64(uint32(id))]
}

// cgroup2 is where the cgroup2 hierarchy is mounted: root itself on a host
// with only cgroup2, which is every current distribution, or root/unified on
// one in systemd's hybrid mode.
func cgroup2(root string) (string, error) {
	for _, candidate := range []string{root, filepath.Join(root, "unified")} {
		var stat syscall.Statfs_t
		if syscall.Statfs(candidate, &stat) == nil && stat.Type == cgroup2SuperMagic {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no cgroup2 file system at %s, so %w", root, ErrUnknownCgroups)
}
