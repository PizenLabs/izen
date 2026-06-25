use anyhow::Result;
use clap::{Parser, Subcommand};
use lynx_core::Lynx;
use serde::Deserialize;
use serde_json::json;
use std::future;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use tokio::io::{self, AsyncBufReadExt, AsyncWriteExt, BufReader};

#[derive(Parser)]
#[command(name = "lx")]
#[command(about = "Lynx: Discovery Engine for AI-Native Software Engineering", long_about = None)]
struct Cli {
    #[arg(short, long, default_value = ".lynx")]
    storage_path: PathBuf,

    #[command(subcommand)]
    command: Commands,
}

// Add version subcommand

#[derive(Subcommand)]
enum Commands {
    /// Show version information
    /// Index a repository
    Index {
        #[arg(default_value = ".")]
        path: PathBuf,
        /// Include test, mock, generated files in indexing
        #[arg(long, action = clap::ArgAction::SetTrue, default_value_t = false)]
        include_tests: bool,
    },
    /// Search the index
    Search {
        query: String,
        /// Include test, mock, generated files in search results
        #[arg(long, action = clap::ArgAction::SetTrue, default_value_t = false)]
        include_tests: bool,
    },
    /// Resolve a symbol by name
    Resolve { name: String },
    /// Find related implementations
    Related { location: String },
    /// Discover and visualize control flow using Lea
    Flow { query: String },
    /// Start MCP (Model Context Protocol) server over stdio
    Mcp,
    /// Show version information
    Version,
    #[command(hide = true)]
    Init {
        #[arg(default_value = ".")]
        path: PathBuf,
    },
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt::init();

    let cli = Cli::parse();
    let mut lynx = Lynx::new(&cli.storage_path).await?;

    match cli.command {
        Commands::Index {
            path,
            include_tests,
        } => {
            println!("Indexing repository at {:?}", path);
            lynx.set_include_tests(include_tests);
            lynx.index_repository(&path).await?;
            println!("Indexing complete.");
        }
        Commands::Search {
            query,
            include_tests,
        } => {
            lynx.set_include_tests(include_tests);
            let results = lynx.search(&query).await?;
            if results.is_empty() {
                println!("No results found.");
            } else {
                for result in results {
                    println!("{}", format_discovery(&result));
                }
            }
        }
        Commands::Resolve { name } => {
            let results = lynx.resolve_symbol(&name).await?;
            if results.is_empty() {
                println!("No symbols found.");
            } else {
                for result in results {
                    println!("{}", format_discovery(&result));
                }
            }
        }
        Commands::Related { location } => {
            let (file_path, line) = parse_location(&location)?;
            let results = lynx.find_related(&file_path, line).await?;
            if results.is_empty() {
                println!("No related results found.");
            } else {
                for result in results {
                    println!("{}", format_discovery(&result));
                }
            }
        }
        Commands::Flow { query } => {
            let results = lynx.search(&query).await?;
            if let Some(top_result) = results.first() {
                println!("Flow for: {}", top_result.symbol_id);
                let status = Command::new("lea")
                    .arg("flow")
                    .arg(&top_result.symbol_id)
                    .status()?;
                if !status.success() {
                    return Err(anyhow::anyhow!("lea flow failed with status {}", status));
                }
            } else {
                println!("No results found for query: {}", query);
            }
        }
        Commands::Version => {
            println!("lx version {}", env!("CARGO_PKG_VERSION"));
        }
        Commands::Mcp => {
            let storage_dir = find_lynx_dir()
                .map(|root| root.join(".lynx"))
                .unwrap_or_else(|| cli.storage_path.clone());

            let lynx = Lynx::new(&storage_dir).await?;

            let stdin = BufReader::new(io::stdin());
            let mut stdout = io::stdout();
            let mut lines = stdin.lines();

            loop {
                tokio::select! {
                                    line = lines.next_line() => {
                                        match line {
                                            Ok(Some(line)) => {
                                                let line = line.trim().to_string();
                                                if line.is_empty() {
                                                    continue;
                                                }

                let raw: serde_json::Value = match serde_json::from_str(&line) {
                                                Ok(v) => v,
                                                Err(err) => {
                                                    let response = json!({"jsonrpc": "2.0", "id": null, "error": {"code": -32700, "message": "Parse error", "data": err.to_string()}});
                                                    let mut buf = serde_json::to_string(&response)?;
                                                    buf.push('\n');
                                                    let _ = stdout.write_all(buf.as_bytes()).await;
                                                    let _ = stdout.flush().await;
                                                    continue;
                                                }
                                            };
                                            let request_id = raw.get("id").cloned();
                                            let request: McpRequest = match serde_json::from_value(raw) {
                                                Ok(r) => r,
                                                Err(err) => {
                                                    let response = json!({"jsonrpc": "2.0", "id": request_id, "error": {"code": -32600, "message": "Invalid Request", "data": err.to_string()}});
                                                    let mut buf = serde_json::to_string(&response)?;
                                                    buf.push('\n');
                                                    let _ = stdout.write_all(buf.as_bytes()).await;
                                                    let _ = stdout.flush().await;
                                                    continue;
                                                }
                                            };

                                                let response = handle_mcp_request(&lynx, request, request_id).await;
                                                if response.is_null() {
                                                    continue;
                                                }
                                                let mut buf = serde_json::to_string(&response)?;
                                                buf.push('\n');
                                                if let Err(e) = stdout.write_all(buf.as_bytes()).await {
                                                    eprintln!("write error: {}", e);
                                                    break;
                                                }
                                                if let Err(e) = stdout.flush().await {
                                                    eprintln!("flush error: {}", e);
                                                    break;
                                                }
                                            }
                                            Ok(None) => break,
                                            Err(e) => {
                                                eprintln!("stdin error: {}", e);
                                                break;
                                            }
                                        }
                                    }
                                    _ = future::pending::<()>() => {}
                                }
            }
        }
        Commands::Init { path } => {
            println!("Initializing indexes at {:?}", path);
            let mut lea_child = Command::new("lea")
                .arg("index")
                .arg(&path)
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()?;
            lynx.index_repository(&path).await?;
            let status = lea_child.wait()?;
            if !status.success() {
                return Err(anyhow::anyhow!("lea index failed with status {}", status));
            }
            println!("Initialization complete.");
        }
    }

    Ok(())
}

fn parse_location(location: &str) -> Result<(String, usize)> {
    let mut parts = location.rsplitn(2, ':');
    let line_part = parts
        .next()
        .ok_or_else(|| anyhow::anyhow!("Missing line number"))?;
    let file_part = parts
        .next()
        .ok_or_else(|| anyhow::anyhow!("Missing file path"))?;
    let line: usize = line_part
        .parse()
        .map_err(|_| anyhow::anyhow!("Invalid line number"))?;
    Ok((file_part.to_string(), line))
}

fn format_discovery(result: &lynx_protocol::DiscoveryResult) -> String {
    let (kind, symbol_name) = split_symbol_id(&result.symbol_id, &result.file_path);
    let lines = if result.start_line == result.end_line {
        format!("{}", result.start_line)
    } else {
        format!("{}-{}", result.start_line, result.end_line)
    };

    // Normalize score to 0-100% for display
    // BM25 + Vector scores can be small, so we use a scaling factor
    let percentage = (result.score * 100.0).min(100.0);

    let confidence = if percentage > 85.0 {
        "High"
    } else if percentage > 50.0 {
        "Medium"
    } else {
        "Low"
    };

    let why_str = if result.reasons.is_empty() {
        "".to_string()
    } else {
        let reasons_list: Vec<String> = result
            .reasons
            .iter()
            .map(|r| format!("  - {}", r))
            .collect();
        format!("\n  Why:\n{}\n", reasons_list.join("\n"))
    };

    format!(
        "{}\n  {}\n\n  Confidence: {} ({:.0}%)\n{}\n  Symbol:\n  {}\n\n  File:\n  {}:{}\n",
        kind.to_uppercase(),
        symbol_name,
        confidence,
        percentage,
        why_str,
        result.symbol_id,
        result.file_path,
        lines
    )
}

fn split_symbol_id(symbol_id: &str, file_path: &str) -> (String, String) {
    if let Some(rest) = symbol_id.strip_prefix("file:") {
        let display_name = Path::new(rest)
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or(rest);
        return ("file".to_string(), display_name.to_string());
    }

    // New format: kind:package:SymbolName or kind:package:Receiver.MethodName
    let parts: Vec<&str> = symbol_id.split(':').collect();
    if parts.len() >= 3 {
        let kind = parts[0];
        let symbol_name = parts.last().unwrap_or(&"");
        return (kind.to_string(), symbol_name.to_string());
    }

    // Fallback for old format or unexpected formats
    let mut tail = symbol_id.rsplitn(2, ':');
    let symbol_name = tail.next().unwrap_or(symbol_id);
    if let Some(head) = tail.next() {
        let mut head_parts = head.splitn(2, ':');
        let kind = head_parts.next().unwrap_or("symbol");
        return (kind.to_string(), symbol_name.to_string());
    }

    let fallback = Path::new(file_path)
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or(symbol_id);
    ("symbol".to_string(), fallback.to_string())
}

fn find_lynx_dir() -> Option<PathBuf> {
    let mut current = std::env::current_dir().ok()?;
    loop {
        if current.join(".lynx").is_dir() {
            return Some(current);
        }
        if !current.pop() {
            return None;
        }
    }
}

async fn handle_mcp_request(
    lynx: &Lynx,
    request: McpRequest,
    id: Option<serde_json::Value>,
) -> serde_json::Value {
    let id = match id {
        Some(id) => id,
        None => return serde_json::Value::Null,
    };

    match request.method.as_str() {
        "initialize" => {
            json!({"jsonrpc": "2.0", "id": id, "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {
                    "tools": {}
                },
                "serverInfo": {
                    "name": "lynx-mcp",
                    "version": env!("CARGO_PKG_VERSION")
                }
            }})
        }
        "notifications/initialized" => serde_json::Value::Null,
        "tools/list" => {
            json!({"jsonrpc": "2.0", "id": id, "result": {
                "tools": [
                    {
                        "name": "lynx_search_graph",
                        "description": "Search the codebase for relevant code",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "query": {"type": "string", "description": "Search query"}
                            },
                            "required": ["query"]
                        }
                    },
                    {
                        "name": "lynx_resolve_symbol",
                        "description": "Resolve a symbol by name within the codebase",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "name": {"type": "string", "description": "Symbol name"}
                            },
                            "required": ["name"]
                        }
                    },
                    {
                        "name": "lynx_find_related",
                        "description": "Find related implementations across the codebase",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "file": {"type": "string", "description": "File path"},
                                "line": {"type": "number", "description": "Line number"}
                            },
                            "required": ["file", "line"]
                        }
                    }
                ]
            }})
        }
        "lynx_search_graph" => {
            let query = request
                .params
                .as_ref()
                .and_then(|value| value.get("query"))
                .and_then(|value| value.as_str());

            match query {
                Some(query) => match lynx.search(query).await {
                    Ok(results) => json!({"jsonrpc": "2.0", "id": id, "result": results}),
                    Err(err) => {
                        json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32603, "message": err.to_string()}})
                    }
                },
                None => {
                    json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32602, "message": "Missing query parameter"}})
                }
            }
        }
        "lynx_resolve_symbol" => {
            let name = request
                .params
                .as_ref()
                .and_then(|value| value.get("name"))
                .and_then(|value| value.as_str());

            match name {
                Some(name) => match lynx.resolve_symbol(name).await {
                    Ok(results) => json!({"jsonrpc": "2.0", "id": id, "result": results}),
                    Err(err) => {
                        json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32603, "message": err.to_string()}})
                    }
                },
                None => {
                    json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32602, "message": "Missing name parameter"}})
                }
            }
        }
        "lynx_find_related" => {
            let file_path = request
                .params
                .as_ref()
                .and_then(|value| value.get("file"))
                .and_then(|value| value.as_str());
            let line = request
                .params
                .as_ref()
                .and_then(|value| value.get("line"))
                .and_then(|value| value.as_u64());

            match (file_path, line) {
                (Some(file_path), Some(line)) => {
                    match lynx.find_related(file_path, line as usize).await {
                        Ok(results) => json!({"jsonrpc": "2.0", "id": id, "result": results}),
                        Err(err) => {
                            json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32603, "message": err.to_string()}})
                        }
                    }
                }
                _ => {
                    json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32602, "message": "Missing file or line parameter"}})
                }
            }
        }
        _ => {
            json!({"jsonrpc": "2.0", "id": id, "error": {"code": -32601, "message": "Method not found"}})
        }
    }
}

#[derive(Deserialize)]
struct McpRequest {
    #[allow(dead_code)]
    id: Option<serde_json::Value>,
    method: String,
    params: Option<serde_json::Value>,
}
