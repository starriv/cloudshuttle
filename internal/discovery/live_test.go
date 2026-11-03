package discovery

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestBrowseLive browses the network this computer is on; set
// CLOUDSHUTTLE_TEST_BROWSE to run it.
func TestBrowseLive(t *testing.T) {
	if os.Getenv("CLOUDSHUTTLE_TEST_BROWSE") == "" {
		t.Skip("CLOUDSHUTTLE_TEST_BROWSE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	servers, err := Browse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		t.Logf("%q: connect to %s (name %s, addr %s, port %d)", s.Name, s.Host, s.HostName, s.Addr, s.Port)
	}
	t.Logf("%d servers", len(servers))
}
