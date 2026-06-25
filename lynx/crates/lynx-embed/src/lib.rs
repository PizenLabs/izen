pub mod fastembed;
pub mod models;
pub mod onnx;

use anyhow::Result;
use async_trait::async_trait;

#[async_trait]
pub trait Embedder: Send + Sync {
    async fn embed(&self, text: &str) -> Result<Vec<f32>>;
    async fn embed_batch(&self, texts: &[String]) -> Result<Vec<Vec<f32>>>;
    fn dimension(&self) -> usize;
}

pub struct EmbedderManager {
    inner: Box<dyn Embedder>,
}

impl EmbedderManager {
    pub fn new(embedder: Box<dyn Embedder>) -> Self {
        Self { inner: embedder }
    }

    pub async fn embed(&self, text: &str) -> Result<Vec<f32>> {
        self.inner.embed(text).await
    }

    pub async fn embed_batch(&self, texts: &[String]) -> Result<Vec<Vec<f32>>> {
        self.inner.embed_batch(texts).await
    }

    pub fn dimension(&self) -> usize {
        self.inner.dimension()
    }
}
