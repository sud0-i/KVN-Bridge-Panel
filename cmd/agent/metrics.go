package main

import (
	"bufio"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Показатели сервера для панели: процессор (и steal — сколько отнимают соседи по
// физическому серверу), память, диск, сеть, TCP-ретрансмиты. Читаются из /proc при
// каждой синхронизации; скорости — разница с прошлым замером.

var procRoot = "/proc" // в тестах — каталог с подготовленными файлами

type counters struct {
	at                time.Time
	cpuTotal, cpuIdle uint64
	cpuSteal          uint64
	rx, tx            uint64
	outSegs, retrans  uint64
	haveCPU, haveNet  bool
	haveTCP           bool
}

type metricsSampler struct {
	prev counters
	now  func() time.Time
	disk func(path string) (total, free uint64, err error)
}

func newMetricsSampler() *metricsSampler {
	return &metricsSampler{now: time.Now, disk: func(path string) (uint64, uint64, error) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			return 0, 0, err
		}
		return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
	}}
}

func readLines(name string) []string {
	f, err := os.Open(filepath.Join(procRoot, name))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func ptr(v float64) *float64   { return &v }

func (m *metricsSampler) sample() protocol.NodeMetrics {
	var out protocol.NodeMetrics
	cur := counters{at: m.now()}
	out.Cores = runtime.NumCPU()

	// Процессор: cpu user nice system idle iowait irq softirq steal …
	for _, l := range readLines("stat") {
		f := strings.Fields(l)
		if len(f) < 9 || f[0] != "cpu" {
			continue
		}
		var vals []uint64
		for _, x := range f[1:] {
			v, _ := strconv.ParseUint(x, 10, 64)
			vals = append(vals, v)
		}
		for i, v := range vals {
			if i < 8 { // guest уже входит в user
				cur.cpuTotal += v
			}
		}
		cur.cpuIdle = vals[3] + vals[4]
		cur.cpuSteal = vals[7]
		cur.haveCPU = true
	}
	if cur.haveCPU && m.prev.haveCPU && cur.cpuTotal > m.prev.cpuTotal {
		dt := float64(cur.cpuTotal - m.prev.cpuTotal)
		out.CPU = ptr(round1(100 * (dt - float64(cur.cpuIdle-m.prev.cpuIdle)) / dt))
		out.Steal = ptr(round1(100 * float64(cur.cpuSteal-m.prev.cpuSteal) / dt))
	}

	// Память: занято = всего − доступно (кеш не считаем занятым)
	var memTotal, memAvail float64
	for _, l := range readLines("meminfo") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "MemTotal:":
			memTotal = v
		case "MemAvailable:":
			memAvail = v
		}
	}
	if memTotal > 0 {
		out.Mem = round1(100 * (memTotal - memAvail) / memTotal)
		out.MemTotalMB = int(memTotal / 1024)
	}

	if total, free, err := m.disk("/"); err == nil && total > 0 {
		out.Disk = round1(100 * float64(total-free) / float64(total))
		out.DiskTotalGB = round1(float64(total) / (1 << 30))
	}

	if l := readLines("loadavg"); len(l) > 0 {
		if f := strings.Fields(l[0]); len(f) > 0 {
			out.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if l := readLines("uptime"); len(l) > 0 {
		if f := strings.Fields(l[0]); len(f) > 0 {
			up, _ := strconv.ParseFloat(f[0], 64)
			out.UptimeSec = int64(up)
		}
	}

	// Сеть — по интерфейсу маршрута по умолчанию: мосты Docker не удваивают трафик
	iface := defaultIface()
	for _, l := range readLines("net/dev") {
		name, rest, ok := strings.Cut(l, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "lo" || (iface != "" && name != iface) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		cur.rx += rx
		cur.tx += tx
		cur.haveNet = true
	}
	secs := cur.at.Sub(m.prev.at).Seconds()
	if cur.haveNet && m.prev.haveNet && secs > 0 && cur.rx >= m.prev.rx && cur.tx >= m.prev.tx {
		out.RxBps = ptr(math.Round(float64(cur.rx-m.prev.rx) * 8 / secs))
		out.TxBps = ptr(math.Round(float64(cur.tx-m.prev.tx) * 8 / secs))
	}

	// TCP: переотправленные сегменты — потери, которые видит сам сервер
	var header []string
	for _, l := range readLines("net/snmp") {
		if !strings.HasPrefix(l, "Tcp:") {
			continue
		}
		f := strings.Fields(l)
		if header == nil {
			header = f
			continue
		}
		for i := 1; i < len(f) && i < len(header); i++ {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			switch header[i] {
			case "OutSegs":
				cur.outSegs = v
			case "RetransSegs":
				cur.retrans = v
			}
		}
		cur.haveTCP = true
	}
	// Меньше 1000 сегментов за минуту — сервер почти простаивает, доля была бы шумом
	if cur.haveTCP && m.prev.haveTCP && cur.outSegs >= m.prev.outSegs+1000 && cur.retrans >= m.prev.retrans {
		out.Retrans = ptr(math.Round(10000*float64(cur.retrans-m.prev.retrans)/float64(cur.outSegs-m.prev.outSegs)) / 100)
	}
	for _, l := range readLines("net/sockstat") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "TCP:" && f[1] == "inuse" {
			out.Conns, _ = strconv.Atoi(f[2])
		}
	}

	m.prev = cur
	return out
}

// defaultIface — интерфейс маршрута по умолчанию (из /proc/net/route)
func defaultIface() string {
	for _, l := range readLines("net/route") {
		f := strings.Fields(l)
		if len(f) > 1 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

func (m *metricsSampler) header() string {
	raw, _ := json.Marshal(m.sample())
	return url.QueryEscape(string(raw))
}
