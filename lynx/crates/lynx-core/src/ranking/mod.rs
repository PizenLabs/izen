use crate::classifier::QueryIntent;
use lynx_protocol::{CodeChunk, DiscoveryResult};
use std::collections::HashMap;

pub const MIN_CONFIDENCE_THRESHOLD: f32 = 0.15;

pub struct Ranker;

struct ScoredChunk {
    chunk: CodeChunk,
    score: f32,
    reasons: Vec<String>,
}

impl Ranker {
    pub fn rank(
        query: &str,
        query_intent: QueryIntent,
        lexical_results: Vec<(CodeChunk, f32)>,
        semantic_results: Vec<(CodeChunk, f32)>,
        k: f32,
        include_tests: bool,
    ) -> Vec<DiscoveryResult> {
        let (alpha, beta) = query_intent.weights();
        let mut scores: HashMap<String, ScoredChunk> = HashMap::new();

        // Process lexical candidate hits into the unified scoring tracker
        for (i, (chunk, _score)) in lexical_results.into_iter().enumerate() {
            let entry = scores.entry(chunk.id.clone()).or_insert(ScoredChunk {
                chunk: chunk.clone(),
                score: 0.0,
                reasons: Vec::new(),
            });
            entry.score += alpha / (k + i as f32);
            if !entry.reasons.contains(&"Lexical match".to_string()) {
                entry.reasons.push("Lexical match".to_string());
            }
        }

        // Process semantic candidate hits into the unified scoring tracker
        for (i, (chunk, _score)) in semantic_results.into_iter().enumerate() {
            let entry = scores.entry(chunk.id.clone()).or_insert(ScoredChunk {
                chunk: chunk.clone(),
                score: 0.0,
                reasons: Vec::new(),
            });
            entry.score += beta / (k + i as f32);
            if !entry.reasons.contains(&"Semantic match".to_string()) {
                entry.reasons.push("Semantic match".to_string());
            }
        }

        let mut scored_chunks: Vec<ScoredChunk> = scores.into_values().collect();

        // Noise suppression and active workspace path filtering
        if !include_tests {
            scored_chunks.retain(|scored| {
                let path = scored.chunk.file_path.to_lowercase();
                !(path.contains("vendor")
                    || path.contains("node_modules")
                    || path.contains("mock")
                    || path.contains("test")
                    || path.contains("generated")
                    || path.contains(".pb.go")
                    || path.contains("fixtures")
                    || path.contains("examples"))
            });
        }

        // Apply baseline system infrastructure penalties
        apply_noise_suppression(&mut scored_chunks);

        // Apply targeted syntax structure match boosts
        apply_definition_boost(&mut scored_chunks, query);
        apply_identifier_boost(&mut scored_chunks, query);

        // Apply intent-specific heuristic transformations
        match query_intent {
            QueryIntent::Semantic | QueryIntent::Flow | QueryIntent::Architecture => {
                apply_concept_boost(&mut scored_chunks, query);
                apply_intent_boost(&mut scored_chunks);

                // CRITICAL FIX: Explicitly invoke generic boilerplate mitigation
                // to aggressively suppress infrastructure symbols (main, new, init)
                apply_generic_symbol_penalty(&mut scored_chunks);
            }
            _ => {}
        }

        // Execute structural package/file grouping reinforcement
        apply_file_coherence_boost(&mut scored_chunks);

        // Normalize computed RRF scores and modifiers to a stable [0.0, 1.0] confidence scale
        let max_rrf = 1.0 / k;
        for scored in scored_chunks.iter_mut() {
            // Calibrate score distribution against theoretical max boundaries and cap at 1.0
            scored.score = (scored.score / (max_rrf * 2.0)).min(1.0);
        }

        // In-place filtration to discard low-confidence noise rows
        scored_chunks.retain(|scored| scored.score >= MIN_CONFIDENCE_THRESHOLD);

        // Sort descending based on final normalized confidence
        scored_chunks.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });

        // Map internal structural trackers back to external protocol data transfer objects (DTOs)
        scored_chunks
            .into_iter()
            .map(|scored| DiscoveryResult {
                symbol_id: scored
                    .chunk
                    .symbols_defined
                    .first()
                    .cloned()
                    .unwrap_or_else(|| format!("file:{}", scored.chunk.file_path)),
                score: scored.score,
                file_path: scored.chunk.file_path,
                start_line: scored.chunk.start_line,
                end_line: scored.chunk.end_line,
                reasons: scored.reasons,
            })
            .collect()
    }
}

fn apply_intent_boost(scored_chunks: &mut [ScoredChunk]) {
    for scored in scored_chunks.iter_mut() {
        for symbol_id in &scored.chunk.symbols_defined {
            let symbol_name = symbol_id
                .split(':')
                .next_back()
                .unwrap_or(symbol_id)
                .to_lowercase();

            // Heuristic boost for potential service entry points, handlers, and public interfaces
            if symbol_name.contains("service")
                || symbol_name.contains("handler")
                || symbol_name.contains("controller")
                || symbol_name.contains("router")
                || symbol_name.contains("interface")
                || symbol_name.contains("api")
            {
                scored.score *= 2.5;
                scored
                    .reasons
                    .push("Intent-based service/handler boost".to_string());
            }
        }
    }
}

fn is_definition_of_query(chunk: &CodeChunk, query: &str) -> bool {
    let query_lower = query.to_lowercase();
    let query_tokens: Vec<&str> = query_lower.split_whitespace().collect();

    for symbol_id in &chunk.symbols_defined {
        let symbol_name = symbol_id
            .split(':')
            .next_back()
            .unwrap_or(symbol_id)
            .to_lowercase();

        if symbol_name == query_lower {
            return true;
        }

        // Parse token components separated by object/module access tokens
        let name_parts: Vec<&str> = symbol_name.split('.').collect();
        for part in name_parts {
            if query_tokens.contains(&part) {
                return true;
            }
        }
    }
    false
}

fn apply_definition_boost(scored_chunks: &mut [ScoredChunk], query: &str) {
    for scored in scored_chunks {
        if is_definition_of_query(&scored.chunk, query) {
            scored.score *= 2.0;
            scored.reasons.push("Definition boost".to_string());
        }
    }
}

fn apply_identifier_boost(scored_chunks: &mut [ScoredChunk], query: &str) {
    let query_tokens: Vec<String> = query
        .split_whitespace()
        .map(|token| token.to_lowercase())
        .collect();

    for scored in scored_chunks {
        if scored.chunk.symbols_defined.is_empty() {
            continue;
        }

        let mut boosted = false;
        for symbol_id in &scored.chunk.symbols_defined {
            let symbol_name = symbol_id.split(':').next_back().unwrap_or(symbol_id);
            let symbol_name_lower = symbol_name.to_lowercase();
            if query_tokens
                .iter()
                .any(|token| symbol_name_lower == *token || symbol_name_lower.contains(token))
            {
                boosted = true;
                break;
            }
        }

        if boosted {
            scored.score *= 1.3;
            scored.reasons.push("Identifier match boost".to_string());
        }
    }
}

fn apply_noise_suppression(scored_chunks: &mut [ScoredChunk]) {
    for scored in scored_chunks {
        let path_lower = scored.chunk.file_path.to_lowercase();
        let mut penalty = 1.0;
        let mut reason = "";

        if path_lower.contains("vendor") || path_lower.contains("node_modules") {
            penalty = 0.01;
            reason = "Vendor/node_modules penalty";
        } else if path_lower.contains("generated") || path_lower.contains(".pb.go") {
            penalty = 0.05;
            reason = "Generated code penalty";
        } else if path_lower.ends_with("_test.go")
            || path_lower.ends_with(".test.ts")
            || path_lower.ends_with("_test.rs")
            || path_lower.ends_with("_test.py")
            || path_lower.contains("testdata")
        {
            penalty = 0.10;
            reason = "Test/testdata penalty";
        } else if path_lower.contains("mock") || path_lower.contains("test") {
            penalty = 0.20;
            reason = "Mock/Test directory penalty";
        } else if path_lower.contains("fixtures")
            || path_lower.contains("examples")
            || path_lower.contains("target")
            || path_lower.contains("build")
            || path_lower.contains("dist")
        {
            penalty = 0.10;
            reason = "Fixtures/Examples/Build penalty";
        }

        if penalty < 1.0 {
            scored.score *= penalty;
            scored.reasons.push(reason.to_string());
        }
    }
}

fn apply_file_coherence_boost(scored_chunks: &mut [ScoredChunk]) {
    let mut counts: HashMap<String, usize> = HashMap::new();
    for scored in scored_chunks.iter() {
        *counts.entry(scored.chunk.file_path.clone()).or_insert(0) += 1;
    }

    for scored in scored_chunks.iter_mut() {
        if let Some(count) = counts.get(&scored.chunk.file_path) {
            if *count > 1 {
                scored.score *= 1.0 + ((*count as f32 - 1.0) * 0.05);
                scored.reasons.push("File coherence boost".to_string());
            }
        }
    }
}

fn apply_concept_boost(scored_chunks: &mut [ScoredChunk], query: &str) {
    let query_tokens: Vec<String> = query.split_whitespace().map(|t| t.to_lowercase()).collect();
    for scored in scored_chunks.iter_mut() {
        if scored.chunk.symbols_defined.is_empty() {
            continue;
        }
        let mut boosted = false;
        for symbol_id in &scored.chunk.symbols_defined {
            let symbol_name = symbol_id.split(':').next_back().unwrap_or(symbol_id);
            let symbol_name_lower = symbol_name.to_lowercase();
            if query_tokens
                .iter()
                .any(|tok| symbol_name_lower.contains(tok))
            {
                boosted = true;
                break;
            }
        }
        if boosted {
            scored.score *= 1.5;
            scored.reasons.push("Concept match boost".to_string());
        }
    }
}

fn apply_generic_symbol_penalty(scored_chunks: &mut [ScoredChunk]) {
    let generic_symbols = ["main", "new", "init", "test", "helper", "util", "config"];
    for scored in scored_chunks.iter_mut() {
        for symbol_id in &scored.chunk.symbols_defined {
            let symbol_name = symbol_id
                .split(':')
                .next_back()
                .unwrap_or(symbol_id)
                .to_lowercase();

            if generic_symbols.contains(&symbol_name.as_str()) {
                scored.score *= 0.2; // Severely suppress boilerplate identifiers
                scored.reasons.push("Generic symbol penalty".to_string());
                break; // Penalty applied once per unified code chunk
            }
        }
    }
}
