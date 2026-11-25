package protocol

import (
	"encoding/base64"
	"strings"
	"unicode"
)

// AWGStatus carries the public server profile and consecutive counter snapshots.
type AWGStatus struct {
	PublicKey string            `json:"public_key"`
	Address   string            `json:"address"` // IPv4 address and prefix of the server interface
	Port      uint16            `json:"port"`
	Epoch     string            `json:"epoch"`
	Options   map[string]string `json:"options"`
	Peers     []AWGCounter      `json:"peers"`
}

type AWGCounter struct {
	PublicKey string `json:"public_key"`
	Rx        int64  `json:"rx"`
	Tx        int64  `json:"tx"`
	Handshake int64  `json:"handshake"`
}

type AWGPeer struct {
	PublicKey string `json:"public_key"`
	IP        string `json:"ip"`
}

var awgOptions = strings.Fields("Jc Jmin Jmax S1 S2 S3 S4 H1 H2 H3 H4 I1 I2 I3 I4 I5 HeaderProtectionKey ContentPaddingAddition RekeyAfterTime RekeyTimeout RejectAfterTime KeepaliveTimeout MaxHandshakeAttempts RandomTrailers DisableCookies")

func AWGKeyValid(key string) bool {
	b, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(b) == 32 && base64.StdEncoding.EncodeToString(b) == key
}

// AWGClientOptions only copies validated interface settings. Commands and server
// private keys must never appear in the profile shared with a client.
func AWGClientOptions(raw map[string]string) map[string]string {
	out := make(map[string]string)
	for _, name := range awgOptions {
		if value := strings.TrimSpace(raw[name]); value != "" && len(value) <= 4096 && !strings.ContainsFunc(value, unicode.IsControl) {
			out[name] = value
		}
	}
	return out
}
