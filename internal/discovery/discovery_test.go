package discovery

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func rr(name string, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Class: dnsmessage.ClassINET},
		Body:   body,
	}
}

func TestResultsGatherAnswers(t *testing.T) {
	r := newResults()
	// One server answers whole; another gives its instance only, then
	// its SRV record, then its address, as later answers.
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(Service, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Disk Station._smb._tcp.local.")}),
		rr("Disk Station._smb._tcp.local.", &dnsmessage.SRVResource{Port: 445, Target: dnsmessage.MustNewName("ds.local.")}),
	}, Additionals: []dnsmessage.Resource{
		rr("ds.local.", &dnsmessage.AResource{A: [4]byte{192, 168, 1, 20}}),
	}})
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(Service, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Backup._smb._tcp.local.")}),
	}})

	q := r.missing()
	if len(q) != 1 || q[0].Type != dnsmessage.TypeSRV || q[0].Name.String() != "backup._smb._tcp.local." {
		t.Fatalf("missing = %v", q)
	}
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr("Backup._smb._tcp.local.", &dnsmessage.SRVResource{Port: 4455, Target: dnsmessage.MustNewName("backup.local.")}),
	}})
	if q := r.missing(); len(q) != 1 || q[0].Type != dnsmessage.TypeA {
		t.Fatalf("missing after SRV = %v", q)
	}
	if got := r.servers(nil); len(got) != 1 {
		t.Fatalf("a server without an address is listed: %+v", got)
	}
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr("backup.local.", &dnsmessage.AResource{A: [4]byte{192, 168, 1, 30}}),
	}})

	got := r.servers(nil)
	want := []Server{
		{Name: "Backup", HostName: "backup.local", Addr: netip.MustParseAddr("192.168.1.30"), Port: 4455},
		{Name: "Disk Station", HostName: "ds.local", Addr: netip.MustParseAddr("192.168.1.20"), Port: 445},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("servers =\n%+v\nwant\n%+v", got, want)
	}

	local := map[netip.Addr]bool{netip.MustParseAddr("192.168.1.30"): true}
	if got := r.servers(local); len(got) != 1 || got[0].Name != "Disk Station" {
		t.Errorf("this computer's server is listed: %+v", got)
	}
}

func TestQuery(t *testing.T) {
	msg, err := query([]dnsmessage.Question{question(Service, dnsmessage.TypePTR)})
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(msg); err != nil {
		t.Fatal(err)
	}
	if m.Response || len(m.Questions) != 1 || m.Questions[0].Name.String() != Service {
		t.Errorf("query = %+v", m)
	}
}
