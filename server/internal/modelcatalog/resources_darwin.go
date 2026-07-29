//go:build darwin

package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func queryResources(ctx context.Context, path string) (uint64, uint64, []string) {
	warnings := []string{}
	memory, err := darwinAvailableMemory(ctx)
	if err != nil {
		warnings = append(warnings, "available memory could not be detected")
	}
	disk, err := unixAvailableDisk(path)
	if err != nil {
		warnings = append(warnings, "available disk space could not be detected")
	}
	return memory, disk, warnings
}

// darwinAvailableMemory executes the fixed system vm_stat binary. No user or
// model text is ever interpolated into this command.
func darwinAvailableMemory(parent context.Context) (uint64, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, err
	}
	return parseVMStat(output)
}

func parseVMStat(output []byte) (uint64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	pageSize := uint64(4096)
	var pages uint64
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
			if start := strings.Index(line, "page size of "); start >= 0 {
				raw := line[start+len("page size of "):]
				raw = strings.TrimSuffix(raw, " bytes)")
				if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
					pageSize = parsed
				}
			}
			continue
		}
		name, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "Pages free", "Pages inactive", "Pages speculative":
		default:
			continue
		}
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "."))
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse vm_stat %q: %w", name, err)
		}
		pages += value
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if pages == 0 {
		return 0, fmt.Errorf("vm_stat did not report available pages")
	}
	return pages * pageSize, nil
}

func unixAvailableDisk(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
