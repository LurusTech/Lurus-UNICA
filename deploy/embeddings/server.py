"""OpenAI-compatible text-embedding server.

Why this exists
---------------
Dify indexes knowledge documents one of two ways. "high_quality" embeds every
segment and retrieves on meaning; "economy" skips embedding entirely and builds
a keyword index from a bounded set of terms extracted per segment. Economy is
not a cheaper version of the same thing: a term the extractor did not pick
cannot be matched however often the document repeats it, and in Chinese that
includes most proper nouns and compound terms, because they are not single
tokens. A knowledge base indexed that way reports itself healthy and answers a
large share of real questions with "no information", or — worse — with a
neighbouring record's figures.

Avoiding that needs a text-embedding model, and the hosted LLM a deployment
already pays for usually does not serve one (DeepSeek, for instance, offers no
embedding endpoint). Rather than add a second vendor and a second API key for
what is a small, self-contained model, this serves one locally over the API
shape Dify already speaks, so it can be registered through the built-in
"OpenAI-API-compatible" provider with no Dify-side code.

Model
-----
Default BAAI/bge-base-zh-v1.5: 768 dimensions, trained for Chinese and English,
~400MB, fast enough on CPU for indexing and query loads of this size. Any
sentence-transformers-compatible model works via EMBEDDING_MODEL.

Embeddings are L2-normalised, which is what BGE expects so that a dot product
equals cosine similarity — Dify compares with cosine.

The BGE authors suggest prefixing *queries* (not documents) with a short
retrieval instruction. Dify sends both through this one endpoint with nothing
to tell them apart, and applying the prefix to documents would hurt, so it is
left off. The v1.5 series was trained to work without it.

Run
---
    EMBEDDING_PORT=8199 python server.py

Then in Dify: Settings - Model Provider - OpenAI-API-compatible, add a model of
type "Text Embedding" with the model name below and the API base URL of this
server (including the /v1 suffix). Any non-empty API key is accepted.
"""

import os
import time
from typing import List, Union

import numpy as np
import uvicorn
from fastapi import FastAPI
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

MODEL_NAME = os.environ.get("EMBEDDING_MODEL", "BAAI/bge-base-zh-v1.5")
PORT = int(os.environ.get("EMBEDDING_PORT", "8199"))
HOST = os.environ.get("EMBEDDING_HOST", "0.0.0.0")
# Batch size bounds peak memory during indexing, when Dify sends segments in
# bulk; queries arrive one at a time and never reach it.
BATCH_SIZE = int(os.environ.get("EMBEDDING_BATCH_SIZE", "16"))

app = FastAPI(title="unica embedding server")
_model: SentenceTransformer | None = None


def model() -> SentenceTransformer:
    """Load on first use so the port opens immediately and a health check does
    not have to wait out the model load."""
    global _model
    if _model is None:
        print(f"[embeddings] loading {MODEL_NAME} ...", flush=True)
        started = time.time()
        _model = SentenceTransformer(MODEL_NAME, device="cpu")
        print(f"[embeddings] loaded in {time.time() - started:.1f}s "
              f"(dim={_model.get_sentence_embedding_dimension()})", flush=True)
    return _model


class EmbeddingRequest(BaseModel):
    input: Union[str, List[str]]
    model: str | None = None
    encoding_format: str | None = None
    user: str | None = None


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_NAME, "loaded": _model is not None}


@app.get("/v1/models")
def list_models():
    """Some clients probe this before registering a model."""
    return {
        "object": "list",
        "data": [{"id": MODEL_NAME, "object": "model", "owned_by": "local", "created": 0}],
    }


@app.post("/v1/embeddings")
def embeddings(req: EmbeddingRequest):
    texts = [req.input] if isinstance(req.input, str) else list(req.input)
    if not texts:
        return JSONResponse(status_code=400, content={
            "error": {"message": "input must not be empty", "type": "invalid_request_error"}})

    # Empty strings are rejected by some backends and silently distort others;
    # a single space embeds cleanly and keeps the result indices aligned with
    # the request, which the caller relies on.
    texts = [t if t and t.strip() else " " for t in texts]

    vectors = model().encode(
        texts,
        batch_size=BATCH_SIZE,
        normalize_embeddings=True,
        convert_to_numpy=True,
        show_progress_bar=False,
    )
    vectors = np.asarray(vectors, dtype=np.float32)

    # Token counts are approximate: callers use them for reporting, never for
    # billing here, and an exact count would mean tokenising twice.
    approx_tokens = sum(len(t) for t in texts)

    return {
        "object": "list",
        "model": req.model or MODEL_NAME,
        "data": [
            {"object": "embedding", "index": i, "embedding": vec.tolist()}
            for i, vec in enumerate(vectors)
        ],
        "usage": {"prompt_tokens": approx_tokens, "total_tokens": approx_tokens},
    }


if __name__ == "__main__":
    print(f"[embeddings] serving {MODEL_NAME} on {HOST}:{PORT}", flush=True)
    uvicorn.run(app, host=HOST, port=PORT, log_level="warning")
