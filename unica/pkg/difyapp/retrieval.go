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
// DefaultTopK is how many passages the platform asks for. It is exported
// because a dataset that has just been created has no choice of its own to
// preserve, and the one caller in that position has to say so explicitly:
// reading the newborn's own value back and treating it as an override is how a
// new line silently inherits Dify's default instead of this one.
func DefaultTopK() int { return defaultTopK }

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

// IndexingUndecided reports whether a dataset has not been given an indexing
// technique yet.
//
// Dify decides the technique when the first document is indexed, not when the
// dataset is created, so a dataset nobody has uploaded to reports an empty
// string. That is a third state, and reading it as either of the other two
// causes a distinct failure: read as a mismatch, every freshly created
// knowledge base is refused the retrieval settings it needs and shown to its
// operator in red for something nobody did wrong; read as a match, a dataset
// really built on the other technique gets retrieval that returns nothing.
// ClassifyRetrieval asks it first so that no caller has to remember to.
func IndexingUndecided(indexingTechnique string) bool {
	return indexingTechnique == ""
}

// RetrievalVerdict is what a dataset's retrieval settings amount to.
type RetrievalVerdict string

const (
	// RetrievalUnset: no search method at all. The dataset accepts documents
	// and indexes them, and every query against it comes back empty.
	RetrievalUnset RetrievalVerdict = "unset"
	// RetrievalIndexPending: a search method is set and Dify has not decided
	// the indexing technique yet, because nothing has been indexed. Not a
	// fault — there is no index for the search method to disagree with.
	RetrievalIndexPending RetrievalVerdict = "index_pending"
	// RetrievalSound: the search method suits the index the documents were
	// built with.
	RetrievalSound RetrievalVerdict = "sound"
	// RetrievalMismatched: the technique is decided and the search method
	// disagrees with it. Every query returns nothing while the dataset reports
	// itself healthy and the API answers 200.
	RetrievalMismatched RetrievalVerdict = "mismatched"
)

// ClassifyRetrieval reduces a dataset's reported settings to one of the four
// states above.
//
// It exists so the order of the questions is decided once. Three surfaces read
// these two fields — the provisioning walk, the tenant's diagnostic card and
// the platform roster — and the order is a trap in all three: asking whether
// retrieval matches the index before asking whether the index is decided
// answers "no" for every freshly created knowledge base, which reads as
// misconfigured and is not. A fact answered by three algorithms is a fact that
// will eventually be answered three ways; each caller now decides only what to
// do about the verdict, not what the verdict is.
func ClassifyRetrieval(indexingTechnique, searchMethod string) RetrievalVerdict {
	switch {
	case searchMethod == "":
		return RetrievalUnset
	case IndexingUndecided(indexingTechnique):
		return RetrievalIndexPending
	case RetrievalMatchesIndex(indexingTechnique, searchMethod):
		return RetrievalSound
	default:
		return RetrievalMismatched
	}
}

// RetrievalMatchesIndex reports whether a dataset's search method suits the
// index its documents were built with.
//
// The pairing is not cosmetic: keyword search over an embedded index and
// semantic search over a keyword index both return nothing, for every query,
// while the dataset reports itself healthy and the API answers 200. This is the
// check that tells a knowledge base which has nothing to say apart from one
// which cannot be asked.
//
// An undecided index answers false, which is deliberately the opposite of what
// the write path does with it: this function reports whether a pairing is
// confirmed sound, and nothing about an empty dataset is confirmed. It is not a
// verdict of "misconfigured", so anything reporting a dataset's standing should
// call ClassifyRetrieval, which keeps the undecided state as its own answer.
// The write path faces the other direction: it is choosing the settings, so an
// undecided index is one with nothing to contradict, and it proceeds on the
// platform default.
func RetrievalMatchesIndex(indexingTechnique, searchMethod string) bool {
	if IndexingUndecided(indexingTechnique) || searchMethod == "" {
		return false
	}
	want := searchSemantic
	if indexingTechnique == IndexingEconomy {
		want = searchKeyword
	}
	return searchMethod == want
}
