pub mod chunking;
pub mod classifier;
pub mod indexing;
pub mod models;
pub mod pipeline;
pub mod ranking;
pub mod retrieval;

use anyhow::Result;
use lynx_embed::fastembed::FastEmbedder;
use lynx_embed::EmbedderManager;
use lynx_parser::Parser;
use lynx_storage::Storage;
use std::path::Path;

pub struct Lynx {
    parser: Parser,
    storage: Storage,
    embedder: EmbedderManager,
    include_tests: bool,
}

impl Lynx {
    pub async fn new(storage_path: &Path) -> Result<Self> {
        let parser = Parser::new();
        let storage = Storage::new(storage_path)?;
        let embedder = EmbedderManager::new(Box::new(FastEmbedder::new()?));

        Ok(Self {
            parser,
            storage,
            embedder,
            include_tests: false,
        })
    }

    pub fn set_include_tests(&mut self, include_tests: bool) {
        self.include_tests = include_tests;
    }

    pub async fn index_repository(&self, repo_path: &Path) -> Result<()> {
        let mut indexer = indexing::Indexer::new(
            &self.parser,
            &self.storage,
            &self.embedder,
            self.include_tests,
        );
        indexer.index_repository(repo_path).await
    }

    pub async fn search(&self, query: &str) -> Result<Vec<lynx_protocol::DiscoveryResult>> {
        let pipeline =
            pipeline::SearchPipeline::new(&self.storage, &self.embedder, self.include_tests);
        pipeline.search(query).await
    }

    pub async fn resolve_symbol(&self, query: &str) -> Result<Vec<lynx_protocol::DiscoveryResult>> {
        let records = self.storage.search_symbols(query, 25)?;
        Ok(records
            .into_iter()
            .map(|record| lynx_protocol::DiscoveryResult {
                symbol_id: record.symbol_id,
                score: 1.0,
                file_path: record.file_path,
                start_line: record.start_line,
                end_line: record.end_line,
                reasons: vec!["Exact symbol lookup".to_string()],
            })
            .collect())
    }

    pub async fn find_related(
        &self,
        file_path: &str,
        line: usize,
    ) -> Result<Vec<lynx_protocol::DiscoveryResult>> {
        let record = self.storage.find_embedding_by_location(file_path, line)?;
        let Some(record) = record else {
            return Ok(vec![]);
        };

        let mut results = self.storage.vector_search(&record.embedding, 20)?;
        results.retain(|(chunk, _score)| {
            chunk.file_path != file_path || line < chunk.start_line || line > chunk.end_line
        });

        Ok(results
            .into_iter()
            .map(|(chunk, score)| lynx_protocol::DiscoveryResult {
                symbol_id: chunk
                    .symbols_defined
                    .first()
                    .cloned()
                    .unwrap_or_else(|| format!("file:{}", chunk.file_path)),
                score,
                file_path: chunk.file_path,
                start_line: chunk.start_line,
                end_line: chunk.end_line,
                reasons: vec!["Embedding similarity".to_string()],
            })
            .collect())
    }
}
