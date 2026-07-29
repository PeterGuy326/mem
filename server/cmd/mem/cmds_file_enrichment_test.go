package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCLISourceMetadataBuildsCaptureFacts(t *testing.T) {
	cmd := sourceMetadataTestCommand()
	if err := cmd.Flags().Set("lat", "31.2304"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("lon", "121.4737"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("location-accuracy", "6.5"); err != nil {
		t.Fatal(err)
	}

	metadata, err := cliSourceMetadata(
		cmd,
		"2026-07-29T08:00:00+08:00",
		31.2304,
		121.4737,
		6.5,
		"Shanghai",
		"mobile",
		"camera sync",
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.CapturedAt != "2026-07-29T08:00:00+08:00" ||
		metadata.SourceKind != "mobile" ||
		metadata.Location == nil ||
		metadata.Location.Lat != 31.2304 ||
		metadata.Location.Lon != 121.4737 ||
		metadata.Location.AccuracyM == nil ||
		*metadata.Location.AccuracyM != 6.5 {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestCLISourceMetadataRequiresCoordinatePair(t *testing.T) {
	cmd := sourceMetadataTestCommand()
	if err := cmd.Flags().Set("lat", "31.2304"); err != nil {
		t.Fatal(err)
	}
	_, err := cliSourceMetadata(cmd, "", 31.2304, 0, 0, "", "cli", "")
	if err == nil {
		t.Fatal("expected coordinate-pair validation error")
	}
}

func sourceMetadataTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Float64("lat", 0, "")
	cmd.Flags().Float64("lon", 0, "")
	cmd.Flags().Float64("location-accuracy", 0, "")
	return cmd
}
