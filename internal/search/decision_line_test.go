package search

import (
	"strings"
	"testing"
	"time"

	"github.com/vshulcz/deja-vu/internal/model"
)

// A long session mentions its subject in passing before it decides anything
// about it. The block has to quote the line that concluded, not the first line
// that happened to say the word.
//
// Five ways of choosing by where and how often a word fell were measured on a
// real store and none moved the number: rarity within the session, frequency
// within it, earliest against latest, and how many times the line repeats the
// word. All of them read "I'll look at the shard later" and "we moved the shard
// to the region" as the same line.
// The same in Russian. The markers were an English list, so every line of a
// Russian session read as a passing mention — and half the sessions on a real
// store are Russian.
func TestBlockPrefersTheRussianLineThatConcluded(t *testing.T) {
	at := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	msgs := []model.Message{
		{Role: "assistant", Text: "потом посмотрю про шардирование, пока не трогаю", Time: at},
		{Role: "assistant", Text: "снова про шардирование, руки не дошли", Time: at.Add(time.Minute)},
		{Role: "assistant", Text: "в итоге шардирование перенесли на регион", Time: at.Add(2 * time.Minute)},
	}
	s := model.Session{ID: "one", Harness: "claude", Project: "app", Started: at, Updated: at, Messages: msgs}
	block := AutoRecallDigestFor([]model.Session{s}, 2000, []string{"шардирование"})
	if !strings.Contains(block, "перенесли на регион") {
		t.Fatalf("the decision was dropped in favour of two passing mentions:\n%s", block)
	}
}

func TestBlockPrefersTheLineThatConcluded(t *testing.T) {
	at := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	msgs := []model.Message{
		{Role: "assistant", Text: "will look at the quicksilver thing later", Time: at},
		{Role: "assistant", Text: "quicksilver again, still open", Time: at.Add(time.Minute)},
		{Role: "assistant", Text: "the fix: quicksilver retries are capped at four", Time: at.Add(2 * time.Minute)},
	}
	s := model.Session{ID: "one", Harness: "claude", Project: "app", Started: at, Updated: at, Messages: msgs}
	block := AutoRecallDigestFor([]model.Session{s}, 2000, []string{"quicksilver"})
	// The block holds two lines and there are three to choose from. Which of
	// them leads is decided elsewhere — they are printed in the order they were
	// said, so that two conclusions read as a sequence. What this asserts is
	// that the line which concluded is one of the two, where before it was the
	// one dropped: all three matched the query once, and the tie went to
	// whichever came first.
	if !strings.Contains(block, "capped at four") {
		t.Fatalf("the decision was dropped in favour of two passing mentions:\n%s", block)
	}
}
