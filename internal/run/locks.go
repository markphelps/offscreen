package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Slots are advisory OS locks, shared by synchronous clients and future daemon
// workers. The optional project lock is held for the entire job, including prepare.
func acquire(ctx context.Context, dir, root string, exclusive bool, slots, threshold int) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var project *os.File
	if exclusive {
		hash := sha256.Sum256([]byte(root))
		var err error
		project, err = waitLock(ctx, filepath.Join(dir, "project-"+hex.EncodeToString(hash[:])+".lock"))
		if err != nil {
			return nil, err
		}
	}
	releaseProject := func() {
		if project != nil {
			_ = syscall.Flock(int(project.Fd()), syscall.LOCK_UN)
			_ = project.Close()
		}
	}
	if slots < 1 {
		slots = 1
	}
	if slots > 2 {
		slots = 2
	} // macOS guest licensing limit
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Serialize the memory decision with slot admission. In particular, a
		// high-memory waiter must not take vm-1 between a low-memory waiter's
		// vm-1 probe and its acquisition of vm-0.
		admission, err := tryLock(filepath.Join(dir, "vm-admission.lock"))
		if err != nil {
			releaseProject()
			return nil, err
		}
		if admission != nil {
			limit := slots
			if limit > 1 && threshold > 0 {
				available, err := availableMemory()
				if err != nil || available < uint64(threshold)*1024*1024*1024 {
					limit = 1 // Fail closed if the memory probe is unavailable.
				}
			}
			var probe *os.File
			if limit == 1 {
				probe, err = tryLock(filepath.Join(dir, "vm-1.lock"))
			}
			var slot *os.File
			if err == nil && (limit != 1 || probe != nil) {
				for i := range limit {
					slot, err = tryLock(filepath.Join(dir, fmt.Sprintf("vm-%d.lock", i)))
					if err != nil || slot != nil {
						break
					}
				}
			}
			if probe != nil {
				unlockFile(probe)
			}
			unlockFile(admission)
			if err != nil {
				releaseProject()
				return nil, err
			}
			if slot != nil {
				return func() { unlockFile(slot); releaseProject() }, nil
			}
		}
		select {
		case <-ctx.Done():
			releaseProject()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func unlockFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func waitLock(ctx context.Context, path string) (*os.File, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := tryLock(path)
		if err != nil {
			return nil, err
		}
		if file != nil {
			return file, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func tryLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, nil
		}
		return nil, err
	}
	return file, nil
}

var pageSize = regexp.MustCompile(`page size of (\d+) bytes`)

// vm_stat's inactive and speculative pages can be reclaimed by the kernel;
// counting only "free" would unnecessarily reduce concurrency on macOS.
// Purgeable pages overlap other categories and must not be counted twice.
func availableMemory() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, err
	}
	return parseVMStat(string(out))
}

func parseVMStat(output string) (uint64, error) {
	match := pageSize.FindStringSubmatch(output)
	if match == nil {
		return 0, fmt.Errorf("vm_stat: missing page size")
	}
	size, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, err
	}
	var pages uint64
	for line := range strings.SplitSeq(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.TrimSpace(parts[0]) {
		case "Pages free", "Pages inactive", "Pages speculative":
			count, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), ".")), 10, 64)
			if err != nil {
				return 0, err
			}
			pages += count
		}
	}
	return pages * size, nil
}
