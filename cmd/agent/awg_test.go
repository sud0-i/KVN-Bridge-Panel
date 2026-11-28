package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

func TestAWGPeerArgsRepairsMissingAllowedIP(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	desired := []protocol.AWGPeer{{PublicKey: key, IP: "10.67.41.3"}}
	for _, test := range []struct {
		name    string
		current string
		repair  bool
	}{
		{name: "new peer", repair: true},
		{name: "missing address", current: key + "\t(none)", repair: true},
		{name: "wrong address", current: key + "\t10.67.41.2/32", repair: true},
		{name: "correct address", current: key + "\t10.67.41.3/32"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := []string{"set", "kvnawg0"}
			if test.repair {
				want = append(want, "peer", key, "allowed-ips", "10.67.41.3/32")
			}
			got, err := awgPeerArgs("kvnawg0", "10.67.41.1/24", test.current, desired)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("awg arguments = %v, %v; want %v", got, err, want)
			}
		})
	}
}
