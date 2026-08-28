package prompt

import (
	"slices"
	"testing"
)

func TestTermsJapaneseGrammarBigrams2260(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   []string
	}{
		{
			name:   "test request",
			prompt: "すべてのテストを直してください",
			want:   []string{"テス", "スト"},
		},
		{
			name:   "review request",
			prompt: "この変更をレビューして、問題があれば教えて",
			want:   []string{"変更", "レビ", "ビュ", "ュー", "問題"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Terms(tc.prompt)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Terms(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}

	got := Terms("すべての変更を確認")
	for _, grammar := range []string{"すべ", "べて", "ての"} {
		if slices.Contains(got, grammar) {
			t.Errorf("Terms beginning with すべての = %v, contains grammar bigram %q", got, grammar)
		}
	}
	for _, content := range []string{"変更", "確認"} {
		if !slices.Contains(got, content) {
			t.Errorf("Terms beginning with すべての = %v, missing content bigram %q", got, content)
		}
	}
}

func TestTermsJapaneseKeepsContent2260(t *testing.T) {
	prompt := "サーバーのログを確認"
	want := []string{"サー", "ーバ", "バー", "ログ", "確認"}

	got := Terms(prompt)
	if !slices.Equal(got, want) {
		t.Fatalf("Terms(%q) = %v, want %v", prompt, got, want)
	}
}

func TestTermsChineseUnaffected2260(t *testing.T) {
	prompt := "装订计数是什么"
	want := []string{"装订", "订计", "计数", "数是"}

	got := Terms(prompt)
	if !slices.Equal(got, want) {
		t.Fatalf("Terms(%q) = %v, want %v", prompt, got, want)
	}
}
