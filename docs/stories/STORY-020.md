# STORY-020: RAG Knowledge Base + Document Upload Pipeline

**Epic:** EPIC-003 (AI Smart Response & Knowledge Base)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a knowledge admin, I want to upload product documents and have them automatically vectorized for AI retrieval, so that the AI can answer product-specific questions.

---

## Description

### Background
The AI's effectiveness depends directly on the quality and completeness of its knowledge base. Each product line needs its own set of documents (product manuals, FAQ, pricing, policies) that Dify's RAG engine can retrieve and use to ground AI responses. This story builds the document upload pipeline that feeds Dify's knowledge base, enabling accurate, product-specific AI answers.

### Scope
**In scope:**
- Document upload via Dify Dataset API (PDF, DOCX, TXT, MD)
- Automatic chunking and vectorization (pgvector, managed by Dify)
- Document update (replace vectors without affecting other docs)
- Document delete (remove all associated vectors)
- Knowledge base status tracking in `knowledge_docs` table
- Upload pipeline accessible via internal API (for future Admin UI)
- Verification: AI answer quality improves after document upload

**Out of scope:**
- Admin UI for document management (STORY-033)
- Web scraping or automated document collection
- Custom embedding models (use Dify's default)

### Upload Flow
```
1. Knowledge admin uploads document (via API or Dify Web UI)
2. Document sent to Dify Dataset API:
   POST /datasets/{dataset_id}/document/create_by_file
3. Dify processes document:
   a. Parse (PDF/DOCX/TXT/MD extraction)
   b. Chunk (configurable: 500-1000 tokens per chunk)
   c. Embed (generate vectors via embedding model)
   d. Store (vectors in pgvector)
4. Document status tracked:
   - uploading → indexing → completed / error
5. Record document metadata in unica_core.knowledge_docs table
6. AI can now retrieve relevant chunks for this product line
```

---

## Acceptance Criteria

- [ ] Upload supports PDF, DOCX, TXT, MD formats via Dify Dataset API
- [ ] Documents automatically chunked (configurable chunk size, default 800 tokens)
- [ ] Chunks vectorized and stored in pgvector
- [ ] Document update replaces vectors without affecting other documents in same dataset
- [ ] Document delete removes all associated vectors cleanly
- [ ] knowledge_docs table tracks: id, product_line_id, dataset_id, dify_document_id, filename, status, vector_count, uploaded_at
- [ ] Upload status queryable (uploading/indexing/completed/error)
- [ ] Verified: After uploading a product FAQ, AI correctly answers questions from that FAQ
- [ ] Verified: Different product lines cannot access each other's documents

---

## Technical Notes

### Dify Dataset API
```
# Create document by file upload
POST /datasets/{dataset_id}/document/create_by_file
  Content-Type: multipart/form-data
  Authorization: Bearer {dataset_api_key}
  Body: file + data (JSON with indexing_technique, process_rule)

# Update document
POST /datasets/{dataset_id}/documents/{document_id}/update_by_file
  Content-Type: multipart/form-data

# Delete document
DELETE /datasets/{dataset_id}/documents/{document_id}

# Get document indexing status
GET /datasets/{dataset_id}/documents/{document_id}/indexing-status

# List documents
GET /datasets/{dataset_id}/documents
```

### Knowledge Docs Table
```sql
CREATE TABLE IF NOT EXISTS knowledge_docs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_line_id UUID NOT NULL REFERENCES product_lines(id),
    dify_dataset_id VARCHAR(255) NOT NULL,
    dify_document_id VARCHAR(255),
    filename VARCHAR(500) NOT NULL,
    file_size_bytes BIGINT,
    status VARCHAR(50) NOT NULL DEFAULT 'uploading',
    vector_count INTEGER DEFAULT 0,
    error_message TEXT,
    uploaded_by VARCHAR(255),
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_knowledge_docs_product_line ON knowledge_docs(product_line_id);
CREATE INDEX idx_knowledge_docs_status ON knowledge_docs(status);
```

### Internal Upload API
```go
// unica/router/internal/knowledge/manager.go

type KnowledgeManager struct {
    db         *sql.DB
    difyClient *DifyDatasetClient
}

type UploadRequest struct {
    ProductLineID string
    Filename      string
    FileData      io.Reader
    ChunkSize     int    // default 800
    UploadedBy    string
}

func (km *KnowledgeManager) Upload(ctx context.Context, req *UploadRequest) (*KnowledgeDoc, error)
func (km *KnowledgeManager) Update(ctx context.Context, docID string, fileData io.Reader) error
func (km *KnowledgeManager) Delete(ctx context.Context, docID string) error
func (km *KnowledgeManager) GetStatus(ctx context.Context, docID string) (*DocStatus, error)
func (km *KnowledgeManager) ListByProductLine(ctx context.Context, plID string) ([]KnowledgeDoc, error)
```

### Dify Dataset Client
```go
// unica/router/internal/bridge/dify_dataset.go

type DifyDatasetClient struct {
    httpClient *http.Client
    baseURL    string
}

func (d *DifyDatasetClient) CreateDocument(ctx context.Context, datasetID, apiKey string, filename string, data io.Reader, chunkSize int) (*DifyDocResponse, error)
func (d *DifyDatasetClient) UpdateDocument(ctx context.Context, datasetID, docID, apiKey string, data io.Reader) error
func (d *DifyDatasetClient) DeleteDocument(ctx context.Context, datasetID, docID, apiKey string) error
func (d *DifyDatasetClient) GetIndexingStatus(ctx context.Context, datasetID, docID, apiKey string) (*IndexingStatus, error)
```

### Chunking Configuration
```json
{
  "indexing_technique": "high_quality",
  "process_rule": {
    "mode": "custom",
    "rules": {
      "pre_processing_rules": [
        {"id": "remove_extra_spaces", "enabled": true},
        {"id": "remove_urls_emails", "enabled": false}
      ],
      "segmentation": {
        "separator": "\n",
        "max_tokens": 800
      }
    }
  }
}
```

### Components
- `unica/router/internal/knowledge/manager.go` — Upload/update/delete orchestration
- `unica/router/internal/bridge/dify_dataset.go` — Dify Dataset API client
- `unica/router/migrations/003_knowledge_docs.sql` — DB migration

---

## Dependencies

**Prerequisite:**
- STORY-019 (Dify Multi-Workspace — datasets must exist)

**Blocks:**
- STORY-021 (Router-Dify Integration — RAG retrieval depends on populated knowledge base)

---

## Definition of Done

- [ ] Document upload via Dify API working for PDF, DOCX, TXT, MD
- [ ] Chunking and vectorization verified (check pgvector table has embeddings)
- [ ] Update and delete operations verified
- [ ] knowledge_docs table populated with correct metadata
- [ ] Status tracking works (uploading → indexing → completed)
- [ ] Integration test: upload FAQ document → ask AI question from FAQ → correct answer
- [ ] Unit tests for KnowledgeManager and DifyDatasetClient (>=80% coverage)
- [ ] Code committed to `unica/router/`

---

## Story Points Breakdown

- **Dify Dataset API client:** 1.5 points
- **Knowledge manager + DB migration:** 1.5 points
- **Testing + verification:** 2 points
- **Total:** 5 points

**Rationale:** Moderate complexity — involves file upload handling, external API integration, async indexing status polling, and verification that RAG actually improves AI answers.
