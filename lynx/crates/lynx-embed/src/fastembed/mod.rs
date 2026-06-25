use crate::Embedder;
use anyhow::Result;
use async_trait::async_trait;
use fastembed::{EmbeddingModel, InitOptions, TextEmbedding};
use std::sync::Mutex;

pub struct FastEmbedder {
    model: Mutex<TextEmbedding>,
}

impl FastEmbedder {
    pub fn new() -> Result<Self> {
        let mut options = InitOptions::default();
        options.model_name = EmbeddingModel::BGESmallENV15;
        options.show_download_progress = true;

        let model = TextEmbedding::try_new(options)?;
        Ok(Self {
            model: Mutex::new(model),
        })
    }
}

#[async_trait]
impl Embedder for FastEmbedder {
    async fn embed(&self, text: &str) -> Result<Vec<f32>> {
        let mut model = self
            .model
            .lock()
            .map_err(|e| anyhow::anyhow!("Mutex poisoned: {}", e))?;
        let embeddings = model.embed(vec![text], None)?;
        Ok(embeddings[0].clone())
    }

    async fn embed_batch(&self, texts: &[String]) -> Result<Vec<Vec<f32>>> {
        let mut model = self
            .model
            .lock()
            .map_err(|e| anyhow::anyhow!("Mutex poisoned: {}", e))?;
        let embeddings = model.embed(texts.iter().map(|s| s.as_str()).collect::<Vec<_>>(), None)?;
        Ok(embeddings)
    }

    fn dimension(&self) -> usize {
        384
    }
}
