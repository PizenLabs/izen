use tantivy::schema::*;

pub struct ChunkSchema {
    pub schema: Schema,
    pub id: Field,
    pub file_path: Field,
    pub start_line: Field,
    pub end_line: Field,
    pub content: Field,
    pub symbols: Field,
}

impl ChunkSchema {
    pub fn new() -> Self {
        let mut schema_builder = Schema::builder();
        let id = schema_builder.add_text_field("id", STRING | STORED);
        let file_path = schema_builder.add_text_field("file_path", TEXT | STORED);
        let start_line = schema_builder.add_u64_field("start_line", STORED);
        let end_line = schema_builder.add_u64_field("end_line", STORED);
        let content = schema_builder.add_text_field("content", TEXT | STORED);
        let symbols = schema_builder.add_text_field("symbols", TEXT | STORED);

        Self {
            schema: schema_builder.build(),
            id,
            file_path,
            start_line,
            end_line,
            content,
            symbols,
        }
    }
}

impl Default for ChunkSchema {
    fn default() -> Self {
        Self::new()
    }
}

pub struct SymbolSchema {
    pub schema: Schema,
    pub symbol_id: Field,
    pub symbol_name: Field,
    pub symbol_name_exact: Field,
    pub file_path: Field,
    pub start_line: Field,
    pub end_line: Field,
}

impl SymbolSchema {
    pub fn new() -> Self {
        let mut schema_builder = Schema::builder();
        let symbol_id = schema_builder.add_text_field("symbol_id", STRING | STORED);
        let symbol_name = schema_builder.add_text_field("symbol_name", TEXT | STORED);
        let symbol_name_exact = schema_builder.add_text_field("symbol_name_exact", STRING | STORED);
        let file_path = schema_builder.add_text_field("file_path", TEXT | STORED);
        let start_line = schema_builder.add_u64_field("start_line", STORED);
        let end_line = schema_builder.add_u64_field("end_line", STORED);

        Self {
            schema: schema_builder.build(),
            symbol_id,
            symbol_name,
            symbol_name_exact,
            file_path,
            start_line,
            end_line,
        }
    }
}

impl Default for SymbolSchema {
    fn default() -> Self {
        Self::new()
    }
}
