package main

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Only immutable, published artifacts are eligible for migration. Do not run
// an old binary to identify it, or trust a filename, local manifest or marker.
// Source: rtw1248/speedquality GitHub releases v1.0.0 through v1.2.0.
//
//go:embed legacy-probe-checksums.txt
var legacyProbeChecksums string

func legacyProbeHashes() map[string]string {
	known := make(map[string]string)
	for _, line := range strings.Split(legacyProbeChecksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 {
			known[fields[0]+"/"+fields[1]] = fields[2]
		}
	}
	return known
}

func runCacheClean(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("cache-clean", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("path", "", "历史 SQ 缓存目录")
	parent := flags.Int("parent-pid", 0, "本次脚本进程")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *parent != os.Getppid() {
		return errors.New("无法确认本次脚本进程，已保留缓存")
	}
	// Cooperate with new clients' registration; old clients do not take this
	// lock, so also check live processes conservatively before each removal.
	lock, err := os.Open("/tmp")
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("另一个 SQ 任务正在登记或清理，下次运行重试")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	removed, retained, err := cleanLegacyCache(*path, legacyProbeHashes(), legacyTasksBusy)
	if removed > 0 {
		fmt.Fprintf(output, "[i] 已清理 %d 个历史 SQ 测速程序。\n", removed)
	}
	if retained {
		fmt.Fprintln(output, "[!] 历史缓存中有无法确认归属或不适合自动清理的内容，已保留。")
	}
	return err
}

// Open every directory component without following symlinks. Operations below
// stay relative to held directory descriptors, even if an ancestor is renamed.
func openCacheDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("缓存路径必须是绝对路径")
	}
	current, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			current.Close()
			return nil, errors.New("缓存路径含有上级目录")
		}
		next, openErr := openCacheChild(current, component, syscall.O_DIRECTORY)
		current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return current, nil
}

func openCacheChild(parent *os.File, name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func ownedCacheEntry(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0022 == 0
}

func cacheChildPath(parent *os.File, name string) string {
	return fmt.Sprintf("/proc/self/fd/%d/%s", parent.Fd(), name)
}

func sameCacheEntry(parent *os.File, name string, original os.FileInfo) bool {
	current, err := os.Lstat(cacheChildPath(parent, name))
	return err == nil && os.SameFile(original, current) && current.Mode() == original.Mode() &&
		current.Size() == original.Size() && current.ModTime() == original.ModTime()
}

func removeEmptyCacheDir(parent *os.File, name string, original os.FileInfo) {
	if sameCacheEntry(parent, name, original) {
		// Rmdir only: unknown contents, files and symlinks make this fail closed.
		_ = syscall.Rmdir(cacheChildPath(parent, name))
	}
}

func cleanLegacyCache(path string, known map[string]string, busy func() bool) (removed int, retained bool, err error) {
	clean := filepath.Clean(path)
	for _, forbidden := range []string{".", "/", "/tmp", "/var/tmp", "/home", "/root", os.Getenv("HOME"), os.Getenv("XDG_CACHE_HOME")} {
		if clean == forbidden {
			return 0, true, errors.New("缓存路径范围过大，已保留")
		}
	}
	// Validate the original spelling too; filepath.Clean must not conceal '..'.
	root, err := openCacheDirectory(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, true, errors.New("缓存路径无法安全打开（可能含软链接或权限不足）")
	}
	defer root.Close()
	rootInfo, err := root.Stat()
	if err != nil || !ownedCacheEntry(rootInfo) {
		return 0, true, errors.New("缓存目录所有权或权限不符合自动清理条件")
	}
	if busy() {
		return 0, false, errors.New("其他脚本或测速进程仍可能使用旧缓存，下次空闲时重试")
	}
	entries, err := root.ReadDir(-1)
	if err != nil {
		return 0, true, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if known[name+"/sqprobe-linux-amd64"] == "" && known[name+"/sqprobe-linux-arm64"] == "" {
			retained = true
			continue
		}
		dir, openErr := openCacheChild(root, name, syscall.O_DIRECTORY)
		if openErr != nil {
			retained = true
			continue
		}
		info, statErr := dir.Stat()
		if statErr != nil || !ownedCacheEntry(info) {
			dir.Close()
			retained = true
			continue
		}
		files, readErr := dir.ReadDir(-1)
		if readErr != nil {
			dir.Close()
			retained = true
			continue
		}
		for _, file := range files {
			expected := known[name+"/"+file.Name()]
			if expected == "" {
				retained = true
				continue
			}
			matched := matchingCacheFile(dir, file.Name(), expected)
			if matched == nil {
				retained = true
				continue
			}
			if busy() {
				dir.Close()
				return removed, retained, errors.New("检测到其他脚本或测速进程，剩余历史缓存下次清理")
			}
			if sameCacheEntry(dir, file.Name(), matched) && syscall.Unlinkat(int(dir.Fd()), file.Name()) == nil {
				removed++
			} else {
				retained = true
			}
		}
		// Refresh metadata after our own removals changed directory timestamps.
		info, statErr = dir.Stat()
		if statErr == nil {
			removeEmptyCacheDir(root, name, info)
		}
		dir.Close()
	}
	rootInfo, err = root.Stat()
	if err == nil {
		parent, openErr := openCacheDirectory(filepath.Dir(clean))
		if openErr == nil {
			removeEmptyCacheDir(parent, filepath.Base(clean), rootInfo)
			parent.Close()
		}
	}
	return removed, retained, nil
}

func matchingCacheFile(dir *os.File, name, expected string) os.FileInfo {
	file, err := openCacheChild(dir, name, 0)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedCacheEntry(info) || info.Size() > 16<<20 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, io.LimitReader(file, (16<<20)+1)); err != nil {
		return nil
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != expected {
		return nil
	}
	return info
}

func procParent(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	index := strings.LastIndex(string(data), ") ")
	if index < 0 {
		return 0
	}
	fields := strings.Fields(string(data)[index+2:])
	if len(fields) < 2 {
		return 0
	}
	parent, _ := strconv.Atoi(fields[1])
	return parent
}

// Legacy launchers can be waiting between provinces with no sqprobe process.
// Treat other noninteractive Bash processes as potentially using the cache.
// This intentionally prefers deferral to guessing from a process name alone.
func legacyTasksBusy() bool {
	ancestors := make(map[int]bool)
	for pid := os.Getpid(); pid > 0 && !ancestors[pid]; pid = procParent(pid) {
		ancestors[pid] = true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || ancestors[pid] {
			continue
		}
		path := "/proc/" + entry.Name()
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return true
			}
			continue
		}
		if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			continue
		}
		data, err := os.ReadFile(path + "/comm")
		if err != nil {
			if !os.IsNotExist(err) {
				return true
			}
			continue
		}
		name := strings.TrimSpace(string(data))
		if strings.HasPrefix(name, "sqprobe") {
			return true
		}
		if name != "bash" {
			continue
		}
		args, err := os.ReadFile(path + "/cmdline")
		if err != nil {
			if !os.IsNotExist(err) {
				return true
			}
			continue
		}
		fields := strings.Split(strings.TrimRight(string(args), "\x00"), "\x00")
		stdin, _ := os.Readlink(path + "/fd/0")
		interactive := strings.HasPrefix(stdin, "/dev/pts/") || strings.HasPrefix(stdin, "/dev/tty")
		for _, arg := range fields[1:] {
			if arg != "-l" && arg != "--login" && arg != "-i" {
				interactive = false
			}
		}
		if len(args) > 0 && !interactive {
			return true
		}
	}
	return false
}
