package service

import "testing"

func TestMergeToolArgumentsSupportsDeltaAndCumulativeFragments(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		incoming string
		replace  bool
		want     string
	}{
		{name: "standard delta", current: `{"value":`, incoming: `"ok"}`, want: `{"value":"ok"}`},
		{name: "cumulative delta", current: `{"value":`, incoming: `{"value":"ok"}`, want: `{"value":"ok"}`},
		{name: "duplicate cumulative delta", current: `{"value":"ok"}`, incoming: `{"value":"ok"}`, want: `{"value":"ok"}`},
		{name: "placeholder", current: `{}`, incoming: `{"value":"ok"}`, want: `{"value":"ok"}`},
		{name: "short final payload keeps complete arguments", current: `{"value":"ok"}`, incoming: `{"value":`, replace: true, want: `{"value":"ok"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mergeToolArguments(test.current, test.incoming, test.replace); got != test.want {
				t.Fatalf("mergeToolArguments() = %q, want %q", got, test.want)
			}
		})
	}
}
