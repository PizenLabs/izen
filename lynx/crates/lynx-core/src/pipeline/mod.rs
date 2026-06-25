use crate::classifier::{classify_query, QueryIntent};
use crate::ranking::Ranker;
use crate::retrieval::Retriever;
use anyhow::Result;
use lynx_embed::EmbedderManager;
use lynx_protocol::DiscoveryResult;
use lynx_storage::Storage;

/// Minimum confidence threshold calibrated on a [0.0, 1.0] scale.
/// Any search result scoring below this threshold is treated as structural noise and discarded.
const MIN_CONFIDENCE_THRESHOLD: f32 = 0.15;

pub struct SearchPipeline<'a> {
    storage: &'a Storage,
    embedder: &'a EmbedderManager,
    include_tests: bool,
}

impl<'a> SearchPipeline<'a> {
    pub fn new(storage: &'a Storage, embedder: &'a EmbedderManager, include_tests: bool) -> Self {
        Self {
            storage,
            embedder,
            include_tests,
        }
    }

    /// Performs lightweight local query expansion for high-level architectural search intents.
    /// Maps broad conceptual queries into concrete software-engineering tokens with zero allocation overhead.
    fn expand_architectural_query(&self, query: &str) -> String {
        let lower_query = query.to_lowercase();
        let mut expanded = query.to_string();

        // Static Architecture Thesaurus rules
        if lower_query.contains("auth") || lower_query.contains("authentication") {
            expanded.push_str(" login jwt token middleware authorize verify access session identity permission oauth");
        } else if lower_query.contains("database") || lower_query.contains("db") {
            expanded.push_str(
                " repository gorm sql transaction query storage client connection migration schema",
            );
        } else if lower_query.contains("api") || lower_query.contains("endpoint") {
            expanded.push_str(" controller handler route request response gateway payload");
        } else if lower_query.contains("cache") {
            expanded.push_str(" redis memcached ttl evict storage buffer local");
        } else if lower_query.contains("config") || lower_query.contains("setting") {
            expanded.push_str(" environment variable yaml json property flag registry");
        }

        expanded
    }

    pub async fn search(&self, query: &str) -> Result<Vec<DiscoveryResult>> {
        // Step 1: Categorize incoming string via the Query Intent Classifier
        let query_intent = classify_query(query);

        // Step 2: Fast-path routing for exact symbol definitions
        if query_intent == QueryIntent::Symbol {
            let symbol_results = self.storage.resolve_symbol_exact(query, 25)?;
            if !symbol_results.is_empty() {
                return Ok(symbol_results
                    .into_iter()
                    .map(|record| DiscoveryResult {
                        symbol_id: record.symbol_id,
                        score: 1.0, // Absolute confidence score assigned for deterministic hits
                        file_path: record.file_path,
                        start_line: record.start_line,
                        end_line: record.end_line,
                        reasons: vec!["Exact symbol match".to_string()],
                    })
                    .collect());
            }
        }

        // Step 3: Apply query transformation/expansion if dealing with structural flows
        let processed_query = match query_intent {
            QueryIntent::Flow | QueryIntent::Architecture => self.expand_architectural_query(query),
            _ => query.to_string(),
        };

        // Step 4: Execute parallel asynchronous candidates retrieval from hybrid storage backends
        let retriever = Retriever::new(self.storage, self.embedder);
        let (lexical_results, semantic_results) = tokio::try_join!(
            retriever.retrieve_lexical(&processed_query, 50),
            retriever.retrieve_semantic(&processed_query, 50)
        )?;

        // Step 5: Perform Reciprocal Rank Fusion (RRF) and inject heuristic modifiers via the Ranker.
        // The original user query is preserved here to evaluate exact distances against target identifier names.
        let mut ranked_results = Ranker::rank(
            query,
            query_intent,
            lexical_results,
            semantic_results,
            60.0, // Baseline internal routing token weight
            self.include_tests,
        );

        // Step 6: Apply the hard confidence cutoff floor to aggressively prune low-scoring noise
        ranked_results.retain(|result| result.score >= MIN_CONFIDENCE_THRESHOLD);

        // Step 7: Fuzzy Fallback Guard
        // Returns an empty clean vector if no signals satisfy the minimum threshold constraint,
        // preventing irrelevant infrastructure symbols from corrupting the stdout channel.
        if ranked_results.is_empty() {
            return Ok(vec![]);
        }

        Ok(ranked_results)
    }
}
