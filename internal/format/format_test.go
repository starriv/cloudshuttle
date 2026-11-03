package format

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 12_400_000: "12.4 MB", 3_500_000_000_000: "3.5 TB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{4 * time.Second: "4s", 125 * time.Second: "2m 05s", 62 * time.Minute: "1h 02m"} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}
