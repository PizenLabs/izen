use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
pub enum SymbolType {
    Definition,
    Reference,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SymbolRecord {
    pub symbol_id: String,
    pub symbol_name: String,
    pub symbol_type: SymbolType,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CodeChunk {
    pub id: String,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
    pub raw_content: String,
    pub symbols_defined: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DiscoveryResult {
    pub symbol_id: String,
    pub score: f32,
    pub file_path: String,
    pub start_line: usize,
    pub end_line: usize,
    #[serde(default)]
    pub reasons: Vec<String>,
}
