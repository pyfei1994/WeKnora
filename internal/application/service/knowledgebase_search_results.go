package service

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/searchutil"
	"github.com/Tencent/WeKnora/internal/types"
)

// processSearchResults handles the processing of search results, optimizing database queries.
func (s *knowledgeBaseService) processSearchResults(ctx context.Context,
	chunks []*types.IndexWithScore,
	skipEnrichment bool,
) ([]*types.SearchResult, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	tenantID := types.MustTenantIDFromContext(ctx)

	// Collect all knowledge and chunk IDs, track scores and match info
	index := s.buildChunkIndex(chunks)

	// Batch fetch knowledge data (include shared KB so cross-tenant retrieval works)
	logger.Infof(ctx, "Fetching knowledge data for %d IDs", len(index.knowledgeIDs))
	knowledgeMap, err := s.fetchKnowledgeDataWithShared(ctx, tenantID, index.knowledgeIDs)
	if err != nil {
		return nil, err
	}

	// Batch fetch chunks (include shared KB chunks)
	logger.Infof(ctx, "Fetching chunk data for %d IDs", len(index.chunkIDs))
	allChunks, err := s.listChunksByIDWithShared(ctx, tenantID, index.chunkIDs)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"tenant_id": tenantID,
			"chunk_ids": index.chunkIDs,
		})
		return nil, err
	}
	logger.Infof(ctx, "Chunk data fetched successfully, count: %d", len(allChunks))

	// Build chunk map and collect enrichment IDs (parent, related, nearby)
	chunkMap := make(map[string]*types.Chunk, len(allChunks))
	for _, chunk := range allChunks {
		chunkMap[chunk.ID] = chunk
	}

	if !skipEnrichment {
		additionalChunkIDs := s.collectEnrichmentChunkIDs(ctx, allChunks, index)
		if len(additionalChunkIDs) > 0 {
			logger.Infof(ctx, "Fetching %d additional chunks", len(additionalChunkIDs))
			additionalChunks, err := s.listChunksByIDWithShared(ctx, tenantID, additionalChunkIDs)
			if err != nil {
				logger.Warnf(ctx, "Failed to fetch some additional chunks: %v", err)
			} else {
				for _, chunk := range additionalChunks {
					chunkMap[chunk.ID] = chunk
				}
				// Second round: only needed when image chunks are among primary
				// results (image → text resolved above, now text → parent_text).
				// For normal text-only results this is a no-op.
				if s.hasImageChunks(allChunks) {
					parentIDs := s.collectParentChunkIDs(additionalChunks, index)
					if len(parentIDs) > 0 {
						logger.Infof(ctx, "Fetching %d second-level parent chunks", len(parentIDs))
						parentChunks, err := s.listChunksByIDWithShared(ctx, tenantID, parentIDs)
						if err != nil {
							logger.Warnf(ctx, "Failed to fetch second-level parent chunks: %v", err)
						} else {
							for _, chunk := range parentChunks {
								chunkMap[chunk.ID] = chunk
							}
						}
					}
				}
			}
		}
	}

	// Build final search results
	searchResults := s.assembleSearchResults(ctx, chunks, chunkMap, knowledgeMap, index, skipEnrichment)

	searchutil.EnrichSearchResultsImageInfo(ctx, s.chunkRepo, tenantID, searchResults)

	logger.Infof(ctx, "Search results processed, total: %d", len(searchResults))
	return searchResults, nil
}

// chunkIndex holds pre-computed lookup structures for processing search results.
type chunkIndex struct {
	knowledgeIDs    []string
	chunkIDs        []string
	scores          map[string]float64
	matchTypes      map[string]types.MatchType
	matchedContents map[string]string
	processedIDs    map[string]bool // tracks all IDs (chunk + enrichment) to avoid duplicates
}

// buildChunkIndex collects knowledge/chunk IDs and builds score/matchType maps
// from the raw retrieval results.
func (s *knowledgeBaseService) buildChunkIndex(chunks []*types.IndexWithScore) *chunkIndex {
	idx := &chunkIndex{
		scores:          make(map[string]float64, len(chunks)),
		matchTypes:      make(map[string]types.MatchType, len(chunks)),
		matchedContents: make(map[string]string, len(chunks)),
		processedIDs:    make(map[string]bool, len(chunks)*2),
	}

	processedKnowledgeIDs := make(map[string]bool)
	for _, chunk := range chunks {
		if !processedKnowledgeIDs[chunk.KnowledgeID] {
			idx.knowledgeIDs = append(idx.knowledgeIDs, chunk.KnowledgeID)
			processedKnowledgeIDs[chunk.KnowledgeID] = true
		}
		idx.chunkIDs = append(idx.chunkIDs, chunk.ChunkID)
		idx.scores[chunk.ChunkID] = chunk.Score
		idx.matchTypes[chunk.ChunkID] = chunk.MatchType
		idx.matchedContents[chunk.ChunkID] = chunk.Content
	}
	return idx
}

// collectEnrichmentChunkIDs gathers IDs for parent, related, and nearby chunks
// that should be fetched to enrich the search results.
func (s *knowledgeBaseService) collectEnrichmentChunkIDs(
	ctx context.Context,
	allChunks []*types.Chunk,
	idx *chunkIndex,
) []string {
	// Mark all primary chunks as processed
	for _, chunk := range allChunks {
		idx.processedIDs[chunk.ID] = true
	}

	var additionalIDs []string

	for _, chunk := range allChunks {
		// Collect parent chunks
		if chunk.ParentChunkID != "" && !idx.processedIDs[chunk.ParentChunkID] {
			additionalIDs = append(additionalIDs, chunk.ParentChunkID)
			idx.processedIDs[chunk.ParentChunkID] = true
			idx.scores[chunk.ParentChunkID] = idx.scores[chunk.ID]
			idx.matchTypes[chunk.ParentChunkID] = types.MatchTypeParentChunk
		}

		// Collect related chunks
		relationChunkIDs := s.collectRelatedChunkIDs(chunk, idx.processedIDs)
		for _, chunkID := range relationChunkIDs {
			additionalIDs = append(additionalIDs, chunkID)
			idx.matchTypes[chunkID] = types.MatchTypeRelationChunk
		}

		// Add nearby chunks (prev and next) for text chunks
		if slices.Contains([]string{types.ChunkTypeText}, chunk.ChunkType) {
			if chunk.NextChunkID != "" && !idx.processedIDs[chunk.NextChunkID] {
				additionalIDs = append(additionalIDs, chunk.NextChunkID)
				idx.processedIDs[chunk.NextChunkID] = true
				idx.matchTypes[chunk.NextChunkID] = types.MatchTypeNearByChunk
			}
			if chunk.PreChunkID != "" && !idx.processedIDs[chunk.PreChunkID] {
				additionalIDs = append(additionalIDs, chunk.PreChunkID)
				idx.processedIDs[chunk.PreChunkID] = true
				idx.matchTypes[chunk.PreChunkID] = types.MatchTypeNearByChunk
			}
		}
	}

	return additionalIDs
}

// collectParentChunkIDs returns unprocessed parent IDs from the given chunks.
// Unlike collectEnrichmentChunkIDs, this only resolves parent links without
// expanding nearby or related chunks, making it suitable for second-round
// parent chain resolution (e.g., text → parent_text after image → text).
func (s *knowledgeBaseService) collectParentChunkIDs(
	chunks []*types.Chunk,
	idx *chunkIndex,
) []string {
	var ids []string
	for _, chunk := range chunks {
		if chunk.ParentChunkID != "" && !idx.processedIDs[chunk.ParentChunkID] {
			ids = append(ids, chunk.ParentChunkID)
			idx.processedIDs[chunk.ParentChunkID] = true
			idx.scores[chunk.ParentChunkID] = idx.scores[chunk.ID]
			idx.matchTypes[chunk.ParentChunkID] = types.MatchTypeParentChunk
		}
	}
	return ids
}

// hasImageChunks returns true if any chunk is an image_ocr or image_caption type.
func (s *knowledgeBaseService) hasImageChunks(chunks []*types.Chunk) bool {
	for _, c := range chunks {
		if c.ChunkType == types.ChunkTypeImageOCR || c.ChunkType == types.ChunkTypeImageCaption {
			return true
		}
	}
	return false
}

// assembleSearchResults builds the final []*types.SearchResult from chunk data and knowledge data.
// Primary results (from input chunks) are added first in order, then enrichment results.
func (s *knowledgeBaseService) assembleSearchResults(
	ctx context.Context,
	inputChunks []*types.IndexWithScore,
	chunkMap map[string]*types.Chunk,
	knowledgeMap map[string]*types.Knowledge,
	idx *chunkIndex,
	skipEnrichment bool,
) []*types.SearchResult {
	var searchResults []*types.SearchResult
	addedChunkIDs := make(map[string]bool)
	const maxInvalidChunkLog = 8
	invalidChunkCnt := 0
	invalidChunkSamples := make([]string, 0, maxInvalidChunkLog)

	// First pass: Add results in the original order from input chunks
	for _, inputChunk := range inputChunks {
		chunk, exists := chunkMap[inputChunk.ChunkID]
		if !exists {
			logger.Debugf(ctx, "Chunk not found in chunkMap: %s", inputChunk.ChunkID)
			continue
		}
		if !s.isSearchableChunk(chunk) {
			invalidChunkCnt++
			if len(invalidChunkSamples) < maxInvalidChunkLog {
				invalidChunkSamples = append(invalidChunkSamples, chunk.ID+":"+chunk.ChunkType)
			}
			continue
		}
		if addedChunkIDs[chunk.ID] {
			continue
		}

		score := idx.scores[chunk.ID]
		if knowledge, ok := knowledgeMap[chunk.KnowledgeID]; ok {
			matchType := idx.matchTypes[chunk.ID]
			matchedContent := idx.matchedContents[chunk.ID]
			searchResults = append(searchResults, s.buildSearchResult(chunk, knowledge, score, matchType, matchedContent))
			addedChunkIDs[chunk.ID] = true
		} else {
			logger.Warnf(ctx, "Knowledge not found for chunk: %s, knowledge_id: %s", chunk.ID, chunk.KnowledgeID)
		}
	}
	if invalidChunkCnt > 0 {
		logger.Debugf(ctx,
			"Skip non-searchable chunks in search results: total=%d sampled=%d samples=%v",
			invalidChunkCnt, len(invalidChunkSamples), invalidChunkSamples,
		)
	}

	// Collapse same-image sibling chunks before enrichment.
	//
	// One uploaded image expands into several chunks sharing a single parent:
	// text (the "![](...)" markdown), image_ocr, image_caption and summary.
	// Each carries its own chunk ID, so chunk-ID dedup cannot see that they
	// describe the same picture, and a query matching the image returns all
	// of them. Feeding every sibling to the model makes it cite the same image
	// once per chunk — the "same image shows up four times" symptom.
	//
	// Only image documents collapse: a plain-text document's chunks carry no
	// image_* sibling, so the common path (and its chunk ordering) is
	// untouched.
	var collapsedAway []string
	searchResults, collapsedAway = collapseImageSiblings(ctx, searchResults)
	// Mark both the survivors (already recorded above, but re-marking is
	// harmless) and the collapsed siblings as handled. The siblings are still
	// present in chunkMap, so without this the enrichment pass below treats
	// them as unclaimed candidates and adds them straight back.
	for _, r := range searchResults {
		addedChunkIDs[r.ID] = true
	}
	for _, id := range collapsedAway {
		addedChunkIDs[id] = true
	}

	// Second pass: Add enrichment chunks (parent, nearby, relation)
	if !skipEnrichment {
		for chunkID, chunk := range chunkMap {
			if addedChunkIDs[chunkID] || !s.isSearchableChunk(chunk) {
				continue
			}

			score, hasScore := idx.scores[chunkID]
			if !hasScore || score <= 0 {
				score = 0.0
			}

			if knowledge, ok := knowledgeMap[chunk.KnowledgeID]; ok {
				matchType := types.MatchTypeParentChunk
				if specificType, exists := idx.matchTypes[chunkID]; exists {
					matchType = specificType
				} else {
					logger.Warnf(ctx, "Unkonwn match type for chunk: %s", chunkID)
					continue
				}
				matchedContent := idx.matchedContents[chunkID]
				searchResults = append(searchResults, s.buildSearchResult(chunk, knowledge, score, matchType, matchedContent))
			}
		}
	}

	return searchResults
}

// collapseImageSiblings merges the sibling chunks of one image document into a
// single search result.
//
// An uploaded image yields several chunks under one parent: the text chunk
// holding the "![](...)" markdown, plus image_ocr, image_caption and summary.
// They all match the same query but each has a distinct chunk ID, so
// chunk-level dedup keeps every one of them and the model ends up citing one
// picture N times.
//
// Documents whose chunks are all standalone text pass through untouched,
// preserving the existing ordering and count for plain-text knowledge. When a
// document does hold image siblings, the winner is chosen by how much the
// model can actually use: image_caption (a description plus the image URL)
// first, then image_ocr, then summary, then text. The highest score among the
// collapsed group is kept so ranking against other documents is unchanged.
//
// "Is this an image document" is deliberately NOT derived from which chunk
// types the query happened to match. A query can surface only summary + text
// for a picture and never touch image_caption, and gating on the matched
// types would leave that case uncollapsed — exactly the duplicate we are
// fixing. The reliable signal is structural: image children carry a non-empty
// ParentChunkID (they hang under the picture's text chunk) and/or ImageInfo,
// so any group containing one of those qualifies.
//
// Returns the collapsed results plus the IDs that were dropped, so the caller
// can mark them handled and keep the enrichment pass from re-adding them.
func collapseImageSiblings(ctx context.Context, results []*types.SearchResult) ([]*types.SearchResult, []string) {
	if len(results) < 2 {
		return results, nil
	}

	// Group by knowledge ID. A group qualifies as an image document when at
	// least one member is structurally an image chunk (has a parent or carries
	// image metadata) or matched as image_ocr / image_caption.
	type group struct {
		indices   []int
		topScore  float64
		hasImage  bool
		winnerIdx int
	}
	groupsByKnowledge := make(map[string]*group)
	for i, r := range results {
		if r == nil || r.KnowledgeID == "" {
			continue
		}
		g, ok := groupsByKnowledge[r.KnowledgeID]
		if !ok {
			g = &group{winnerIdx: i}
			groupsByKnowledge[r.KnowledgeID] = g
		}
		g.indices = append(g.indices, i)
		if r.Score > g.topScore {
			g.topScore = r.Score
		}
		if isImageSiblingResult(r) {
			g.hasImage = true
		}
	}

	// Pick the winner per image document.
	dropIndices := make(map[int]bool)
	var droppedIDs []string
	for _, g := range groupsByKnowledge {
		if !g.hasImage || len(g.indices) < 2 {
			continue
		}
		bestRank := -1
		for _, i := range g.indices {
			rank := imageSiblingPriority(results[i].ChunkType)
			if rank > bestRank {
				bestRank = rank
				g.winnerIdx = i
			}
		}
		winner := results[g.winnerIdx]
		// Carry the group's best score so the collapsed result keeps its
		// position relative to results from other documents.
		winner.Score = g.topScore
		for _, i := range g.indices {
			if i != g.winnerIdx {
				dropIndices[i] = true
				droppedIDs = append(droppedIDs, results[i].ID)
			}
		}
	}

	if len(dropIndices) == 0 {
		return results, nil
	}

	collapsed := make([]*types.SearchResult, 0, len(results)-len(dropIndices))
	for i, r := range results {
		if dropIndices[i] {
			continue
		}
		collapsed = append(collapsed, r)
	}
	logger.Infof(ctx, "Collapsed %d image sibling chunk(s) into their parent result", len(dropIndices))
	return collapsed, droppedIDs
}

// isImageSiblingResult reports whether a search result is structurally part of
// an image document's chunk family.
//
// ParentChunkID is the primary signal: image_ocr / image_caption / summary
// children always point at the picture's text chunk, while a plain-text
// document's chunks are independent rows with no parent. ImageInfo is the
// secondary signal for chunks that embed the picture handle directly, and the
// chunk type check covers the case where neither field survived hydration.
func isImageSiblingResult(r *types.SearchResult) bool {
	if r == nil {
		return false
	}
	if r.ParentChunkID != "" || r.ImageInfo != "" {
		return true
	}
	return r.ChunkType == types.ChunkTypeImageOCR || r.ChunkType == types.ChunkTypeImageCaption
}

// imageSiblingPriority ranks chunk types of one image document by how much
// signal they carry for the model. Higher wins.
func imageSiblingPriority(chunkType string) int {
	switch chunkType {
	case types.ChunkTypeImageCaption:
		return 4
	case types.ChunkTypeImageOCR:
		return 3
	case types.ChunkTypeSummary:
		return 2
	case types.ChunkTypeText:
		return 1
	default:
		return 0
	}
}

// collectRelatedChunkIDs extracts related chunk IDs from a chunk.
func (s *knowledgeBaseService) collectRelatedChunkIDs(chunk *types.Chunk, processedIDs map[string]bool) []string {
	var relatedIDs []string
	if len(chunk.RelationChunks) > 0 {
		var relations []string
		if err := json.Unmarshal(chunk.RelationChunks, &relations); err == nil {
			for _, id := range relations {
				if !processedIDs[id] {
					relatedIDs = append(relatedIDs, id)
					processedIDs[id] = true
				}
			}
		}
	}
	return relatedIDs
}

// buildSearchResult creates a search result from chunk and knowledge.
func (s *knowledgeBaseService) buildSearchResult(chunk *types.Chunk,
	knowledge *types.Knowledge,
	score float64,
	matchType types.MatchType,
	matchedContent string,
) *types.SearchResult {
	return &types.SearchResult{
		ID:                      chunk.ID,
		Content:                 chunk.Content,
		ContentRevision:         chunk.ContentRevision,
		KnowledgeID:             chunk.KnowledgeID,
		ChunkIndex:              chunk.ChunkIndex,
		KnowledgeTitle:          knowledge.Title,
		StartAt:                 chunk.StartAt,
		EndAt:                   chunk.EndAt,
		Seq:                     chunk.ChunkIndex,
		Score:                   score,
		MatchType:               matchType,
		Metadata:                knowledge.GetMetadata(),
		ChunkType:               string(chunk.ChunkType),
		ParentChunkID:           chunk.ParentChunkID,
		ImageInfo:               chunk.ImageInfo,
		KnowledgeFilename:       knowledge.FileName,
		KnowledgeSource:         knowledge.Source,
		KnowledgeChannel:        knowledge.Channel,
		KnowledgeDescription:    knowledge.Description,
		KnowledgeCustomMetadata: knowledge.CustomMetadataText(),
		ChunkMetadata:           chunk.Metadata,
		MatchedContent:          matchedContent,
		KnowledgeBaseID:         knowledge.KnowledgeBaseID,
	}
}

// isSearchableChunk checks if a chunk type should be included in search results.
func (s *knowledgeBaseService) isSearchableChunk(chunk *types.Chunk) bool {
	if chunk == nil || !chunk.IsEnabled {
		return false
	}
	// An edit is persisted before its retrieval artifacts are synchronized.
	// Do not hydrate stale vector hits while that synchronization is pending or
	// failed. Empty is accepted for legacy rows created before index_status.
	if chunk.IndexStatus == "processing" || chunk.IndexStatus == "failed" {
		return false
	}
	return slices.Contains([]types.ChunkType{
		types.ChunkTypeText, types.ChunkTypeSummary,
		types.ChunkTypeTableColumn, types.ChunkTypeTableSummary,
		types.ChunkTypeFAQ,
		types.ChunkTypeImageOCR, types.ChunkTypeImageCaption,
	}, chunk.ChunkType)
}
