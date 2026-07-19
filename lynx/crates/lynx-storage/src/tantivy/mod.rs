use crate::schema::{ChunkSchema, SymbolSchema};
use anyhow::Result;
use lynx_protocol::{CodeChunk, SymbolRecord};
use std::path::Path;
use tantivy::collector::TopDocs;
use tantivy::query::{BooleanQuery, Occur, TermQuery};
use tantivy::schema::TantivyDocument;
use tantivy::schema::{IndexRecordOption, Term, Value};
use tantivy::{doc, query::QueryParser, DocAddress, Index, IndexWriter, ReloadPolicy};

fn sanitize_query(query: &str) -> String {
    query
        .replace(['\r', '\n'], " ")
        .chars()
        .flat_map(|c| match c {
            '\\' => vec!['\\', '\\'],
            '"' => vec!['\\', '"'],
            '+' | '-' | '!' | '(' | ')' | ':' | '^' | '[' | ']' | '{' | '}' | '~' | '*' | '?'
            | '|' | '&' => {
                vec!['\\', c]
            }
            c if c.is_control() => vec![],
            c => vec![c],
        })
        .collect()
}

pub struct TantivyStorage {
    chunk_index: Index,
    symbol_index: Index,
    chunk_schema: ChunkSchema,
    symbol_schema: SymbolSchema,
}

impl TantivyStorage {
    pub fn new(path: &Path) -> Result<Self> {
        let chunk_schema = ChunkSchema::new();
        let symbol_schema = SymbolSchema::new();

        let chunk_path = path.join("chunks");
        let symbol_path = path.join("symbols");

        std::fs::create_dir_all(&chunk_path)?;
        std::fs::create_dir_all(&symbol_path)?;

        let chunk_index = Index::open_or_create(
            tantivy::directory::MmapDirectory::open(chunk_path)?,
            chunk_schema.schema.clone(),
        )?;
        let symbol_index = Index::open_or_create(
            tantivy::directory::MmapDirectory::open(symbol_path)?,
            symbol_schema.schema.clone(),
        )?;

        Ok(Self {
            chunk_index,
            symbol_index,
            chunk_schema,
            symbol_schema,
        })
    }

    pub fn clear(&self) -> Result<()> {
        // Use the default TantivyDocument type for the writer.
        let mut chunk_writer: IndexWriter<tantivy::schema::TantivyDocument> =
            self.chunk_index.writer(10_000_000)?;
        chunk_writer.delete_all_documents()?;
        chunk_writer.commit()?;

        let mut symbol_writer: IndexWriter<tantivy::schema::TantivyDocument> =
            self.symbol_index.writer(10_000_000)?;
        symbol_writer.delete_all_documents()?;
        symbol_writer.commit()?;

        Ok(())
    }

    pub fn index_chunks(&self, chunks: &[CodeChunk]) -> Result<()> {
        let mut writer: IndexWriter<TantivyDocument> = self.chunk_index.writer(50_000_000)?;
        for chunk in chunks {
            writer.add_document(doc!(
                self.chunk_schema.id => chunk.id.clone(),
                self.chunk_schema.file_path => chunk.file_path.clone(),
                self.chunk_schema.start_line => chunk.start_line as u64,
                self.chunk_schema.end_line => chunk.end_line as u64,
                self.chunk_schema.content => chunk.raw_content.clone(),
                self.chunk_schema.symbols => chunk.symbols_defined.join(" "),
            ))?;
        }
        writer.commit()?;
        Ok(())
    }

    pub fn index_symbols(&self, symbols: &[SymbolRecord]) -> Result<()> {
        let mut writer: IndexWriter<TantivyDocument> = self.symbol_index.writer(50_000_000)?;
        for symbol in symbols {
            writer.add_document(doc!(
                self.symbol_schema.symbol_id => symbol.symbol_id.clone(),
                self.symbol_schema.symbol_name => symbol.symbol_name.clone(),
                self.symbol_schema.symbol_name_exact => symbol.symbol_name.clone(),
                self.symbol_schema.file_path => symbol.file_path.clone(),
                self.symbol_schema.start_line => symbol.start_line as u64,
                self.symbol_schema.end_line => symbol.end_line as u64,
            ))?;
        }
        writer.commit()?;
        Ok(())
    }

    pub fn search_chunks(&self, query_str: &str, limit: usize) -> Result<Vec<CodeChunk>> {
        self.search_chunks_with_scores(query_str, limit)
            .map(|v| v.into_iter().map(|(c, _)| c).collect())
    }

    pub fn search_chunks_with_scores(
        &self,
        query_str: &str,
        limit: usize,
    ) -> Result<Vec<(CodeChunk, f32)>> {
        let reader = self
            .chunk_index
            .reader_builder()
            .reload_policy(ReloadPolicy::OnCommitWithDelay)
            .try_into()?;
        let searcher = reader.searcher();
        let query_parser = QueryParser::for_index(
            &self.chunk_index,
            vec![self.chunk_schema.content, self.chunk_schema.symbols],
        );
        let query = query_parser.parse_query(&sanitize_query(query_str))?;
        let top_docs: Vec<(f32, DocAddress)> =
            searcher.search(&query, &TopDocs::with_limit(limit).order_by_score())?;

        let mut results = Vec::new();
        for (score, doc_address) in top_docs {
            let retrieved_doc: tantivy::TantivyDocument = searcher.doc(doc_address)?;
            results.push((
                CodeChunk {
                    id: retrieved_doc
                        .get_first(self.chunk_schema.id)
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .to_string(),
                    file_path: retrieved_doc
                        .get_first(self.chunk_schema.file_path)
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .to_string(),
                    start_line: retrieved_doc
                        .get_first(self.chunk_schema.start_line)
                        .and_then(|v| v.as_u64())
                        .unwrap_or_default() as usize,
                    end_line: retrieved_doc
                        .get_first(self.chunk_schema.end_line)
                        .and_then(|v| v.as_u64())
                        .unwrap_or_default() as usize,
                    raw_content: retrieved_doc
                        .get_first(self.chunk_schema.content)
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .to_string(),
                    symbols_defined: retrieved_doc
                        .get_first(self.chunk_schema.symbols)
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .split_whitespace()
                        .map(|s| s.to_string())
                        .collect(),
                },
                score,
            ));
        }
        Ok(results)
    }

    pub fn search_symbols(&self, query_str: &str, limit: usize) -> Result<Vec<SymbolRecord>> {
        let reader = self
            .symbol_index
            .reader_builder()
            .reload_policy(ReloadPolicy::OnCommitWithDelay)
            .try_into()?;
        let searcher = reader.searcher();
        let query_parser = QueryParser::for_index(
            &self.symbol_index,
            vec![self.symbol_schema.symbol_name, self.symbol_schema.symbol_id],
        );
        let query = query_parser.parse_query(&sanitize_query(query_str))?;
        let top_docs: Vec<(f32, DocAddress)> =
            searcher.search(&query, &TopDocs::with_limit(limit).order_by_score())?;

        let mut results = Vec::new();
        for (_score, doc_address) in top_docs {
            let retrieved_doc: tantivy::TantivyDocument = searcher.doc(doc_address)?;
            results.push(SymbolRecord {
                symbol_id: retrieved_doc
                    .get_first(self.symbol_schema.symbol_id)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                symbol_name: retrieved_doc
                    .get_first(self.symbol_schema.symbol_name)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                symbol_type: lynx_protocol::SymbolType::Definition,
                file_path: retrieved_doc
                    .get_first(self.symbol_schema.file_path)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                start_line: retrieved_doc
                    .get_first(self.symbol_schema.start_line)
                    .and_then(|v| v.as_u64())
                    .unwrap_or_default() as usize,
                end_line: retrieved_doc
                    .get_first(self.symbol_schema.end_line)
                    .and_then(|v| v.as_u64())
                    .unwrap_or_default() as usize,
            });
        }
        Ok(results)
    }

    pub fn resolve_symbol_exact(&self, query_str: &str, limit: usize) -> Result<Vec<SymbolRecord>> {
        let reader = self
            .symbol_index
            .reader_builder()
            .reload_policy(ReloadPolicy::OnCommitWithDelay)
            .try_into()?;
        let searcher = reader.searcher();

        let mut clauses: Vec<(Occur, Box<dyn tantivy::query::Query>)> = Vec::new();
        let symbol_id_term = Term::from_field_text(self.symbol_schema.symbol_id, query_str);
        clauses.push((
            Occur::Should,
            Box::new(TermQuery::new(symbol_id_term, IndexRecordOption::Basic)),
        ));

        let symbol_name_term =
            Term::from_field_text(self.symbol_schema.symbol_name_exact, query_str);
        clauses.push((
            Occur::Should,
            Box::new(TermQuery::new(symbol_name_term, IndexRecordOption::Basic)),
        ));

        let query = BooleanQuery::new(clauses);
        let top_docs: Vec<(f32, DocAddress)> =
            searcher.search(&query, &TopDocs::with_limit(limit).order_by_score())?;

        let mut results = Vec::new();
        for (_score, doc_address) in top_docs {
            let retrieved_doc: tantivy::TantivyDocument = searcher.doc(doc_address)?;
            results.push(SymbolRecord {
                symbol_id: retrieved_doc
                    .get_first(self.symbol_schema.symbol_id)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                symbol_name: retrieved_doc
                    .get_first(self.symbol_schema.symbol_name)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                symbol_type: lynx_protocol::SymbolType::Definition,
                file_path: retrieved_doc
                    .get_first(self.symbol_schema.file_path)
                    .and_then(|v| v.as_str())
                    .unwrap_or_default()
                    .to_string(),
                start_line: retrieved_doc
                    .get_first(self.symbol_schema.start_line)
                    .and_then(|v| v.as_u64())
                    .unwrap_or_default() as usize,
                end_line: retrieved_doc
                    .get_first(self.symbol_schema.end_line)
                    .and_then(|v| v.as_u64())
                    .unwrap_or_default() as usize,
            });
        }

        Ok(results)
    }
}
