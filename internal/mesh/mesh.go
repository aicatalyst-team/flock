// Package mesh provides the node-to-node networking layer.
//
// v0.2 ships the "lan" backend: nodes report a directly-routable address
// (host:port on the LAN), and the leader contacts them over plain HTTP.
// This requires nodes to be reachable from the leader and is appropriate
// for a single trusted network.
//
// The "tailscale" backend (using tsnet) is defined as an interface here
// and will be implemented in v0.3 — it solves NAT traversal, mTLS, and
// cross-network discovery.
package mesh

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Backend abstracts the underlying overlay network. v0.2 only ships LAN.
type Backend interface {
	Name() string
	// Address returns a host:port that other nodes can reach this node at.
	Address(port int) (string, error)
	// Hostname returns this node's display name within the mesh.
	Hostname() (string, error)
	// Close releases any resources held by the backend.
	Close() error
}

// NewLAN returns the simplest mesh backend: just report the local hostname
// + a routable IP from the default interface.
func NewLAN() Backend {
	return &lanBackend{}
}

type lanBackend struct{}

func (l *lanBackend) Name() string                    { return "lan" }
func (l *lanBackend) Hostname() (string, error)       { return os.Hostname() }
func (l *lanBackend) Close() error                    { return nil }

func (l *lanBackend) Address(port int) (string, error) {
	ip, err := outboundIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d", ip, port), nil
}

// outboundIP returns the IP of the interface used to reach 1.1.1.1.
// The dial is UDP so it doesn't actually open a connection.
func outboundIP() (string, error) {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		// fall back to scanning interfaces
		return firstNonLoopback()
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), nil
}

func firstNonLoopback() (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 interface found")
}

// SplitHostPort is a small helper for parsing addresses; returns ("", port) if
// the string is just ":<port>" so the leader can substitute its own IP.
func SplitHostPort(addr string) (host string, port string, err error) {
	if strings.HasPrefix(addr, ":") {
		return "", addr[1:], nil
	}
	return net.SplitHostPort(addr)
}
