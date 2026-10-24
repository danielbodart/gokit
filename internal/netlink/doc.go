// Package netlink is the little of the kernel's netlink protocol that gokit's
// kernel readers share: a socket that sends a dump request and reads its
// answer, datagram by datagram, until the kernel says it is done or that it
// failed, and the attributes each answer carries.
//
// The syscall package has the netlink basics - the header, its parser, the
// socket - but nothing that reads a dump or its attributes, and every one of
// sockdiag's and conntrack's mistakes worth making lives in those.
package netlink
