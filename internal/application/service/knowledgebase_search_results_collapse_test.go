package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// oneImageDocument builds the four sibling chunks WeKnora creates for a single
// image: the parent text chunk plus the OCR, caption and summary children. They
// all live in the same knowledge document, so search can match several of them
// at once and the model would otherwise render the same picture repeatedly.
func oneImageDocument(knowledgeID string) []*types.SearchResult {
	return []*types.SearchResult{
		{ID: "parent-text", KnowledgeID: knowledgeID, ChunkType: types.ChunkTypeText, Score: 0.40},
		{ID: "ocr", KnowledgeID: knowledgeID, ChunkType: types.ChunkTypeImageOCR, Score: 0.75},
		{ID: "caption", KnowledgeID: knowledgeID, ChunkType: types.ChunkTypeImageCaption, Score: 0.70},
		{ID: "summary", KnowledgeID: knowledgeID, ChunkType: types.ChunkTypeSummary, Score: 0.60},
	}
}

func TestCollapseImageSiblingsKeepsOneResultPerImage(t *testing.T) {
	results, dropped := collapseImageSiblings(context.Background(), oneImageDocument("kb-1"))

	if len(results) != 1 {
		t.Fatalf("expected 1 surviving result for one image, got %d", len(results))
	}
	if got := results[0].ChunkType; got != types.ChunkTypeImageCaption {
		t.Fatalf("expected the caption chunk to win, got chunk type %q", got)
	}
	if results[0].ID != "caption" {
		t.Fatalf("expected surviving chunk id %q, got %q", "caption", results[0].ID)
	}
	if len(dropped) != 3 {
		t.Fatalf("expected 3 dropped sibling ids, got %d (%v)", len(dropped), dropped)
	}
}

// The collapsed result must keep the best score of the group so it still
// outranks (or ties) results coming from other documents under the same query.
func TestCollapseImageSiblingsKeepsTopScore(t *testing.T) {
	results, _ := collapseImageSiblings(context.Background(), oneImageDocument("kb-1"))

	if len(results) != 1 {
		t.Fatalf("expected 1 surviving result, got %d", len(results))
	}
	if results[0].Score != 0.75 {
		t.Fatalf("expected collapsed result to keep the group top score 0.75, got %v", results[0].Score)
	}
}

// A document whose chunks are all plain text has no image siblings and must be
// left completely untouched, including its original ordering.
func TestCollapseImageSiblingsLeavesPlainTextUntouched(t *testing.T) {
	input := []*types.SearchResult{
		{ID: "t1", KnowledgeID: "kb-text", ChunkType: types.ChunkTypeText, Score: 0.9},
		{ID: "t2", KnowledgeID: "kb-text", ChunkType: types.ChunkTypeText, Score: 0.8},
		{ID: "t3", KnowledgeID: "kb-text", ChunkType: types.ChunkTypeParentText, Score: 0.7},
	}

	results, dropped := collapseImageSiblings(context.Background(), input)

	if len(dropped) != 0 {
		t.Fatalf("plain text document should drop nothing, got %v", dropped)
	}
	if len(results) != len(input) {
		t.Fatalf("expected %d results, got %d", len(input), len(results))
	}
	for i := range input {
		if results[i] != input[i] {
			t.Fatalf("result %d changed: expected %v, got %v", i, input[i], results[i])
		}
	}
}

// A single result cannot be collapsed with anything, even when it is an image
// chunk on its own.
func TestCollapseImageSiblingsIgnoresSingleResult(t *testing.T) {
	input := []*types.SearchResult{
		{ID: "ocr-only", KnowledgeID: "kb-1", ChunkType: types.ChunkTypeImageOCR, Score: 0.5},
	}

	results, dropped := collapseImageSiblings(context.Background(), input)

	if len(results) != 1 || results[0] != input[0] {
		t.Fatalf("single result should pass through unchanged, got %v", results)
	}
	if dropped != nil {
		t.Fatalf("expected no dropped ids, got %v", dropped)
	}
}

// Results from different knowledge documents must never be merged together,
// and a pure-text document alongside an image document keeps all of its chunks.
func TestCollapseImageSiblingsOnlyTouchesImageDocument(t *testing.T) {
	input := append(oneImageDocument("kb-image"), []*types.SearchResult{
		{ID: "other-t1", KnowledgeID: "kb-text", ChunkType: types.ChunkTypeText, Score: 0.65},
		{ID: "other-t2", KnowledgeID: "kb-text", ChunkType: types.ChunkTypeText, Score: 0.55},
	}...)

	results, dropped := collapseImageSiblings(context.Background(), input)

	if len(results) != 3 {
		t.Fatalf("expected 3 results (1 image + 2 text), got %d", len(results))
	}
	if len(dropped) != 3 {
		t.Fatalf("expected 3 dropped image siblings, got %d (%v)", len(dropped), dropped)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
	}
	for _, want := range []string{"caption", "other-t1", "other-t2"} {
		if !seen[want] {
			t.Fatalf("expected %q to survive, survivors: %v", want, seen)
		}
	}
}

// The winner selection must be stable regardless of the order the retrievers
// emit the siblings in — last writer used to win before the priority ranking.
func TestCollapseImageSiblingsWinnerIsOrderIndependent(t *testing.T) {
	base := oneImageDocument("kb-1")
	input := []*types.SearchResult{base[3], base[1], base[0], base[2]} // summary, ocr, text, caption

	results, _ := collapseImageSiblings(context.Background(), input)

	if len(results) != 1 {
		t.Fatalf("expected 1 surviving result, got %d", len(results))
	}
	if results[0].ChunkType != types.ChunkTypeImageCaption {
		t.Fatalf("expected caption to win regardless of input order, got %q", results[0].ChunkType)
	}
}

func TestImageSiblingPriorityRanksCaptionAboveOCRAboveSummaryAboveText(t *testing.T) {
	caption := imageSiblingPriority(types.ChunkTypeImageCaption)
	ocr := imageSiblingPriority(types.ChunkTypeImageOCR)
	summary := imageSiblingPriority(types.ChunkTypeSummary)
	text := imageSiblingPriority(types.ChunkTypeText)
	unknown := imageSiblingPriority("something_else")

	if !(caption > ocr && ocr > summary && summary > text && text > unknown) {
		t.Fatalf(
			"unexpected priority order: caption=%d ocr=%d summary=%d text=%d unknown=%d",
			caption, ocr, summary, text, unknown,
		)
	}
}

// Nil and knowledge-less entries are skipped when grouping, so a malformed
// result can never be dropped by the collapse.
func TestCollapseImageSiblingsSkipsNilAndUnknownKnowledge(t *testing.T) {
	input := []*types.SearchResult{
		nil,
		{ID: "no-knowledge", KnowledgeID: "", ChunkType: types.ChunkTypeImageOCR, Score: 0.9},
	}

	results, dropped := collapseImageSiblings(context.Background(), input)

	if len(results) != len(input) {
		t.Fatalf("expected both results to survive, got %d", len(results))
	}
	if dropped != nil {
		t.Fatalf("expected no dropped ids, got %v", dropped)
	}
}
