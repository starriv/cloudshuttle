package discovery

import (
	"net/netip"
	"slices"
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
	r := newResults(SMB)
	// One server answers whole; another gives its instance only, then
	// its SRV record, then its address, as later answers.
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(SMB, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Disk Station._smb._tcp.local.")}),
		rr("Disk Station._smb._tcp.local.", &dnsmessage.SRVResource{Port: 445, Target: dnsmessage.MustNewName("ds.local.")}),
	}, Additionals: []dnsmessage.Resource{
		rr("ds.local.", &dnsmessage.AResource{A: [4]byte{192, 168, 1, 20}}),
	}})
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(SMB, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Backup._smb._tcp.local.")}),
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
	msg, err := query([]dnsmessage.Question{question(SMB, dnsmessage.TypePTR)})
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(msg); err != nil {
		t.Fatal(err)
	}
	if m.Response || len(m.Questions) != 1 || m.Questions[0].Name.String() != SMB {
		t.Errorf("query = %+v", m)
	}
}

// An NFS server may say its export in a TXT record; an SMB answer in the
// same packet is not one of them.
func TestNFSAnswersCarryTheirPath(t *testing.T) {
	r := newResults(NFS)
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(NFS, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Tank._nfs._tcp.local.")}),
		rr("Tank._nfs._tcp.local.", &dnsmessage.SRVResource{Port: 2049, Target: dnsmessage.MustNewName("tank.local.")}),
		rr("Tank._nfs._tcp.local.", &dnsmessage.TXTResource{TXT: []string{"path=/mnt/pool/backup"}}),
		rr(SMB, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Tank._smb._tcp.local.")}),
		rr("Tank._smb._tcp.local.", &dnsmessage.SRVResource{Port: 445, Target: dnsmessage.MustNewName("tank.local.")}),
	}, Additionals: []dnsmessage.Resource{
		rr("tank.local.", &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}}),
	}})
	got := r.servers(nil)
	if len(got) != 1 || got[0].Port != 2049 || got[0].Path != "/mnt/pool/backup" || got[0].Name != "Tank" {
		t.Fatalf("servers = %+v", got)
	}
}

// One NFS server announces each of its exports as an instance of its own:
// each is a row to offer, while an SMB server is listed once.
func TestNFSExportsOfOneServerAreAllListed(t *testing.T) {
	r := newResults(NFS)
	r.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(NFS, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Backup._nfs._tcp.local.")}),
		rr("Backup._nfs._tcp.local.", &dnsmessage.SRVResource{Port: 2049, Target: dnsmessage.MustNewName("tank.local.")}),
		rr("Backup._nfs._tcp.local.", &dnsmessage.TXTResource{TXT: []string{"path=/mnt/pool/backup"}}),
		rr(NFS, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Media._nfs._tcp.local.")}),
		rr("Media._nfs._tcp.local.", &dnsmessage.SRVResource{Port: 2049, Target: dnsmessage.MustNewName("tank.local.")}),
		rr("Media._nfs._tcp.local.", &dnsmessage.TXTResource{TXT: []string{"path=/mnt/pool/media"}}),
		// Another instance of the same export is the export once.
		rr(NFS, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Backup2._nfs._tcp.local.")}),
		rr("Backup2._nfs._tcp.local.", &dnsmessage.SRVResource{Port: 2049, Target: dnsmessage.MustNewName("tank.local.")}),
		rr("Backup2._nfs._tcp.local.", &dnsmessage.TXTResource{TXT: []string{"path=/mnt/pool/backup"}}),
	}, Additionals: []dnsmessage.Resource{
		rr("tank.local.", &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}}),
	}})
	got := r.servers(nil)
	want := []Server{
		{Name: "Backup", HostName: "tank.local", Addr: netip.MustParseAddr("10.0.0.5"), Port: 2049, Path: "/mnt/pool/backup"},
		{Name: "Media", HostName: "tank.local", Addr: netip.MustParseAddr("10.0.0.5"), Port: 2049, Path: "/mnt/pool/media"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("servers =\n%+v\nwant\n%+v", got, want)
	}

	// SMB announces its shares as folders, not servers: one address is
	// one row.
	smb := newResults(SMB)
	smb.add(dnsmessage.Message{Answers: []dnsmessage.Resource{
		rr(SMB, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Photos._smb._tcp.local.")}),
		rr("Photos._smb._tcp.local.", &dnsmessage.SRVResource{Port: 445, Target: dnsmessage.MustNewName("nas.local.")}),
		rr(SMB, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Media._smb._tcp.local.")}),
		rr("Media._smb._tcp.local.", &dnsmessage.SRVResource{Port: 445, Target: dnsmessage.MustNewName("nas.local.")}),
	}, Additionals: []dnsmessage.Resource{
		rr("nas.local.", &dnsmessage.AResource{A: [4]byte{10, 0, 0, 6}}),
	}})
	if got := smb.servers(nil); len(got) != 1 {
		t.Fatalf("an SMB server listed %d times: %+v", len(got), got)
	}
}
