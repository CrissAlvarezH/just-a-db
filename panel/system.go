package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server usage, read from /proc and statfs. /proc in a container shows the
// host's memory, load and CPU, so this is the instance's usage, not the
// panel's. Only works on Linux; elsewhere the fields stay zero and the
// page hides them.
type SystemStats struct {
	OK          bool
	MemTotal    uint64
	MemUsed     uint64
	SwapTotal   uint64
	SwapUsed    uint64
	DiskTotal   uint64
	DiskUsed    uint64
	DiskPath    string
	CPUs        int
	CPUPercent  float64
	Load1       float64
	Load5       float64
	Load15      float64
	Uptime      time.Duration
	HasCPUUsage bool
}

// System samples CPU usage in the background (it needs two readings of
// /proc/stat some time apart); everything else is read on each request.
type System struct {
	diskPath string

	mu      sync.Mutex
	cpuPct  float64
	cpuOK   bool
	lastCPU cpuTimes
}

type cpuTimes struct{ idle, total uint64 }

func NewSystem(diskPath string) *System {
	s := &System{diskPath: diskPath}
	if t, ok := readCPU(); ok {
		s.lastCPU = t
		go s.sampleCPU()
	}
	return s
}

func (s *System) sampleCPU() {
	for range time.Tick(5 * time.Second) {
		t, ok := readCPU()
		if !ok {
			continue
		}
		s.mu.Lock()
		if dt := t.total - s.lastCPU.total; dt > 0 {
			s.cpuPct = 100 * float64(dt-(t.idle-s.lastCPU.idle)) / float64(dt)
			s.cpuOK = true
		}
		s.lastCPU = t
		s.mu.Unlock()
	}
}

func readCPU() (cpuTimes, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuTimes{}, false
	}
	// cpu  user nice system idle iowait irq softirq steal ...
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		t.total += n
		if i == 3 || i == 4 { // idle, iowait
			t.idle += n
		}
	}
	return t, true
}

func (s *System) Stats() SystemStats {
	st := SystemStats{CPUs: runtime.NumCPU(), DiskPath: s.diskPath}

	if mem, err := readKV("/proc/meminfo"); err == nil {
		st.OK = true
		st.MemTotal = mem["MemTotal"] * 1024
		st.MemUsed = st.MemTotal - mem["MemAvailable"]*1024
		st.SwapTotal = mem["SwapTotal"] * 1024
		st.SwapUsed = st.SwapTotal - mem["SwapFree"]*1024
	}

	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			st.Load1, _ = strconv.ParseFloat(f[0], 64)
			st.Load5, _ = strconv.ParseFloat(f[1], 64)
			st.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}

	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 1 {
			secs, _ := strconv.ParseFloat(f[0], 64)
			st.Uptime = time.Duration(secs) * time.Second
		}
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs(s.diskPath, &fs); err == nil {
		bsize := uint64(fs.Bsize)
		st.DiskTotal = fs.Blocks * bsize
		// Bfree includes blocks reserved for root, which Postgres can't use:
		// "used" counts them, like df does.
		st.DiskUsed = st.DiskTotal - fs.Bfree*bsize
	}

	s.mu.Lock()
	st.CPUPercent, st.HasCPUUsage = s.cpuPct, s.cpuOK
	s.mu.Unlock()
	return st
}

// readKV parses "Key:   123 kB" lines (/proc/meminfo).
func readKV(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			out[key], _ = strconv.ParseUint(f[0], 10, 64)
		}
	}
	return out, sc.Err()
}
