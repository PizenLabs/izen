use anyhow::Result;
use lynx_protocol::{CodeChunk, SymbolRecord};
use std::path::Path;
use tree_sitter::{Parser, Query, QueryCursor, StreamingIterator};
use tree_sitter_python::LANGUAGE;

pub fn extract(path: &Path, content: &str) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
    let mut parser = Parser::new();
    parser.set_language(&LANGUAGE.into())?;

    let tree = parser
        .parse(content, None)
        .ok_or_else(|| anyhow::anyhow!("Failed to parse Python file"))?;
    let root_node = tree.root_node();

    let mut chunks = Vec::new();
    let mut symbols = Vec::new();

    let query_str = r#"
        (class_definition name: (identifier) @class_name) @class
        (function_definition name: (identifier) @func_name) @func
    "#;

    let query = Query::new(&LANGUAGE.into(), query_str)?;
    let mut cursor = QueryCursor::new();
    let mut captures = cursor.captures(&query, root_node, content.as_bytes());

    let module_path = path
        .parent()
        .unwrap_or_else(|| Path::new(""))
        .to_string_lossy()
        .replace('\\', "/");
    let module_path = if module_path.is_empty() || module_path == "." {
        "crate".to_string()
    } else {
        module_path
    };

    while let Some(&(ref mat, capture_index)) = captures.next() {
        let capture = mat.captures[capture_index];
        let capture_name = query.capture_names()[capture.index as usize];

        if !["func", "class"].contains(&capture_name) {
            continue;
        }

        let node = capture.node;
        let start_line = node.start_position().row + 1;
        let end_line = node.end_position().row + 1;
        let raw_content = node.utf8_text(content.as_bytes())?.to_string();

        let (kind, symbol_name) =
            match extract_py_symbol_info(node, capture_name, mat, &query, content.as_bytes()) {
                Some(info) => info,
                None => continue,
            };

        let file_path = path.to_string_lossy().replace('\\', "/");
        let symbol_id = format!("{}:{}:{}", kind, module_path, symbol_name);

        symbols.push(SymbolRecord {
            symbol_id: symbol_id.clone(),
            symbol_name: symbol_name.clone(),
            symbol_type: lynx_protocol::SymbolType::Definition,
            file_path: file_path.clone(),
            start_line,
            end_line,
        });

        chunks.push(CodeChunk {
            id: blake3::hash(raw_content.as_bytes()).to_string(),
            file_path: file_path.clone(),
            start_line,
            end_line,
            raw_content,
            symbols_defined: vec![symbol_id],
        });
    }

    Ok((chunks, symbols))
}

fn extract_py_symbol_info(
    node: tree_sitter::Node,
    capture_name: &str,
    mat: &tree_sitter::QueryMatch,
    query: &Query,
    content: &[u8],
) -> Option<(String, String)> {
    match capture_name {
        "func" => {
            let mut name = None;
            for capture in mat.captures {
                let name_cap = query.capture_names()[capture.index as usize];
                if name_cap == "func_name" {
                    name = capture.node.utf8_text(content).ok().map(|s| s.to_string());
                }
            }
            let name = name.or_else(|| {
                node.child_by_field_name("name")
                    .and_then(|n| n.utf8_text(content).ok().map(|s| s.to_string()))
            })?;

            // Walk up to see if this function is inside a class
            if let Some(class_name) = find_py_class_container(node, content) {
                Some(("method".to_string(), format!("{}.{}", class_name, name)))
            } else {
                Some(("func".to_string(), name))
            }
        }
        "class" => {
            let mut name = None;
            for capture in mat.captures {
                let name_cap = query.capture_names()[capture.index as usize];
                if name_cap == "class_name" {
                    name = capture.node.utf8_text(content).ok().map(|s| s.to_string());
                }
            }
            let name = name.or_else(|| {
                node.child_by_field_name("name")
                    .and_then(|n| n.utf8_text(content).ok().map(|s| s.to_string()))
            })?;
            Some(("class".to_string(), name))
        }
        _ => None,
    }
}

fn find_py_class_container(node: tree_sitter::Node, content: &[u8]) -> Option<String> {
    let mut current = node.parent();
    while let Some(parent) = current {
        if parent.kind() == "class_definition" {
            if let Some(name_node) = parent.child_by_field_name("name") {
                if let Ok(text) = name_node.utf8_text(content) {
                    return Some(text.to_string());
                }
            }
        }
        current = parent.parent();
    }
    None
}
