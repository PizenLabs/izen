pub mod languages;
pub mod symbol_extraction;
pub mod tree_sitter;

use anyhow::Result;
use lynx_protocol::{CodeChunk, SymbolRecord};
use std::path::Path;

pub struct Parser {
    // We can add state here if needed, like a pool of tree-sitter parsers
}

impl Parser {
    pub fn new() -> Self {
        Self {}
    }
}

impl Default for Parser {
    fn default() -> Self {
        Self::new()
    }
}

impl Parser {
    pub fn parse_file(
        &self,
        path: &Path,
        content: &str,
    ) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        let extension = path.extension().and_then(|s| s.to_str()).unwrap_or("");

        match extension {
            "rs" => self.parse_rust(path, content),
            "go" => self.parse_go(path, content),
            "ts" | "tsx" => self.parse_typescript(path, content),
            "js" | "jsx" => self.parse_javascript(path, content),
            "py" => self.parse_python(path, content),
            _ => {
                // For unsupported languages, we might want to do basic line-based chunking
                // or just return empty for now
                Ok((vec![], vec![]))
            }
        }
    }

    fn parse_rust(
        &self,
        path: &Path,
        content: &str,
    ) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        symbol_extraction::rust::extract(path, content)
    }

    fn parse_go(&self, path: &Path, content: &str) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        symbol_extraction::go::extract(path, content)
    }

    fn parse_typescript(
        &self,
        path: &Path,
        content: &str,
    ) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        symbol_extraction::typescript::extract(path, content)
    }

    fn parse_javascript(
        &self,
        path: &Path,
        content: &str,
    ) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        symbol_extraction::javascript::extract(path, content)
    }

    fn parse_python(
        &self,
        path: &Path,
        content: &str,
    ) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
        symbol_extraction::python::extract(path, content)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn test_parse_go() {
        let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let auth_go_path = manifest_dir.join("../../testdata/auth.go");
        let content = std::fs::read_to_string(&auth_go_path).unwrap();
        let parser = Parser::new();
        let (chunks, symbols) = parser
            .parse_file(&PathBuf::from("testdata/auth.go"), &content)
            .unwrap();

        println!("CHUNKS:");
        for chunk in &chunks {
            println!(
                "  Chunk: {} {}-{} symbols: {:?}",
                chunk.id, chunk.start_line, chunk.end_line, chunk.symbols_defined
            );
        }
        println!("SYMBOLS:");
        for symbol in &symbols {
            println!("  Symbol ID: {}", symbol.symbol_id);
            println!("  Symbol Name: {}", symbol.symbol_name);
            println!("  File Path: {}", symbol.file_path);
            println!("  Lines: {}-{}", symbol.start_line, symbol.end_line);
        }

        assert!(!symbols.is_empty());
    }

    #[test]
    fn test_parse_rust() {
        let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let lib_rs_path = manifest_dir.join("src/lib.rs");
        let content = std::fs::read_to_string(&lib_rs_path).unwrap();
        let parser = Parser::new();
        let (chunks, symbols) = parser
            .parse_file(&PathBuf::from("src/lib.rs"), &content)
            .unwrap();

        println!("RUST CHUNKS:");
        for chunk in &chunks {
            println!(
                "  Chunk: {} {}-{} symbols: {:?}",
                chunk.id, chunk.start_line, chunk.end_line, chunk.symbols_defined
            );
        }
        println!("RUST SYMBOLS:");
        for symbol in &symbols {
            println!("  Symbol ID: {}", symbol.symbol_id);
            println!("  Symbol Name: {}", symbol.symbol_name);
            println!("  File Path: {}", symbol.file_path);
            println!("  Lines: {}-{}", symbol.start_line, symbol.end_line);
        }

        assert!(!symbols.is_empty());
        // Verify struct Parser is present
        assert!(symbols.iter().any(|s| s.symbol_id == "struct:src:Parser"));
        // Verify method new is present
        assert!(symbols
            .iter()
            .any(|s| s.symbol_id == "method:src:Parser.new"));
    }
}
