package engram

import (
	"strings"
	"testing"
)

func TestParseMemoryAddress(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want MemoryAddress
	}{
		{"project", "engram:long/decision", MemoryAddress{Tier: TierLong, Key: "decision"}},
		{"global", "engram:/short/thread", MemoryAddress{Global: true, Tier: TierShort, Key: "thread"}},
		{"agent", "engram:/preference/@codex/style", MemoryAddress{Global: true, Tier: TierPreference, Agent: "codex", Key: "style"}},
		{"escaped key", "engram:long/a%2Fb%20c", MemoryAddress{Tier: TierLong, Key: "a/b c"}},
		{"tier alias canonicalized", "engram:long-term/decision", MemoryAddress{Tier: TierLong, Key: "decision"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ParseMemoryAddress(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("ParseMemoryAddress did not recognize engram scheme")
			}
			if got != tt.want {
				t.Errorf("ParseMemoryAddress(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseMemoryAddressRejectsReservedOrMalformedForms(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"engram://host/long/key", "authorities"},
		{"engram:long/key?view=tldr", "query parameters"},
		{"engram:long/key#body", "fragments"},
		{"engram:long", "tier/key"},
		{"engram:long/a/b", "tier/@agent/key"},
		{"engram:preference/@codex/style", "agent layers are global"},
		{"engram:/long/@codex/style", "only support invariant or preference"},
		{"engram:/preference/@bad.agent/style", "invalid agent"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, ok, err := ParseMemoryAddress(tt.raw)
			if !ok {
				t.Fatal("ParseMemoryAddress did not recognize engram scheme")
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseMemoryAddress(%q) error = %v, want %q", tt.raw, err, tt.want)
			}
		})
	}

	if _, ok, err := ParseMemoryAddress("ordinary-key"); err != nil || ok {
		t.Fatalf("bare key recognized as address: ok=%v err=%v", ok, err)
	}
}

func TestMemoryAddressRoundTrip(t *testing.T) {
	tests := []MemoryAddress{
		{Tier: TierLong, Key: "decision"},
		{Global: true, Tier: TierPreference, Agent: "codex", Key: "style"},
		{Tier: TierLong, Key: "a/b c?#%"},
		{Tier: TierCold, Key: "."},
		{Global: true, Tier: TierShort, Key: ".."},
	}
	for _, want := range tests {
		raw := want.String()
		got, ok, err := ParseMemoryAddress(raw)
		if err != nil || !ok {
			t.Fatalf("ParseMemoryAddress(%q) = %+v, %v, %v", raw, got, ok, err)
		}
		if got != want {
			t.Errorf("round trip through %q = %+v, want %+v", raw, got, want)
		}
	}
}

func TestMemoryAddressForAgentLayer(t *testing.T) {
	m := Memory{Tier: TierPreference, Key: "agent/codex/style"}
	if got, want := MemoryAddressFor(true, m).String(), "engram:/preference/@codex/style"; got != want {
		t.Errorf("global layer address = %q, want %q", got, want)
	}
	if got, want := MemoryAddressFor(false, m).String(), "engram:preference/agent%2Fcodex%2Fstyle"; got != want {
		t.Errorf("project key address = %q, want %q", got, want)
	}
	m.Tier = TierLong
	if got, want := MemoryAddressFor(true, m).String(), "engram:/long/agent%2Fcodex%2Fstyle"; got != want {
		t.Errorf("non-standing global key address = %q, want %q", got, want)
	}
}
