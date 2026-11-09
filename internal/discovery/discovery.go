// Package discovery finds the SMB and NFS servers of the local network
// that announce themselves with Bonjour (mDNS and DNS-SD), as NAS and Macs
// sharing files do. It is a small mDNS client in pure Go: it asks from a
// port of its own, which the system's responder does not hold, and
// responders answer it directly (RFC 6762, section 6.7).
package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// The DNS-SD types of SMB and NFS file sharing.
const (
	SMB = "_smb._tcp.local."
	NFS = "_nfs._tcp.local."
)

// Server is a server that announced itself.
type Server struct {
	Name     string     // the name it announces, such as "DiskStation"
	HostName string     // its host name, such as "DiskStation.local"
	Addr     netip.Addr // its IPv4 address
	Port     int        // 445 for most SMB servers, 2049 for NFS
	// Path is the folder an NFS server announced in its TXT record, as
	// path=/volume1/backup; empty when it did not say.
	Path string
	// Host is what to connect to: HostName when this computer resolves it
	// to Addr, which keeps working as the address changes, else Addr.
	Host string
}

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Browse asks the network for servers of service, SMB or NFS, for as long
// as ctx allows, and returns those that answered with an address, by name.
// The servers of this computer are left out.
func Browse(ctx context.Context, service string) ([]Server, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	pc := ipv4.NewPacketConn(conn)
	ifaces := multicastInterfaces()
	if len(ifaces) == 0 {
		return nil, errors.New("no network interface can send multicast")
	}

	r := newResults(service)
	go func() {
		<-ctx.Done()
		conn.SetReadDeadline(time.Now()) // ends the reading below
	}()

	// Ask, then ask again for what the answers left out: a responder may
	// leave the service's host or address to a later question.
	asked := make(chan struct{})
	go func() {
		defer close(asked)
		send := func(questions []dnsmessage.Question) {
			msg, err := query(questions)
			if err != nil {
				return
			}
			for _, ifi := range ifaces {
				if pc.SetMulticastInterface(&ifi) == nil {
					pc.WriteTo(msg, nil, group)
				}
			}
		}
		send([]dnsmessage.Question{question(service, dnsmessage.TypePTR)})
		for range 2 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
			if q := r.missing(); len(q) > 0 {
				send(q)
			}
		}
	}()

	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break // the deadline, as ctx ends
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) == nil && m.Response {
			r.add(m)
		}
	}
	<-asked
	servers := r.servers(localAddrs())
	resolve(servers)
	return servers, nil
}

// resolve sets the Host of each server, looking its name up for a moment.
// Few resolvers outside macOS's look up .local names; and macOS's may not
// for every server.
func resolve(servers []Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range servers {
		s := &servers[i]
		s.Host = s.Addr.String()
		wg.Go(func() {
			addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", s.HostName)
			if err == nil && slices.ContainsFunc(addrs, func(a netip.Addr) bool { return a.Unmap() == s.Addr }) {
				s.Host = s.HostName
			}
		})
	}
	wg.Wait()
}

func query(questions []dnsmessage.Question) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	for _, q := range questions {
		if err := b.Question(q); err != nil {
			return nil, err
		}
	}
	return b.Finish()
}

func question(name string, t dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.ClassINET}
}

// results gathers what the answers say: instances of the service, where
// each runs, and the addresses of the hosts. The reader adds to it while
// the sender asks what is missing, each on a goroutine of its own.
type results struct {
	service   string
	mu        sync.Mutex
	instances map[string]bool // "diskstation._smb._tcp.local."
	srv       map[string]dnsmessage.SRVResource
	addrs     map[string]netip.Addr // host name to its IPv4 address
	names     map[string]string     // instance to its name as announced
	paths     map[string]string     // instance to the path its TXT record says
}

func newResults(service string) *results {
	return &results{
		service:   service,
		paths:     map[string]string{},
		instances: map[string]bool{},
		srv:       map[string]dnsmessage.SRVResource{},
		addrs:     map[string]netip.Addr{},
		names:     map[string]string{},
	}
}

func (r *results) add(m dnsmessage.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rr := range slices.Concat(m.Answers, m.Additionals) {
		name := strings.ToLower(rr.Header.Name.String())
		switch body := rr.Body.(type) {
		case *dnsmessage.PTRResource:
			if name == r.service {
				r.instances[strings.ToLower(body.PTR.String())] = true
			}
		case *dnsmessage.SRVResource:
			if strings.HasSuffix(name, "."+r.service) {
				r.instances[name] = true
				r.srv[name] = *body
				full := rr.Header.Name.String()
				r.names[name] = full[:len(full)-len(r.service)-1] // as announced, in its case
			}
		case *dnsmessage.TXTResource:
			if strings.HasSuffix(name, "."+r.service) {
				for _, kv := range body.TXT {
					if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "path") && strings.HasPrefix(v, "/") {
						r.paths[name] = v
					}
				}
			}
		case *dnsmessage.AResource:
			r.addrs[name] = netip.AddrFrom4(body.A)
		}
	}
}

// missing asks for the SRV records of the instances without one, and the
// addresses of the hosts without one.
func (r *results) missing() []dnsmessage.Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	var q []dnsmessage.Question
	for inst := range r.instances {
		srv, ok := r.srv[inst]
		if !ok {
			q = append(q, question(inst, dnsmessage.TypeSRV))
			continue
		}
		host := strings.ToLower(srv.Target.String())
		if _, ok := r.addrs[host]; !ok {
			q = append(q, question(host, dnsmessage.TypeA))
		}
	}
	return q
}

// servers lists the instances found whole, leaving out those at local, and
// each server once: an SMB server by its address, an NFS server by its
// address, port and export, of which it may announce several. It walks the
// instances by name, so a server announced twice always keeps the same one.
func (r *results) servers(local map[netip.Addr]bool) []Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	type announce struct {
		addr netip.Addr
		port int
		path string
	}
	instances := make([]string, 0, len(r.srv))
	for inst := range r.srv {
		instances = append(instances, inst)
	}
	slices.Sort(instances)
	seenAddr := map[netip.Addr]bool{}
	seenExport := map[announce]bool{}
	var out []Server
	for _, inst := range instances {
		srv := r.srv[inst]
		host := strings.ToLower(srv.Target.String())
		addr, ok := r.addrs[host]
		if !ok || local[addr] {
			continue
		}
		path := r.paths[inst]
		if r.service == NFS {
			key := announce{addr: addr, port: int(srv.Port), path: path}
			if seenExport[key] {
				continue
			}
			seenExport[key] = true
		} else {
			if seenAddr[addr] {
				continue
			}
			seenAddr[addr] = true
		}
		out = append(out, Server{
			Name:     r.names[inst],
			HostName: strings.TrimSuffix(srv.Target.String(), "."),
			Port:     int(srv.Port),
			Addr:     addr,
			Path:     path,
		})
	}
	slices.SortFunc(out, func(a, b Server) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func multicastInterfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && ifi.Flags&net.FlagLoopback == 0 {
			out = append(out, ifi)
		}
	}
	return out
}

func localAddrs() map[netip.Addr]bool {
	out := map[netip.Addr]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out[p.Addr().Unmap()] = true
		}
	}
	return out
}
