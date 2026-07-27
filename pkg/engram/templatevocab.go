package engram

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// This file backs the experimental template/vocabulary ("madlibs") mechanism: a
// fixed directive template carries named blanks like {artifact}, and a flat
// per-slot vocabulary supplies the candidate words those blanks draw from.
//
// The scope here is deliberately plumbing only -- manual CRUD, single-binding
// render, and cross-product enumeration. There is no learner, weighting, scoring,
// or featurizer: this is the substrate a later layer will learn to select and
// fill from. Substitution is literal; there is no morphology.

// slotPattern matches a named blank {name}. A name starts with a letter or
// underscore and continues with letters, digits, or underscores, so ordinary
// braces around other text are not mistaken for slots.
var slotPattern = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// Template is one fixed directive template with named blanks. Key is unique;
// adding an existing key overwrites (upsert). TS is unix epoch ms.
type Template struct {
	ID   int64
	TS   int64
	Key  string
	Text string
	Tldr string
}

// VocabEntry is one word in a slot's flat vocabulary. (Slot, Word) is unique.
type VocabEntry struct {
	ID   int64
	TS   int64
	Slot string
	Word string
}

// DetectSlots returns the distinct slot names in text, in order of first
// appearance. A slot repeated in the text appears once here.
func DetectSlots(text string) []string {
	matches := slotPattern.FindAllStringSubmatch(text, -1)
	var out []string
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// RenderTemplate substitutes each {slot} in text with its bound word, literally
// and in a single pass (so a substituted word is never rescanned for slots). A
// slot appearing more than once is replaced at every occurrence. Extra bindings
// with no matching slot are ignored. If any slot has no binding, no output is
// produced and the error names every unbound slot, in order of first appearance.
func RenderTemplate(text string, bindings map[string]string) (string, error) {
	var missing []string
	seenMissing := make(map[string]bool)
	out := slotPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := slotPattern.FindStringSubmatch(match)[1]
		if word, ok := bindings[name]; ok {
			return word
		}
		if !seenMissing[name] {
			seenMissing[name] = true
			missing = append(missing, name)
		}
		return match
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unbound slots: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// UpsertTemplate writes (creating or overwriting) a template keyed on Key, and
// captures a template-add curation event best-effort. An add always writes -- the
// upsert refreshes ts and may change text or tldr -- so it always records, the
// same always-on capture WriteMemory uses for memory writes.
func UpsertTemplate(ctx context.Context, db *sql.DB, t Template, opts ...CurationOption) error {
	if strings.TrimSpace(t.Key) == "" {
		return fmt.Errorf("template key must be non-empty")
	}
	if t.TS == 0 {
		t.TS = time.Now().UnixMilli()
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO templates (ts, key, text, tldr)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			ts = excluded.ts,
			text = excluded.text,
			tldr = excluded.tldr
	`, t.TS, t.Key, t.Text, t.Tldr); err != nil {
		return fmt.Errorf("upsert template: %w", err)
	}
	co := resolveCurationOptions(opts)
	if !co.suppress {
		captureCuration(ctx, db, CurationEvent{
			TS:        t.TS,
			SessionID: co.session,
			Action:    CurationTemplateAdd,
			Key:       t.Key,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   t.Text,
			Tldr:      t.Tldr,
		})
	}
	return nil
}

// GetTemplate returns the template with the given key, or nil if none exists.
func GetTemplate(ctx context.Context, db *sql.DB, key string) (*Template, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, ts, key, text, tldr FROM templates WHERE key = ?`, key)
	var t Template
	switch err := row.Scan(&t.ID, &t.TS, &t.Key, &t.Text, &t.Tldr); err {
	case nil:
		return &t, nil
	case sql.ErrNoRows:
		return nil, nil
	default:
		return nil, fmt.Errorf("get template: %w", err)
	}
}

// ListTemplates returns all templates ordered by key.
func ListTemplates(ctx context.Context, db *sql.DB) ([]Template, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, ts, key, text, tldr FROM templates ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.TS, &t.Key, &t.Text, &t.Tldr); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTemplate removes the template with the given key, erroring if none
// existed. The removed text and tldr are snapshotted into a template-delete
// curation event before deletion, best-effort, only on a successful delete.
func DeleteTemplate(ctx context.Context, db *sql.DB, key string, opts ...CurationOption) error {
	co := resolveCurationOptions(opts)
	var snapshot *Template
	if !co.suppress {
		snapshot, _ = GetTemplate(ctx, db, key)
	}
	result, err := db.ExecContext(ctx, `DELETE FROM templates WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("template not found: %s", key)
	}
	if !co.suppress && snapshot != nil {
		captureCuration(ctx, db, CurationEvent{
			SessionID: co.session,
			Action:    CurationTemplateDelete,
			Key:       key,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   snapshot.Text,
			Tldr:      snapshot.Tldr,
		})
	}
	return nil
}

// AddVocab adds word to slot's vocabulary. It reports whether a new row was
// inserted; a word already present in the slot is a no-op and reports false. A
// vocab-add curation event is captured best-effort only for real growth (an
// actual insertion), so re-adding an existing word does not pollute the log.
func AddVocab(ctx context.Context, db *sql.DB, slot, word string, opts ...CurationOption) (bool, error) {
	if strings.TrimSpace(slot) == "" {
		return false, fmt.Errorf("vocab slot must be non-empty")
	}
	if strings.TrimSpace(word) == "" {
		return false, fmt.Errorf("vocab word must be non-empty")
	}
	ts := time.Now().UnixMilli()
	result, err := db.ExecContext(ctx, `
		INSERT INTO vocab (ts, slot, word) VALUES (?, ?, ?)
		ON CONFLICT(slot, word) DO NOTHING
	`, ts, slot, word)
	if err != nil {
		return false, fmt.Errorf("add vocab: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("add vocab: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	co := resolveCurationOptions(opts)
	if !co.suppress {
		captureCuration(ctx, db, CurationEvent{
			TS:        ts,
			SessionID: co.session,
			Action:    CurationVocabAdd,
			Key:       slot,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   word,
		})
	}
	return true, nil
}

// ListVocab returns vocabulary entries ordered by slot then word. An empty slot
// returns every slot's entries; a non-empty slot returns only that slot's.
func ListVocab(ctx context.Context, db *sql.DB, slot string) ([]VocabEntry, error) {
	q := `SELECT id, ts, slot, word FROM vocab`
	var args []any
	if slot != "" {
		q += ` WHERE slot = ?`
		args = append(args, slot)
	}
	q += ` ORDER BY slot, word`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list vocab: %w", err)
	}
	defer rows.Close()
	var out []VocabEntry
	for rows.Next() {
		var v VocabEntry
		if err := rows.Scan(&v.ID, &v.TS, &v.Slot, &v.Word); err != nil {
			return nil, fmt.Errorf("scan vocab: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteVocab removes word from slot's vocabulary, erroring if it was not present.
// A vocab-delete curation event is captured best-effort only on a real removal.
func DeleteVocab(ctx context.Context, db *sql.DB, slot, word string, opts ...CurationOption) error {
	result, err := db.ExecContext(ctx,
		`DELETE FROM vocab WHERE slot = ? AND word = ?`, slot, word)
	if err != nil {
		return fmt.Errorf("delete vocab: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete vocab: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("vocab not found: %s/%s", slot, word)
	}
	co := resolveCurationOptions(opts)
	if !co.suppress {
		captureCuration(ctx, db, CurationEvent{
			SessionID: co.session,
			Action:    CurationVocabDelete,
			Key:       slot,
			DBScope:   co.scope,
			Source:    co.source,
			Content:   word,
		})
	}
	return nil
}

// slotVocab loads, for each distinct slot in the template text, that slot's
// vocabulary words (ordered). It errors clearly if any slot has no vocabulary,
// naming every empty slot, because an empty slot makes the cross-product empty.
func slotVocab(ctx context.Context, db *sql.DB, slots []string) (map[string][]string, error) {
	words := make(map[string][]string, len(slots))
	var empty []string
	for _, slot := range slots {
		entries, err := ListVocab(ctx, db, slot)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			empty = append(empty, slot)
			continue
		}
		ws := make([]string, len(entries))
		for i, e := range entries {
			ws[i] = e.Word
		}
		words[slot] = ws
	}
	if len(empty) > 0 {
		return nil, fmt.Errorf("no vocabulary for slot(s): %s", strings.Join(empty, ", "))
	}
	return words, nil
}

// EnumerateOptions controls Enumerate. Limit caps how many rendered cells are
// returned in deterministic order (0 means no cap). Sample, when > 0, instead
// draws a uniform random sample of that many distinct cells; if Sample is at
// least the total product, the full product is returned (the sample>=total
// fallback). Sample takes precedence over Limit. Rand is the source for sampling;
// nil uses a time-seeded default, which is what the CLI passes.
type EnumerateOptions struct {
	Limit  int
	Sample int
	Rand   *rand.Rand
}

// EnumerateResult reports a template expansion against current vocabulary. Total
// is the full cross-product size; Rendered holds the shown cells; Omitted is how
// many cells the full product has beyond those shown (Total - len(Rendered)),
// which the caller must report rather than truncate silently. Sampled is true
// when Rendered is a random sample rather than a deterministic prefix.
type EnumerateResult struct {
	Slots    []string
	Total    int
	Rendered []string
	Omitted  int
	Sampled  bool
}

// Enumerate expands key's template against the current vocabulary. Every distinct
// slot must have vocabulary or it errors. The full cross-product size is Total; a
// mixed-radix index in [0, Total) addresses each unique binding combination
// (cell), so both the deterministic prefix (Limit) and the uniform random sample
// (Sample) decode indices the same way. Sampling uses Floyd's algorithm so it
// draws distinct cells in O(k) without materializing the whole product.
func Enumerate(ctx context.Context, db *sql.DB, key string, opts EnumerateOptions) (*EnumerateResult, error) {
	tmpl, err := GetTemplate(ctx, db, key)
	if err != nil {
		return nil, err
	}
	if tmpl == nil {
		return nil, fmt.Errorf("template not found: %s", key)
	}
	slots := DetectSlots(tmpl.Text)

	// A template with no slots is a single fixed cell.
	if len(slots) == 0 {
		return &EnumerateResult{Slots: nil, Total: 1, Rendered: []string{tmpl.Text}}, nil
	}

	words, err := slotVocab(ctx, db, slots)
	if err != nil {
		return nil, err
	}

	counts := make([]int, len(slots))
	for i, s := range slots {
		counts[i] = len(words[s])
	}
	total, ok := product(counts)
	if !ok {
		return nil, fmt.Errorf("template %q cross-product is too large to enumerate", key)
	}

	render := func(index int) (string, error) {
		return RenderTemplate(tmpl.Text, indexToBinding(index, slots, words))
	}

	var indices []int
	sampled := false
	switch {
	case opts.Sample > 0 && opts.Sample < total:
		r := opts.Rand
		if r == nil {
			r = rand.New(rand.NewSource(time.Now().UnixNano()))
		}
		indices = sampleDistinct(total, opts.Sample, r)
		sampled = true
	default:
		// Deterministic prefix. Sample>=total falls through to here (the full
		// product), as does the plain Limit path and the no-option path.
		shown := total
		if opts.Limit > 0 && opts.Limit < shown {
			shown = opts.Limit
		}
		indices = make([]int, shown)
		for i := range indices {
			indices[i] = i
		}
	}

	rendered := make([]string, len(indices))
	for i, idx := range indices {
		s, err := render(idx)
		if err != nil {
			return nil, err
		}
		rendered[i] = s
	}

	return &EnumerateResult{
		Slots:    slots,
		Total:    total,
		Rendered: rendered,
		Omitted:  total - len(rendered),
		Sampled:  sampled,
	}, nil
}

// indexToBinding decodes a cross-product index into a slot->word binding using
// mixed-radix positional notation: the last slot is the least-significant digit.
func indexToBinding(index int, slots []string, words map[string][]string) map[string]string {
	b := make(map[string]string, len(slots))
	for i := len(slots) - 1; i >= 0; i-- {
		ws := words[slots[i]]
		b[slots[i]] = ws[index%len(ws)]
		index /= len(ws)
	}
	return b
}

// product multiplies counts, reporting false on int overflow so an unbounded
// enumeration is refused rather than wrapping to a nonsense size.
func product(counts []int) (int, bool) {
	const maxInt = int(^uint(0) >> 1)
	p := 1
	for _, c := range counts {
		if c == 0 {
			return 0, true
		}
		if p > maxInt/c {
			return 0, false
		}
		p *= c
	}
	return p, true
}

// sampleDistinct returns k distinct integers drawn uniformly from [0, n) using
// Floyd's algorithm: every k-subset is equally likely, in O(k) time and space
// with no dependence on n. It requires k <= n (Enumerate guarantees this by only
// sampling when Sample < total). Order within the result is not shuffled, which
// does not matter for a set-valued sample.
func sampleDistinct(n, k int, r *rand.Rand) []int {
	chosen := make(map[int]bool, k)
	out := make([]int, 0, k)
	for j := n - k; j < n; j++ {
		t := r.Intn(j + 1)
		if chosen[t] {
			t = j
		}
		chosen[t] = true
		out = append(out, t)
	}
	return out
}
