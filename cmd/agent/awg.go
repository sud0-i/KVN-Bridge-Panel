package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

const managedAWGInterface = "kvnawg0"

func (a *agent) awgInterfaceName() string {
	if a.awgInterface != "" {
		return a.awgInterface
	}
	if marker, err := os.ReadFile("/etc/kvn-awg/managed"); err == nil && strings.TrimSpace(string(marker)) == managedAWGInterface {
		return managedAWGInterface
	}
	return ""
}

func awgCommand(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	binary := "awg"
	if len(args) > 1 && args[1] == managedAWGInterface {
		if _, err := os.Stat("/usr/local/lib/kvn-awg/awg"); err == nil {
			binary = "/usr/local/lib/kvn-awg/awg"
		}
	}
	out, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return "", fmt.Errorf("awg %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func awgStatus(iface string) (protocol.AWGStatus, error) {
	var status protocol.AWGStatus
	nic, err := net.InterfaceByName(iface)
	if err != nil {
		return status, err
	}
	addresses, err := nic.Addrs()
	if err != nil {
		return status, err
	}
	for _, a := range addresses {
		p, err := netip.ParsePrefix(a.String())
		if err == nil && p.Addr().Is4() && p.Addr().IsPrivate() && p.Bits() >= 16 && p.Bits() <= 30 {
			if status.Address != "" {
				return status, fmt.Errorf("у %s больше одной частной сети IPv4", iface)
			}
			status.Address = p.String()
		}
	}
	if status.Address == "" {
		return status, fmt.Errorf("у %s нет частной сети IPv4", iface)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return status, err
	}
	status.Epoch = fmt.Sprintf("%s:%d", strings.TrimSpace(string(boot)), nic.Index)
	if status.PublicKey, err = awgCommand("show", iface, "public-key"); err != nil {
		return status, err
	}
	raw, err := awgCommand("show", iface, "listen-port")
	if err != nil {
		return status, err
	}
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return status, fmt.Errorf("у %s нет UDP-порта", iface)
	}
	status.Port = uint16(port)
	conf, err := awgCommand("showconf", iface)
	if err != nil {
		return status, err
	}
	status.Options = parseAWGOptions(conf)
	if !protocol.AWGKeyValid(status.Options["HeaderProtectionKey"]) {
		return status, fmt.Errorf("в настройках %s нет HeaderProtectionKey для профиля AWG 3.1", iface)
	}
	transfers, err := awgCommand("show", iface, "transfer")
	if err != nil {
		return status, err
	}
	handshakes, err := awgCommand("show", iface, "latest-handshakes")
	if err != nil {
		return status, err
	}
	status.Peers, err = parseAWGCounters(transfers, handshakes)
	return status, err
}

func parseAWGOptions(conf string) map[string]string {
	options := map[string]string{}
	for line := range strings.SplitSeq(conf, "\n") {
		if strings.TrimSpace(line) == "[Peer]" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			options[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return protocol.AWGClientOptions(options)
}

func parseAWGCounters(transfers, handshakes string) ([]protocol.AWGCounter, error) {
	times := map[string]int64{}
	for line := range strings.SplitSeq(handshakes, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			times[fields[0]], _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	out := make([]protocol.AWGCounter, 0)
	for line := range strings.SplitSeq(transfers, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 || !protocol.AWGKeyValid(fields[0]) {
			return nil, fmt.Errorf("неожиданный формат счётчиков AWG")
		}
		rx, errRx := strconv.ParseInt(fields[1], 10, 64)
		tx, errTx := strconv.ParseInt(fields[2], 10, 64)
		if errRx != nil || errTx != nil || rx < 0 || tx < 0 {
			return nil, fmt.Errorf("неожиданный формат счётчиков AWG")
		}
		out = append(out, protocol.AWGCounter{PublicKey: fields[0], Rx: rx, Tx: tx, Handshake: times[fields[0]]})
	}
	return out, nil
}

func (a *agent) syncAWG(iface string) {
	if err := a.updateAWG(iface); err != nil {
		log.Printf("⚠️ AWG: %v", err)
	}
}

func (a *agent) updateAWG(iface string) error {
	status, err := awgStatus(iface)
	if err != nil {
		return err
	}
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, a.masterURL+"/api/awg", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set(protocol.NodeTokenHeader, a.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("панель вернула HTTP %d", res.StatusCode)
	}
	var desired []protocol.AWGPeer
	if err := json.NewDecoder(res.Body).Decode(&desired); err != nil {
		return err
	}
	current := make(map[string]bool, len(status.Peers))
	for _, p := range status.Peers {
		current[p.PublicKey] = true
	}
	args := []string{"set", iface}
	pool, _ := netip.ParsePrefix(status.Address)
	for _, p := range desired {
		ip, err := netip.ParseAddr(p.IP)
		if err != nil || !ip.Is4() || !pool.Contains(ip) || ip == pool.Addr() || !protocol.AWGKeyValid(p.PublicKey) {
			return fmt.Errorf("панель передала некорректный профиль AWG")
		}
		if !current[p.PublicKey] {
			args = append(args, "peer", p.PublicKey, "allowed-ips", p.IP+"/32", "advanced-security", "on")
		}
		delete(current, p.PublicKey)
	}
	// The administrator opts in with a dedicated interface. Only KVN writes peers to it.
	for pub := range current {
		args = append(args, "peer", pub, "remove")
	}
	if len(args) > 2 {
		_, err = awgCommand(args...)
	}
	return err
}
