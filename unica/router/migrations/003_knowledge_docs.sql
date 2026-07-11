-- Migration 003: Knowledge documents table for tracking uploaded documents per product line.

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
