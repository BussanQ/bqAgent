package modelcatalog

import "testing"

func TestQualifiedNamePreservesProviderNamespace(t *testing.T) {
	for _, test := range []struct{ provider, id, name, want string }{
		{"gateway", "group/auto-deepseek", "Deepseek-V4.1-Flash", "group/Deepseek-V4.1-Flash"},
		{"gateway", "other/deepseek", "Deepseek-V4.1-Flash", "other/Deepseek-V4.1-Flash"},
		{"gateway", "workbuddy/ai/gpt-6-sol", "", "workbuddy/ai/gpt-6-sol"},
		{"gateway", "group/model", "group/Friendly", "group/Friendly"},
		{"configured", "model", "Friendly", "configured/Friendly"},
		{"openai-response", "model", "", "openai-response/model"},
		{"", "model", "", "provider/model"},
	} {
		if got := QualifiedName(test.provider, test.id, test.name); got != test.want {
			t.Errorf("QualifiedName(%q, %q, %q) = %q, want %q", test.provider, test.id, test.name, got, test.want)
		}
	}
}
