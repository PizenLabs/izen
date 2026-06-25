use anyhow::Result;
use lynx_embed::EmbedderManager;
use lynx_protocol::CodeChunk;
use lynx_storage::Storage;

pub struct Retriever<'a> {
    storage: &'a Storage,
    embedder: &'a EmbedderManager,
}

impl<'a> Retriever<'a> {
    pub fn new(storage: &'a Storage, embedder: &'a EmbedderManager) -> Self {
        Self { storage, embedder }
    }

    pub async fn retrieve_lexical(
        &self,
        query: &str,
        limit: usize,
    ) -> Result<Vec<(CodeChunk, f32)>> {
        // Tantivy returns docs, but doesn't easily return scores here with the current Storage API
        // I'll update Storage API to return scores
        self.storage.search_chunks_with_scores(query, limit)
    }

    pub async fn retrieve_semantic(
        &self,
        query: &str,
        limit: usize,
    ) -> Result<Vec<(CodeChunk, f32)>> {
        let embedding = self.embedder.embed(query).await?;
        self.storage.vector_search(&embedding, limit)
    }
}
