package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/rc"
)

func TestBandwidthLimitIsAppliedAndReset(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "payload"), make([]byte, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	for i, limit := range []int{1, 0, 2} {
		var configured int64
		start := time.Now()
		p, err := Run(context.Background(), Job{ID: "bandwidth", Source: Location{Path: src}, Destination: Location{Path: t.TempDir()}, BandwidthMBps: limit, Log: func(line Line) {
			if strings.HasPrefix(line.Text, "rclone ") {
				out, err := rc.Calls.Get("core/bwlimit").Fn(context.Background(), rc.Params{})
				if err != nil {
					t.Error(err)
					return
				}
				configured = out["bytesPerSecond"].(int64)
			}
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(limit) << 20
		if limit == 0 {
			want = -1
		}
		if configured != want {
			t.Fatalf("run %d limiter=%d", i, configured)
		}
		if limit == 1 && time.Since(start) < time.Second {
			t.Fatalf("copied %d bytes too quickly", p.Bytes)
		}
		// Reset is observable without a timing-dependent assertion on slow machines.
		out, err := rc.Calls.Get("core/bwlimit").Fn(context.Background(), rc.Params{})
		if err != nil || out["bytesPerSecond"].(int64) != -1 {
			t.Fatalf("limiter not reset: %+v %v", out, err)
		}
	}
}

func TestUnsafeEndpointsNeverReachLogs(t *testing.T) {
	for _, endpoint := range []string{"https://user:private-password@host", "https://host/?token=private-token", "https://host/#private-fragment"} {
		var logs strings.Builder
		loc := Location{S3: &S3{Endpoint: endpoint}}
		if _, err := Run(context.Background(), Job{ID: "unsafe", Source: loc, Destination: Location{Path: t.TempDir()}, Log: func(l Line) { logs.WriteString(l.Text) }}, nil); err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe error: %v", err)
		}
		if strings.Contains(logs.String(), "private-") || strings.Contains(describe(loc), "private-") {
			t.Fatal("endpoint secret logged")
		}
		if _, err := makeFs(context.Background(), loc, false); err == nil {
			t.Fatal("unsafe connection allowed")
		}
	}
}
