package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAggregateEvents(t *testing.T) {
	tests := []struct {
		name   string
		events []usageEvent
		want   []tokenRate
	}{
		{
			name: "groups by model and uses output tokens over full duration",
			events: []usageEvent{
				{Model: "gpt-a", OutputTokens: 100, InputTokens: 900, CacheReadTokens: 500, ReasoningTokens: 300, DurationMs: 2000},
				{Model: "gpt-a", OutputTokens: 50, DurationMs: 1000},
				{Model: "gpt-b", OutputTokens: 20, DurationMs: 400},
			},
			want: []tokenRate{
				{Model: "gpt-a", OutputTokens: 150, DurationMs: 3000, TokensPerSecond: 50, Requests: 2},
				{Model: "gpt-b", OutputTokens: 20, DurationMs: 400, TokensPerSecond: 50, Requests: 1},
			},
		},
		{
			name: "does not count input cache or reasoning tokens",
			events: []usageEvent{
				{
					Model:            "gpt-reasoning",
					OutputTokens:     7,
					InputTokens:      10000,
					CacheReadTokens:  20000,
					CacheWriteTokens: 30000,
					ReasoningTokens:  40000,
					DurationMs:       1000,
				},
			},
			want: []tokenRate{{Model: "gpt-reasoning", OutputTokens: 7, DurationMs: 1000, TokensPerSecond: 7, Requests: 1}},
		},
		{
			name:   "zero duration has zero throughput",
			events: []usageEvent{{Model: "gpt-zero", OutputTokens: 10}},
			want:   []tokenRate{{Model: "gpt-zero", OutputTokens: 10, Requests: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggregateEvents(tt.events)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d models, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("model %d: got %#v, want %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestQueryCutoff(t *testing.T) {
	now := time.Date(2026, 8, 13, 17, 47, 54, 883000000, time.FixedZone("CEST", 2*60*60))
	got := queryCutoff(now, 90*time.Second)
	want := time.Date(2026, 8, 13, 15, 46, 24, 883000000, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("got %s (%s), want %s (UTC)", got, got.Location(), want)
	}
}

func TestRenderSnapshotTable(t *testing.T) {
	var got bytes.Buffer
	err := renderSnapshot(&got, snapshot{
		Cutoff: "2026-08-13T15:46:24.883Z",
		Models: []tokenRate{{Model: "gpt-a", OutputTokens: 150, DurationMs: 3000, TokensPerSecond: 50, Requests: 2}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "Copilot output-token rate (since 2026-08-13T15:46:24.883Z)\nMODEL\tOUTPUT TOKENS\tDURATION\tTOKENS/SEC\tREQUESTS\ngpt-a\t150\t3s\t50.00\t2\n"
	if got.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestRenderSnapshotJSON(t *testing.T) {
	var got bytes.Buffer
	err := renderSnapshot(&got, snapshot{Cutoff: "2026-08-13T15:46:24.883Z", Models: []tokenRate{}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var decoded snapshot
	if err := json.Unmarshal(got.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), `"models":[]`) {
		t.Fatalf("JSON output did not contain an empty models array: %s", got.String())
	}
	if decoded.Cutoff != "2026-08-13T15:46:24.883Z" {
		t.Fatalf("got cutoff %q", decoded.Cutoff)
	}
}
