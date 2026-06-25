use anyhow::Result;
use lynx_embed::EmbedderManager;
use lynx_parser::Parser;
use lynx_storage::{EmbeddingRecord, Storage};
use std::path::Path;
use walkdir::WalkDir;

pub struct Indexer<'a> {
    parser: &'a Parser,
    storage: &'a Storage,
    embedder: &'a EmbedderManager,
    include_tests: bool,
}

impl<'a> Indexer<'a> {
    pub fn new(
        parser: &'a Parser,
        storage: &'a Storage,
        embedder: &'a EmbedderManager,
        include_tests: bool,
    ) -> Self {
        Self {
            parser,
            storage,
            embedder,
            include_tests,
        }
    }

    pub async fn index_repository(&mut self, repo_path: &Path) -> Result<()> {
        for entry in WalkDir::new(repo_path)
            .into_iter()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_type().is_file())
        {
            let path = entry.path();
            if self.should_skip(path) {
                continue;
            }

            let content = match std::fs::read_to_string(path) {
                Ok(c) => c,
                Err(_) => continue, // Skip binary or unreadable files
            };

            let relative_path = path.strip_prefix(repo_path).unwrap_or(path).to_path_buf();
            let (chunks, symbols) = self.parser.parse_file(&relative_path, &content)?;

            if !chunks.is_empty() {
                self.storage.index_chunks(&chunks)?;
                let texts: Vec<String> = chunks
                    .iter()
                    .map(|chunk| chunk.raw_content.clone())
                    .collect();
                let embeddings = self.embedder.embed_batch(&texts).await?;
                let records: Vec<EmbeddingRecord> = chunks
                    .iter()
                    .cloned()
                    .zip(embeddings)
                    .map(|(chunk, embedding)| EmbeddingRecord { chunk, embedding })
                    .collect();
                self.storage.index_embeddings(records)?;
            }
            if !symbols.is_empty() {
                self.storage.index_symbols(&symbols)?;
            }
        }
        Ok(())
    }

    fn should_skip(&self, path: &Path) -> bool {
        let path_str = path.to_string_lossy();
        let base_skip = path_str.contains("/.git/")
            || path_str.contains("/node_modules/")
            || path_str.contains("/vendor/")
            || path_str.contains("/target/")
            || path_str.contains("/build/")
            || path_str.contains("/dist/");
        if base_skip {
            return true;
        }
        if !self.include_tests {
            return path_str.contains("/test/")
                || path_str.contains("/mock/")
                || path_str.contains("/generated/");
        }
        false
    }
}
