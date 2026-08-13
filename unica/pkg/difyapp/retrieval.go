package difyapp

// Retrieval settings for a product line's knowledge base.
//
// Dify gives a new dataset semantic search by default, regardless of how its
// documents will be indexed. On a deployment whose workspace has no embedding
// model — where documents must be indexed with the economy technique, which
// builds a keyword index instead of vectors — that default asks for a search
// the dataset has no vectors to serve. Provisioning therefore has to choose the
// search method that matches the indexing technique rather than accept the
// default, or every knowledge base it creates is born misconfigured.

const (
	// IndexingEconomy builds a keyword index: no embedding model required, and
	// only terms the extractor picked out of each segment can ever match.
	IndexingEconomy = "economy"
	// IndexingHighQuality embeds each segment, which needs a text-embedding
	// model configured in the workspace.
	IndexingHighQuality = "high_quality"

	searchKeyword  = "keyword_search"
	searchSemantic = "semantic_search"

	// defaultTopK is deliberately above Dify's own default of 2. Two segments
	// is enough only when ranking is reliable; keyword ranking is not, and a
	// question whose answer sits third is answered as if the answer did not
	// exist — or worse, from the two unrelated segments that outranked it. The
	// ceiling on how many segments a model can be given without losing the
	// thread is far higher than this, so the cost of the extra few is small
	// against the cost of missing the right one.
	defaultTopK = 6
)

// RetrievalModel returns the retrieval settings a dataset should be created
// with for the given indexing technique. Reranking stays off: it needs a rerank
// model, which a workspace lacking an embedding model will not have either.
func RetrievalModel(indexingTechnique string) map[string]interface{} {
	method := searchSemantic
	if indexingTechnique == IndexingEconomy {
		method = searchKeyword
	}
	return map[string]interface{}{
		"search_method":           method,
		"reranking_enable":        false,
		"reranking_model":         map[string]interface{}{"reranking_provider_name": "", "reranking_model_name": ""},
		"top_k":                   defaultTopK,
		"score_threshold_enabled": false,
		"score_threshold":         nil,
	}
}
