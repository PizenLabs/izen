use anyhow::Result;
use lynx_protocol::{CodeChunk, SymbolRecord};
use std::path::Path;
use tree_sitter::{Parser, Query, QueryCursor, StreamingIterator};
use tree_sitter_go::LANGUAGE;

pub fn extract(path: &Path, content: &str) -> Result<(Vec<CodeChunk>, Vec<SymbolRecord>)> {
    let mut parser = Parser::new();
    parser.set_language(&LANGUAGE.into())?;

    let tree = parser
        .parse(content, None)
        .ok_or_else(|| anyhow::anyhow!("Failed to parse Go file"))?;
    let root_node = tree.root_node();

    let mut chunks = Vec::new();
    let mut symbols = Vec::new();

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

    let query_str = r#"
        (function_declaration name: (identifier) @func_name) @func
        (method_declaration) @method
        (type_declaration (type_spec name: (type_identifier) @type_name)) @type
    "#;

    let query = Query::new(&LANGUAGE.into(), query_str)?;
    let mut cursor = QueryCursor::new();
    let mut captures = cursor.captures(&query, root_node, content.as_bytes());

    while let Some(&(ref mat, capture_index)) = captures.next() {
        let capture = mat.captures[capture_index];
        let capture_name = query.capture_names()[capture.index as usize];

        if !["func", "method", "type"].contains(&capture_name) {
            continue;
        }

        let node = capture.node;
        let start_line = node.start_position().row + 1;
        let end_line = node.end_position().row + 1;
        let raw_content = node.utf8_text(content.as_bytes())?.to_string();

        let (kind, symbol_name) =
            match extract_go_symbol_info(node, capture_name, mat, &query, content.as_bytes()) {
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

fn extract_go_symbol_info(
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
            Some(("func".to_string(), name))
        }
        "method" => {
            let mut receiver_type = None;
            let mut method_name = None;

            if let Some(receiver_node) = node.child_by_field_name("receiver") {
                receiver_type = find_receiver_type(receiver_node, content);
            }

            if let Some(name_node) = node.child_by_field_name("name") {
                method_name = name_node.utf8_text(content).ok().map(|s| s.to_string());
            }

            let method_name = method_name.or_else(|| {
                node.child_by_field_name("name")
                    .and_then(|n| n.utf8_text(content).ok().map(|s| s.to_string()))
            })?;

            let symbol_name = if let Some(rt) = receiver_type {
                format!("{}.{}", rt, method_name)
            } else {
                method_name
            };
            Some(("method".to_string(), symbol_name))
        }
        "type" => {
            let mut name = None;
            for capture in mat.captures {
                let name_cap = query.capture_names()[capture.index as usize];
                if name_cap == "type_name" {
                    name = capture.node.utf8_text(content).ok().map(|s| s.to_string());
                }
            }
            let name = name.or_else(|| {
                let mut cursor = node.walk();
                for child in node.named_children(&mut cursor) {
                    if child.kind() == "type_spec" {
                        if let Some(name_node) = child.child_by_field_name("name") {
                            return name_node.utf8_text(content).ok().map(|s| s.to_string());
                        }
                    }
                }
                None
            })?;

            let mut kind = "type".to_string();
            let mut cursor = node.walk();
            for child in node.named_children(&mut cursor) {
                if child.kind() == "type_spec" {
                    if let Some(type_node) = child.child_by_field_name("type") {
                        kind = determine_type_kind(type_node);
                    }
                    break;
                }
            }
            Some((kind, name))
        }
        _ => None,
    }
}

fn determine_type_kind(node: tree_sitter::Node) -> String {
    match node.kind() {
        "interface_type" => "interface".to_string(),
        "struct_type" => "struct".to_string(),
        _ => "type".to_string(),
    }
}

fn find_receiver_type(node: tree_sitter::Node, content: &[u8]) -> Option<String> {
    let mut cursor = node.walk();
    for child in node.named_children(&mut cursor) {
        if child.kind() == "parameter_declaration" {
            if let Some(type_node) = child.child_by_field_name("type") {
                return extract_type_identifier(type_node, content);
            }
        }
    }
    None
}

fn extract_type_identifier(node: tree_sitter::Node, content: &[u8]) -> Option<String> {
    match node.kind() {
        "type_identifier" => node.utf8_text(content).ok().map(|s| s.to_string()),
        "pointer_type" => {
            let mut cursor = node.walk();
            for child in node.children(&mut cursor) {
                if let Some(name) = extract_type_identifier(child, content) {
                    return Some(name);
                }
            }
            None
        }
        _ => {
            let mut cursor = node.walk();
            for child in node.children(&mut cursor) {
                if let Some(name) = extract_type_identifier(child, content) {
                    return Some(name);
                }
            }
            None
        }
    }
}
