package engram

import (
	"context"
	"database/sql"
	"math/rand"
	"strings"
	"testing"
)

// --- pure functions: slot detection and render ---

func TestDetectSlotsDistinctInOrder(t *testing.T) {
	got := DetectSlots("Write the {artifact} with {principle}, a good {artifact}.")
	want := []string{"artifact", "principle"}
	if len(got) != len(want) {
		t.Fatalf("DetectSlots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DetectSlots = %v, want %v", got, want)
		}
	}
	if len(DetectSlots("no slots here")) != 0 {
		t.Errorf("expected no slots")
	}
}

func TestRenderSubstitutesEveryOccurrence(t *testing.T) {
	// A slot used twice is filled at both sites.
	out, err := RenderTemplate("{x} and {x} again", map[string]string{"x": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "yes and yes again" {
		t.Errorf("render = %q", out)
	}
}

func TestRenderMissingBindingErrorsNamingSlots(t *testing.T) {
	_, err := RenderTemplate("{a} {b} {c}", map[string]string{"b": "B"})
	if err == nil {
		t.Fatal("expected error for unbound slots")
	}
	// Both unbound slots named, in first-appearance order.
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "c") {
		t.Errorf("error should name unbound slots a and c: %v", err)
	}
}

func TestRenderIgnoresExtraBindings(t *testing.T) {
	out, err := RenderTemplate("just {a}", map[string]string{"a": "A", "unused": "Z"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "just A" {
		t.Errorf("render = %q", out)
	}
}

func TestRenderDoesNotRescanSubstitutedText(t *testing.T) {
	// A word that itself looks like a slot must not be re-substituted.
	out, err := RenderTemplate("{a}{b}", map[string]string{"a": "{b}", "b": "B"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "{b}B" {
		t.Errorf("render = %q, want %q (single pass, no rescan)", out, "{b}B")
	}
}

// --- CRUD round-trips ---

func TestTemplateCRUDRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := UpsertTemplate(ctx, db, Template{Key: "greet", Text: "hi {who}", Tldr: "a greeting"}); err != nil {
		t.Fatal(err)
	}
	got, err := GetTemplate(ctx, db, "greet")
	if err != nil || got == nil {
		t.Fatalf("GetTemplate: %v, %v", got, err)
	}
	if got.Text != "hi {who}" || got.Tldr != "a greeting" {
		t.Errorf("round-trip = %+v", got)
	}

	// Upsert overwrites in place, keyed on key.
	if err := UpsertTemplate(ctx, db, Template{Key: "greet", Text: "yo {who}"}); err != nil {
		t.Fatal(err)
	}
	list, err := ListTemplates(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Text != "yo {who}" {
		t.Fatalf("after overwrite: %+v", list)
	}

	if err := DeleteTemplate(ctx, db, "greet"); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetTemplate(ctx, db, "greet"); got != nil {
		t.Errorf("expected deleted, got %+v", got)
	}
	if err := DeleteTemplate(ctx, db, "greet"); err == nil {
		t.Error("deleting a missing template should error")
	}
}

func TestVocabCRUDRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	added, err := AddVocab(ctx, db, "artifact", "memo")
	if err != nil || !added {
		t.Fatalf("AddVocab: added=%v err=%v", added, err)
	}
	// Re-adding the same word is an idempotent no-op.
	added, err = AddVocab(ctx, db, "artifact", "memo")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("re-adding an existing word should report not-added")
	}
	if _, err := AddVocab(ctx, db, "artifact", "report"); err != nil {
		t.Fatal(err)
	}

	entries, err := ListVocab(ctx, db, "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListVocab = %+v, want 2", entries)
	}

	if err := DeleteVocab(ctx, db, "artifact", "report"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteVocab(ctx, db, "artifact", "report"); err == nil {
		t.Error("deleting a missing word should error")
	}
	entries, _ = ListVocab(ctx, db, "")
	if len(entries) != 1 || entries[0].Word != "memo" {
		t.Fatalf("after delete: %+v", entries)
	}
}

// --- enumerate ---

func setupEnumerate(t *testing.T) (db *sql.DB, ctx context.Context) {
	t.Helper()
	d := testDB(t)
	c := context.Background()
	if err := UpsertTemplate(c, d, Template{Key: "t", Text: "{a}-{b}"}); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"a1", "a2", "a3"} {
		if _, err := AddVocab(c, d, "a", w); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []string{"b1", "b2"} {
		if _, err := AddVocab(c, d, "b", w); err != nil {
			t.Fatal(err)
		}
	}
	return d, c
}

func TestEnumerateCrossProductCount(t *testing.T) {
	db, ctx := setupEnumerate(t)
	res, err := Enumerate(ctx, db, "t", EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 6 {
		t.Fatalf("Total = %d, want 6", res.Total)
	}
	if len(res.Rendered) != 6 || res.Omitted != 0 {
		t.Fatalf("full enumerate: %d cells, %d omitted", len(res.Rendered), res.Omitted)
	}
	// Every cell distinct and of the {a}-{b} shape.
	seen := map[string]bool{}
	for _, cell := range res.Rendered {
		if seen[cell] {
			t.Errorf("duplicate cell %q", cell)
		}
		seen[cell] = true
		if !strings.Contains(cell, "-") {
			t.Errorf("cell %q not rendered", cell)
		}
	}
}

func TestEnumerateLimitReportsOmission(t *testing.T) {
	db, ctx := setupEnumerate(t)
	res, err := Enumerate(ctx, db, "t", EnumerateOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rendered) != 4 {
		t.Fatalf("Rendered = %d, want 4", len(res.Rendered))
	}
	if res.Total != 6 || res.Omitted != 2 {
		t.Fatalf("Total=%d Omitted=%d, want 6/2", res.Total, res.Omitted)
	}
	if res.Sampled {
		t.Error("limit path should not be marked sampled")
	}
}

func TestEnumerateEmptySlotErrors(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := UpsertTemplate(ctx, db, Template{Key: "t", Text: "{a}-{b}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddVocab(ctx, db, "a", "a1"); err != nil {
		t.Fatal(err)
	}
	// slot b has no vocabulary.
	_, err := Enumerate(ctx, db, "t", EnumerateOptions{})
	if err == nil {
		t.Fatal("expected empty-slot error")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("error should name empty slot b: %v", err)
	}
}

func TestEnumerateSampleDistinctAndBounded(t *testing.T) {
	db, ctx := setupEnumerate(t) // total = 6
	r := rand.New(rand.NewSource(1))
	res, err := Enumerate(ctx, db, "t", EnumerateOptions{Sample: 4, Rand: r})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sampled {
		t.Error("expected Sampled = true")
	}
	if len(res.Rendered) != 4 {
		t.Fatalf("sample size = %d, want 4", len(res.Rendered))
	}
	if res.Total != 6 || res.Omitted != 2 {
		t.Fatalf("Total=%d Omitted=%d, want 6/2", res.Total, res.Omitted)
	}
	// Distinctness: no cell repeated in the sample.
	seen := map[string]bool{}
	for _, cell := range res.Rendered {
		if seen[cell] {
			t.Errorf("sample contains duplicate cell %q", cell)
		}
		seen[cell] = true
	}
}

func TestEnumerateSampleAtLeastTotalFallsBackToFull(t *testing.T) {
	db, ctx := setupEnumerate(t) // total = 6
	res, err := Enumerate(ctx, db, "t", EnumerateOptions{Sample: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sampled {
		t.Error("sample >= total should fall back to full enumeration, not a sample")
	}
	if len(res.Rendered) != 6 || res.Omitted != 0 {
		t.Fatalf("fallback: %d cells, %d omitted, want 6/0", len(res.Rendered), res.Omitted)
	}
}

// TestSampleDistinctUniformity checks Floyd's sampler draws distinct values and
// covers the space roughly uniformly. Over many trials each element of a small
// universe should be selected at close to the expected rate; a fixed seed keeps
// the check deterministic.
func TestSampleDistinctUniformity(t *testing.T) {
	const (
		n      = 6
		k      = 3
		trials = 60000
	)
	r := rand.New(rand.NewSource(42))
	counts := make([]int, n)
	for i := 0; i < trials; i++ {
		got := sampleDistinct(n, k, r)
		if len(got) != k {
			t.Fatalf("sampleDistinct returned %d values, want %d", len(got), k)
		}
		seen := map[int]bool{}
		for _, v := range got {
			if v < 0 || v >= n {
				t.Fatalf("value %d out of range [0,%d)", v, n)
			}
			if seen[v] {
				t.Fatalf("duplicate value %d in one draw", v)
			}
			seen[v] = true
			counts[v]++
		}
	}
	// Each element's expected selection rate is k/n. Allow a generous band.
	expected := float64(trials) * float64(k) / float64(n)
	for v, c := range counts {
		ratio := float64(c) / expected
		if ratio < 0.9 || ratio > 1.1 {
			t.Errorf("element %d selected %d times, expected ~%.0f (ratio %.3f)", v, c, expected, ratio)
		}
	}
}

// --- curation wiring ---

func TestTemplateAddEmitsCurationEvent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := UpsertTemplate(ctx, db, Template{Key: "greet", Text: "hi {who}", Tldr: "sum"},
		WithCurationSource(SourceInteractive), WithCurationScope("project")); err != nil {
		t.Fatal(err)
	}
	evs := curationEvents(t, db, CurationFilter{Action: CurationTemplateAdd})
	if len(evs) != 1 {
		t.Fatalf("got %d template-add events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Key != "greet" || ev.Content != "hi {who}" || ev.Tldr != "sum" {
		t.Errorf("event = %+v, want key/content/tldr snapshot", ev)
	}
	if ev.Source != SourceInteractive || ev.DBScope != "project" {
		t.Errorf("event source/scope = %q/%q", ev.Source, ev.DBScope)
	}
}

func TestVocabAddEmitsCurationEventOnlyOnGrowth(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := AddVocab(ctx, db, "artifact", "memo",
		WithCurationSource(SourceInteractive), WithCurationScope("project")); err != nil {
		t.Fatal(err)
	}
	// A duplicate add must not emit a second event.
	if _, err := AddVocab(ctx, db, "artifact", "memo"); err != nil {
		t.Fatal(err)
	}
	evs := curationEvents(t, db, CurationFilter{Action: CurationVocabAdd})
	if len(evs) != 1 {
		t.Fatalf("got %d vocab-add events, want 1 (idempotent re-add emits nothing)", len(evs))
	}
	if evs[0].Key != "artifact" || evs[0].Content != "memo" {
		t.Errorf("event = %+v, want slot=artifact word=memo", evs[0])
	}
}

func TestTemplateAndVocabDeleteEmitCurationEvents(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if err := UpsertTemplate(ctx, db, Template{Key: "greet", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteTemplate(ctx, db, "greet"); err != nil {
		t.Fatal(err)
	}
	if evs := curationEvents(t, db, CurationFilter{Action: CurationTemplateDelete}); len(evs) != 1 {
		t.Fatalf("template-delete events = %d, want 1", len(evs))
	}

	if _, err := AddVocab(ctx, db, "s", "w"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteVocab(ctx, db, "s", "w"); err != nil {
		t.Fatal(err)
	}
	if evs := curationEvents(t, db, CurationFilter{Action: CurationVocabDelete}); len(evs) != 1 {
		t.Fatalf("vocab-delete events = %d, want 1", len(evs))
	}
}
