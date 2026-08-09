package service

import "testing"

func TestChannelProbeToolChoiceMatchesThinkingModelCompatibility(t *testing.T) {
	for _, model := range []string{"deepseek-v4-pro", "vendor/deepseek-v4-flash", "models:deepseek_v4_flash_0731"} {
		if choice, send := channelProbeToolChoice(model); send || choice != nil {
			t.Fatalf("DeepSeek V4 tool choice for %q = %#v, send = %v", model, choice, send)
		}
	}
	if choice, send := channelProbeToolChoice("kimi-k2.6"); !send || choice != "auto" {
		t.Fatalf("Kimi K2.6 tool choice = %#v, send = %v", choice, send)
	}
	if choice, send := channelProbeToolChoice("gpt-4o"); !send || choice != "required" {
		t.Fatalf("generic tool choice = %#v, send = %v", choice, send)
	}
}

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
