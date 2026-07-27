package main

import (
	"strings"
	"testing"
)

func TestFormatAskSlackMessage_Short(t *testing.T) {
	msg := formatAskSlackMessage("how much?", "costs $50/mo")

	// header + question + divider + 1 answer block = 4
	if len(msg.Blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d", len(msg.Blocks))
	}
	if msg.Blocks[3].Text.Text != "costs $50/mo" {
		t.Fatalf("unexpected answer text: %s", msg.Blocks[3].Text.Text)
	}
}

func TestFormatAskSlackMessage_LongSplitsAtNewline(t *testing.T) {
	// Build a 6000 char answer with newlines every 100 chars
	var sb strings.Builder
	line := strings.Repeat("x", 99) + "\n"
	for sb.Len() < 6000 {
		sb.WriteString(line)
	}
	answer := sb.String()

	msg := formatAskSlackMessage("question", answer)

	// header + question + divider + N answer blocks
	answerBlocks := msg.Blocks[3:]
	if len(answerBlocks) < 2 {
		t.Fatalf("expected multiple answer blocks for 6000 char answer, got %d", len(answerBlocks))
	}

	for i, b := range answerBlocks {
		if len(b.Text.Text) > 2900 {
			t.Errorf("block %d exceeds 2900 chars: %d", i, len(b.Text.Text))
		}
	}

	// Recombine and verify no content lost
	var recombined strings.Builder
	for _, b := range answerBlocks {
		recombined.WriteString(b.Text.Text)
	}
	if recombined.String() != answer {
		t.Error("recombined blocks do not match original answer")
	}
}

func TestFormatAskSlackMessage_NoNewlineHardCut(t *testing.T) {
	// 4000 chars with no newlines
	answer := strings.Repeat("a", 4000)

	msg := formatAskSlackMessage("q", answer)

	answerBlocks := msg.Blocks[3:]
	if len(answerBlocks) != 2 {
		t.Fatalf("expected 2 answer blocks, got %d", len(answerBlocks))
	}
	if len(answerBlocks[0].Text.Text) != 2900 {
		t.Errorf("first block should be 2900 chars, got %d", len(answerBlocks[0].Text.Text))
	}
	if len(answerBlocks[1].Text.Text) != 1100 {
		t.Errorf("second block should be 1100 chars, got %d", len(answerBlocks[1].Text.Text))
	}
}

func TestFormatAskSlackMessage_Empty(t *testing.T) {
	msg := formatAskSlackMessage("q", "")

	// header + question + divider = 3, no answer block
	if len(msg.Blocks) != 3 {
		t.Fatalf("expected 3 blocks for empty answer, got %d", len(msg.Blocks))
	}
}
