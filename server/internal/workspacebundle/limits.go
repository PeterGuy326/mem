package workspacebundle

import (
	"fmt"
	"math"
)

type Limits struct {
	MaxEntries           int
	MaxEntrySize         uint64
	MaxTotalSize         uint64
	MaxMetadataEntrySize uint64
	MaxTotalMetadataSize uint64
	MaxCompressionRatio  uint64
	MaxJSONDepth         int
	MaxPathDepth         int
	MaxIndexLineBytes    int
	MaxRecordsPerIndex   int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries:           250_000,
		MaxEntrySize:         64 << 30,  // 64 GiB
		MaxTotalSize:         1 << 40,   // 1 TiB
		MaxMetadataEntrySize: 64 << 20,  // 64 MiB
		MaxTotalMetadataSize: 512 << 20, // 512 MiB
		MaxCompressionRatio:  200,
		MaxJSONDepth:         64,
		MaxPathDepth:         8,
		MaxIndexLineBytes:    4 << 20, // 4 MiB
		MaxRecordsPerIndex:   1_000_000,
	}
}

func normalizeLimits(in Limits) (Limits, error) {
	if in == (Limits{}) {
		return DefaultLimits(), nil
	}
	if in.MaxEntries <= 0 ||
		in.MaxEntrySize == 0 ||
		in.MaxTotalSize == 0 ||
		in.MaxMetadataEntrySize == 0 ||
		in.MaxTotalMetadataSize == 0 ||
		in.MaxCompressionRatio == 0 ||
		in.MaxJSONDepth <= 0 ||
		in.MaxPathDepth <= 0 ||
		in.MaxIndexLineBytes <= 0 ||
		in.MaxRecordsPerIndex <= 0 {
		return Limits{}, fmt.Errorf("%w: every configured limit must be positive", ErrInvalidBundle)
	}
	if in.MaxMetadataEntrySize > in.MaxEntrySize {
		return Limits{}, fmt.Errorf(
			"%w: metadata entry limit exceeds general entry limit",
			ErrInvalidBundle,
		)
	}
	if in.MaxTotalMetadataSize > in.MaxTotalSize {
		return Limits{}, fmt.Errorf(
			"%w: total metadata limit exceeds total entry limit",
			ErrInvalidBundle,
		)
	}
	for label, value := range map[string]uint64{
		"max_entry_size":          in.MaxEntrySize,
		"max_total_size":          in.MaxTotalSize,
		"max_metadata_entry_size": in.MaxMetadataEntrySize,
		"max_total_metadata_size": in.MaxTotalMetadataSize,
	} {
		if value > math.MaxInt64 {
			return Limits{}, fmt.Errorf(
				"%w: %s exceeds the supported int64 range",
				ErrInvalidBundle,
				label,
			)
		}
	}
	return in, nil
}
