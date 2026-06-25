use anyhow::Result;
use lynx_protocol::CodeChunk;
use serde::{Deserialize, Serialize};
use std::cmp::Ordering;
use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EmbeddingRecord {
    pub chunk: CodeChunk,
    pub embedding: Vec<f32>,
}

pub struct EmbeddingCache {
    path: PathBuf,
    records: HashMap<String, EmbeddingRecord>,
}

impl EmbeddingCache {
    pub fn new(path: PathBuf) -> Result<Self> {
        let records = if path.exists() {
            let data = fs::read_to_string(&path)?;
            let records_vec: Vec<EmbeddingRecord> = serde_json::from_str(&data)?;
            records_vec
                .into_iter()
                .map(|record| (record.chunk.id.clone(), record))
                .collect()
        } else {
            HashMap::new()
        };

        Ok(Self { path, records })
    }

    pub fn add_embeddings(&mut self, records: Vec<EmbeddingRecord>) -> Result<()> {
        for record in records {
            self.records.insert(record.chunk.id.clone(), record);
        }
        self.persist()
    }

    pub fn vector_search(&self, query_embedding: &[f32], limit: usize) -> Vec<(CodeChunk, f32)> {
        let mut results: Vec<(CodeChunk, f32)> = self
            .records
            .values()
            .filter_map(|record| {
                if record.embedding.len() != query_embedding.len() {
                    return None;
                }
                let score = cosine_similarity(query_embedding, &record.embedding);
                Some((record.chunk.clone(), score))
            })
            .collect();

        results.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(Ordering::Equal));
        results.truncate(limit);
        results
    }

    pub fn find_by_location(&self, file_path: &str, line: usize) -> Option<&EmbeddingRecord> {
        self.records.values().find(|record| {
            record.chunk.file_path == file_path
                && line >= record.chunk.start_line
                && line <= record.chunk.end_line
        })
    }

    pub fn get(&self, chunk_id: &str) -> Option<&EmbeddingRecord> {
        self.records.get(chunk_id)
    }

    pub fn clear(&mut self) -> Result<()> {
        self.records.clear();
        self.persist()
    }

    fn persist(&self) -> Result<()> {
        let mut records: Vec<&EmbeddingRecord> = self.records.values().collect();
        records.sort_by(|a, b| a.chunk.id.cmp(&b.chunk.id));
        let serialized = serde_json::to_string(&records)?;
        fs::write(&self.path, serialized)?;
        Ok(())
    }
}

fn cosine_similarity(a: &[f32], b: &[f32]) -> f32 {
    let mut dot = 0.0;
    let mut norm_a = 0.0;
    let mut norm_b = 0.0;

    for (x, y) in a.iter().zip(b.iter()) {
        dot += x * y;
        norm_a += x * x;
        norm_b += y * y;
    }

    if norm_a == 0.0 || norm_b == 0.0 {
        0.0
    } else {
        dot / (norm_a.sqrt() * norm_b.sqrt())
    }
}
