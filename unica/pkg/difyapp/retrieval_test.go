package difyapp

import "testing"

// The pairing is the whole point of the file: a search method that does not
// suit the index returns nothing for every query while everything reports 200.
func TestRetrievalModel_PairsMethodWithIndex(t *testing.T) {
	cases := map[string]string{
		IndexingHighQuality: "semantic_search",
		IndexingEconomy:     "keyword_search",
	}
	for indexing, want := range cases {
		got, _ := RetrievalModel(indexing)["search_method"].(string)
		if got != want {
			t.Errorf("RetrievalModel(%q) search_method = %q, want %q", indexing, got, want)
		}
		if !RetrievalMatchesIndex(indexing, got) {
			t.Errorf("RetrievalModel(%q) produced settings its own check rejects", indexing)
		}
	}
}

// An undecided index is a state of its own, and the two readers of it face in
// opposite directions on purpose. This pins both directions together so a later
// change cannot quietly align them: making RetrievalMatchesIndex answer true
// would report an empty knowledge base as healthy, and making the write path
// treat undecided as a mismatch would refuse every dataset at creation.
func TestIndexingUndecided_IsNeitherMatchNorMismatch(t *testing.T) {
	if !IndexingUndecided("") {
		t.Fatal("an empty indexing technique is the undecided state")
	}
	if IndexingUndecided(IndexingEconomy) || IndexingUndecided(IndexingHighQuality) {
		t.Fatal("a named technique has been decided")
	}
	if RetrievalMatchesIndex("", "semantic_search") {
		t.Error("nothing about an empty dataset is confirmed sound; callers must ask IndexingUndecided first")
	}
}

func TestRetrievalMatchesIndex_RejectsTheCrossedPairs(t *testing.T) {
	if RetrievalMatchesIndex(IndexingEconomy, "semantic_search") {
		t.Error("semantic search over a keyword index retrieves nothing")
	}
	if RetrievalMatchesIndex(IndexingHighQuality, "keyword_search") {
		t.Error("keyword search over an embedded index retrieves nothing")
	}
	if RetrievalMatchesIndex(IndexingHighQuality, "") {
		t.Error("a dataset with no search method cannot be matching one")
	}
}

// Three surfaces switch on this verdict instead of comparing the two fields
// themselves, so the order of the questions is pinned here once. The trap is
// the middle case: asking whether retrieval matches the index before asking
// whether the index is decided answers "mismatched" for every freshly created
// knowledge base, which is the state provisioning has to be able to configure.
func TestClassifyRetrieval_SeparatesTheFourStates(t *testing.T) {
	cases := []struct {
		indexing string
		method   string
		want     RetrievalVerdict
	}{
		{"", "", RetrievalUnset},
		{IndexingHighQuality, "", RetrievalUnset},
		{"", "semantic_search", RetrievalIndexPending},
		{"", "keyword_search", RetrievalIndexPending},
		{IndexingHighQuality, "semantic_search", RetrievalSound},
		{IndexingEconomy, "keyword_search", RetrievalSound},
		{IndexingHighQuality, "keyword_search", RetrievalMismatched},
		{IndexingEconomy, "semantic_search", RetrievalMismatched},
	}
	for _, c := range cases {
		if got := ClassifyRetrieval(c.indexing, c.method); got != c.want {
			t.Errorf("ClassifyRetrieval(%q, %q) = %q, want %q", c.indexing, c.method, got, c.want)
		}
	}
}
