package engram

import (
	"context"
	"testing"
)

func TestGuidanceReadHistogramAggregatesByTopicAndVersion(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)

	for _, read := range []GuidanceRead{
		{Topic: "memory-tiers", Version: "v1.0.0", TS: 300},
		{Topic: "memory-tiers", Version: "v1.0.0", TS: 100},
		{Topic: "skills", Version: "v1.0.0", TS: 200},
		{Topic: "memory-tiers", Version: "v2.0.0", TS: 400},
	} {
		if err := RecordGuidanceRead(ctx, db, read); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := ListGuidanceReadStats(ctx, db, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d stats, want 2: %+v", len(stats), stats)
	}
	if got := stats[0]; got.Topic != "memory-tiers" || got.Loads != 2 ||
		got.FirstLoaded != 100 || got.LastLoaded != 300 {
		t.Errorf("memory-tiers stat = %+v, want two loads spanning 100..300", got)
	}
	if got := stats[1]; got.Topic != "skills" || got.Loads != 1 ||
		got.FirstLoaded != 200 || got.LastLoaded != 200 {
		t.Errorf("skills stat = %+v, want one load at 200", got)
	}
}

func TestGuidanceReadRequiresTopicAndVersion(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	for _, read := range []GuidanceRead{
		{Version: "v1.0.0"},
		{Topic: "skills"},
	} {
		if err := RecordGuidanceRead(ctx, db, read); err == nil {
			t.Errorf("RecordGuidanceRead(%+v) unexpectedly succeeded", read)
		}
	}
}

func TestGuidanceReadStatsTreatMissingTableAsEmpty(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if _, err := db.ExecContext(ctx, `DROP TABLE guidance_reads`); err != nil {
		t.Fatal(err)
	}
	stats, err := ListGuidanceReadStats(ctx, db, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("stats without table = %+v, want empty", stats)
	}
}
