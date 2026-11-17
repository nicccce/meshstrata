package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/platform"
	"golang.org/x/sys/unix"
)

var errProcess = errors.New("AGENT_KERNEL_PROCESS_FAILED")

const processFile = "kernel-process.json"
const launchVariable = "MESHSTRATA_KERNEL_LAUNCH="

type ProcessOptions struct {
	Binary      string
	WorkDir     string
	ConfigDir   string
	StateDir    string
	StopTimeout time.Duration
}

type launchRecord struct {
	Device uint64
	Inode  uint64
	Boot   string
	Token  string
	Args   []string
	PID    int
	Start  string
}

type Supervisor struct {
	options ProcessOptions
	boot    string
	mu      sync.Mutex
}

var _ Process = (*Supervisor)(nil)

func NewProcess(o ProcessOptions) (*Supervisor, error) {
	if o.StopTimeout == 0 {
		o.StopTimeout = 2 * time.Second
	}
	if o.StopTimeout <= 0 || o.StopTimeout > 30*time.Second {
		return nil, errProcess
	}
	for _, path := range []string{o.WorkDir, o.ConfigDir, o.StateDir} {
		d, err := checkpoint.OpenPrivateDir(path)
		if err != nil {
			return nil, err
		}
		d.Close()
	}
	if !filepath.IsAbs(o.Binary) || filepath.Clean(o.Binary) != o.Binary {
		return nil, errProcess
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || len(bytes.TrimSpace(boot)) != 36 {
		return nil, errProcess
	}
	// Probe the pidfd API before making any effectful start.
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, errProcess
	}
	unix.Close(fd)
	supervisor := &Supervisor{options: o, boot: strings.TrimSpace(string(boot))}
	f, err := pinnedBinary(o.Binary)
	if err != nil {
		// The launch record can still authenticate a running instance whose
		// installation file has been changed or removed since launch.
		stop, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return nil, errors.Join(err, supervisor.Stop(stop))
	}
	f.Close()
	return supervisor, nil
}

func pinnedBinary(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errProcess
	}
	uid := uint32(os.Geteuid())
	for p := path; p != "/"; p = filepath.Dir(p) {
		var st unix.Stat_t
		if unix.Lstat(p, &st) != nil || (st.Uid != 0 && st.Uid != uid) {
			return nil, errProcess
		}
		if p == path {
			if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&06022 != 0 || st.Mode&0100 == 0 {
				return nil, errProcess
			}
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0022 != 0 && (st.Uid != 0 || st.Mode&unix.S_ISVTX == 0) {
			return nil, errProcess
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errProcess
	}
	f := os.NewFile(uintptr(fd), path)
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (128<<20)+1))
	if err != nil || n > 128<<20 || hex.EncodeToString(h.Sum(nil)) != core.LinuxAMD64SHA256 {
		f.Close()
		return nil, errProcess
	}
	return f, nil
}

func validArgs(o ProcessOptions, args []string) bool {
	if len(args) != 4 || args[0] != "-d" || args[1] != o.WorkDir || args[2] != "-f" ||
		filepath.Dir(args[3]) != o.ConfigDir {
		return false
	}
	return validConfigName(filepath.Base(args[3]))
}

func (p *Supervisor) lock(ctx context.Context) (*checkpoint.PrivateDir, *os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	d, err := checkpoint.OpenPrivateDir(p.options.StateDir)
	if err != nil {
		return nil, nil, err
	}
	f, err := d.OpenFile("kernel-process.lock", unix.O_RDWR|unix.O_CREAT)
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		d.Close()
		return nil, nil, errProcess
	}
	return d, f, nil
}

func (p *Supervisor) record(d *checkpoint.PrivateDir) (launchRecord, error) {
	var r launchRecord
	b, err := d.Read(processFile, 8192)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, errProcess
	}
	if json.Unmarshal(b, &r) != nil || !validArgs(p.options, r.Args) || len(r.Token) != 32 || r.PID < 0 || r.PID == 1 || r.Inode == 0 || len(r.Boot) != 36 {
		return r, errProcess
	}
	if _, err := hex.DecodeString(r.Token); err != nil {
		return r, errProcess
	}
	return r, nil
}

func writeRecord(d *checkpoint.PrivateDir, r launchRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return errProcess
	}
	_, err = d.Replace(processFile, b)
	return err
}

func processStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return "", errProcess
	}
	fields := bytes.Fields(b[i+1:])
	if len(fields) < 20 {
		return "", errProcess
	}
	if string(fields[0]) == "Z" || string(fields[0]) == "X" {
		return "", os.ErrProcessDone
	}
	return string(fields[19]), nil
}

func openOwnedPID(binary string, r launchRecord, pid int) (int, string, bool) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return -1, "", false
	}
	owned := false
	defer func() {
		if !owned {
			unix.Close(fd)
		}
	}()
	start, err := processStart(pid)
	if err != nil || (r.PID == pid && r.Start != "" && r.Start != start) {
		return -1, "", false
	}
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	cmd, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil || !slices.Equal(strings.Split(strings.TrimSuffix(string(cmd), "\x00"), "\x00"), append([]string{binary}, r.Args...)) {
		return -1, "", false
	}
	env, err := os.ReadFile(filepath.Join(proc, "environ"))
	if err != nil || !slices.Contains(strings.Split(string(env), "\x00"), launchVariable+r.Token) {
		return -1, "", false
	}
	var exe unix.Stat_t
	if unix.Stat(filepath.Join(proc, "exe"), &exe) != nil || uint64(exe.Dev) != r.Device || exe.Ino != r.Inode {
		return -1, "", false
	}
	end, err := processStart(pid)
	if err != nil || start != end {
		return -1, "", false
	}
	owned = true
	return fd, start, true
}

// live recovers an intent left by a crash between fork and PID persistence.
// It looks for the random per-launch token and exact immutable launch argv.
func (p *Supervisor) live(r launchRecord) (int, int, error) {
	if r.Boot != p.boot {
		return 0, -1, nil
	}
	if r.PID > 1 {
		fd, _, ok := openOwnedPID(p.options.Binary, r, r.PID)
		if ok {
			return r.PID, fd, nil
		}
		// If the recorded process start still matches but its identity
		// cannot be authenticated, do not assume the slot is vacant.
		if start, err := processStart(r.PID); err == nil && start == r.Start {
			return 0, -1, errProcess
		}
		return 0, -1, nil
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, -1, errProcess
	}
	var found int
	fd := -1
	for _, entry := range procs {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if handle, _, ok := openOwnedPID(p.options.Binary, r, pid); ok {
			if found != 0 {
				unix.Close(handle)
				unix.Close(fd)
				return 0, -1, errProcess
			}
			found, fd = pid, handle
		}
	}
	return found, fd, nil
}

func (p *Supervisor) Status(ctx context.Context) (platform.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, f, err := p.lock(ctx)
	if err != nil {
		return platform.Status{}, err
	}
	defer d.Close()
	defer f.Close()
	r, err := p.record(d)
	if err != nil {
		return platform.Status{}, err
	}
	pid, fd, err := p.live(r)
	if fd >= 0 {
		unix.Close(fd)
	}
	if err == nil && pid > 0 && r.PID == 0 {
		r.PID = pid
		r.Start, err = processStart(pid)
		if err == nil {
			err = writeRecord(d, r)
		}
	}
	return platform.Status{PID: pid, Running: pid > 0}, err
}

func (p *Supervisor) Start(ctx context.Context, executable string, args []string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if executable != p.options.Binary || !validArgs(p.options, args) {
		return 0, errProcess
	}
	d, f, err := p.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	defer f.Close()
	r, err := p.record(d)
	if err != nil {
		return 0, err
	}
	pid, fd, err := p.live(r)
	if fd >= 0 {
		unix.Close(fd)
	}
	if err != nil || pid != 0 {
		return 0, errProcess
	}
	binary, err := pinnedBinary(executable)
	if err != nil {
		return 0, err
	}
	defer binary.Close()
	config, err := checkpoint.OpenPrivateDir(p.options.ConfigDir)
	if err != nil {
		return 0, err
	}
	content, err := config.Read(filepath.Base(args[3]), 1<<20)
	config.Close()
	if err != nil || configName(core.Digest(content)) != filepath.Base(args[3]) {
		return 0, errProcess
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return 0, errProcess
	}
	var stat unix.Stat_t
	if unix.Fstat(int(binary.Fd()), &stat) != nil {
		return 0, errProcess
	}
	r = launchRecord{Boot: p.boot, Token: hex.EncodeToString(token[:]), Args: args, Device: uint64(stat.Dev), Inode: stat.Ino}
	if err := writeRecord(d, r); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// The configuration has already passed Adapter.Startup. Exec the exact
	// checked file descriptor with an absolute path and no shell.
	cmd := exec.Command("/proc/self/fd/3", args...)
	cmd.Args[0] = executable
	cmd.ExtraFiles = []*os.File{binary}
	cmd.Dir = p.options.WorkDir
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true}
	cmd.Env = []string{launchVariable + r.Token, "GOMAXPROCS=1"}
	if cmd.Start() != nil {
		return 0, errProcess
	}
	go cmd.Wait()
	r.PID = cmd.Process.Pid
	r.Start, err = processStart(r.PID)
	if err == nil {
		err = writeRecord(d, r)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = cmd.Process.Kill()
		return 0, errProcess
	}
	return r.PID, nil
}

func (p *Supervisor) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, f, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	defer f.Close()
	r, err := p.record(d)
	if err != nil {
		return err
	}
	_, fd, err := p.live(r)
	if err != nil || fd < 0 {
		return err
	}
	defer unix.Close(fd)
	for _, signal := range []unix.Signal{unix.SIGTERM, unix.SIGKILL} {
		if err := unix.PidfdSendSignal(fd, signal, nil, 0); errors.Is(err, unix.ESRCH) {
			return nil
		} else if err != nil {
			return errProcess
		}
		until := time.Now().Add(p.options.StopTimeout)
		for time.Now().Before(until) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, err := unix.Poll(fds, 25); err == nil && n > 0 {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	return errProcess
}
