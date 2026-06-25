pub mod cache;
pub mod schema;
pub mod tantivy;

use anyhow::Result;
use cache::EmbeddingCache;
use lynx_protocol::{CodeChunk, SymbolRecord};
use std::path::Path;
use std::sync::Mutex;

pub struct Storage {
    inner: tantivy::TantivyStorage,
    embedding_cache: Mutex<EmbeddingCache>,
}

impl Storage {
    pub fn new(path: &Path) -> Result<Self> {
        std::fs::create_dir_all(path)?;
        let cache_path = path.join("embeddings.json");
        Ok(Self {
            inner: tantivy::TantivyStorage::new(path)?,
            embedding_cache: Mutex::new(EmbeddingCache::new(cache_path)?),
        })
    }

    pub fn clear(&self) -> Result<()> {
        self.inner.clear()?;
        let mut cache = self
            .embedding_cache
            .lock()
            .map_err(|e| anyhow::anyhow!("Embedding cache lock poisoned: {}", e))?;
        cache.clear()?;
        Ok(())
    }

    pub fn index_chunks(&self, chunks: &[CodeChunk]) -> Result<()> {
        self.inner.index_chunks(chunks)
    }

    pub fn index_symbols(&self, symbols: &[SymbolRecord]) -> Result<()> {
        self.inner.index_symbols(symbols)
    }

    pub fn search_chunks(&self, query: &str, limit: usize) -> Result<Vec<CodeChunk>> {
        self.inner.search_chunks(query, limit)
    }

    pub fn search_chunks_with_scores(
        &self,
        query: &str,
        limit: usize,
    ) -> Result<Vec<(CodeChunk, f32)>> {
        self.inner.search_chunks_with_scores(query, limit)
    }

    pub fn search_symbols(&self, query: &str, limit: usize) -> Result<Vec<SymbolRecord>> {
        self.inner.search_symbols(query, limit)
    }

    pub fn resolve_symbol_exact(&self, query: &str, limit: usize) -> Result<Vec<SymbolRecord>> {
        self.inner.resolve_symbol_exact(query, limit)
    }

    pub fn index_embeddings(&self, records: Vec<EmbeddingRecord>) -> Result<()> {
        let mut cache = self
            .embedding_cache
            .lock()
            .map_err(|e| anyhow::anyhow!("Embedding cache lock poisoned: {}", e))?;
        cache.add_embeddings(records)
    }

    pub fn vector_search(
        &self,
        query_embedding: &[f32],
        limit: usize,
    ) -> Result<Vec<(CodeChunk, f32)>> {
        let cache = self
            .embedding_cache
            .lock()
            .map_err(|e| anyhow::anyhow!("Embedding cache lock poisoned: {}", e))?;
        Ok(cache.vector_search(query_embedding, limit))
    }

    pub fn find_embedding_by_location(
        &self,
        file_path: &str,
        line: usize,
    ) -> Result<Option<EmbeddingRecord>> {
        let cache = self
            .embedding_cache
            .lock()
            .map_err(|e| anyhow::anyhow!("Embedding cache lock poisoned: {}", e))?;
        Ok(cache.find_by_location(file_path, line).cloned())
    }
}

pub use cache::EmbeddingRecord;
