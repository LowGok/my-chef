package config

import "testing"

func TestFeedbackRecipient(t *testing.T) {
	for input, want := range map[string]string{
		"": "", "operator@example.test": "operator@example.test",
		"Operator <operator@example.test>": "", "<operator@example.test>": "",
		"operator@example.test,other@example.test": "", "operator@example.test\n": "",
		" operator@example.test": "", "operator@example.test (Operator)": "", "invalid": "",
	} {
		t.Setenv("MYCHEF_FEEDBACK_TO", input)
		if got := LoadFeedbackRecipient(); got != want {
			t.Errorf("unexpected mailbox validation for fixture %q", input)
		}
	}
}
