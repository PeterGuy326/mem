//go:build linux

package modelcatalog

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func queryResources(_ context.Context, path string) (uint64, uint64, []string) {
	warnings := []string{}
	memory, err := linuxAvailableMemory("/proc/meminfo")
	if err != nil {
		warnings = append(warnings, "available memory could not be detected")
	}
	disk, err := unixAvailableDisk(path)
	if err != nil {
		warnings = append(warnings, "available disk space could not be detected")
	}
	return memory, disk, warnings
}

func linuxAvailableMemory(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "MemAvailable:" {
			continue
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", err)
		}
		return kib * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemAvailable is absent")
}

func unixAvailableDisk(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
