//go:build !darwin && !linux

package modelcatalog

import "context"

func queryResources(_ context.Context, _ string) (uint64, uint64, []string) {
	return 0, 0, []string{
		"available memory detection is not supported on this operating system",
		"available disk detection is not supported on this operating system",
	}
}
