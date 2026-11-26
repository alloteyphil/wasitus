// Package correlate scores the changes that happened before a symptom, so the
// verdict stage can tell whether a recent change is a likely cause.
//
// Every event of kind change at most Weights.Lookback (default 60m) before the
// symptom's start gets a score in [0, 1]:
//
//	score = 0.5 * exp(-Δt / 15m)   // time proximity: 5m before ≈ 0.72, 60m before ≈ 0.02
//	      + 0.3 * same_service     // 1 if change and symptom name the same service (case-insensitive)
//	      + 0.2 * type_weight      // from the change's "change_type" attribute
//
// Type weights: deploy 1.0, release 0.9, config 0.8, infra 0.8, merge 0.4,
// docs 0.1, test 0.1; a missing or unknown type weighs 0.5. All of these are
// the defaults from DefaultWeights and can be replaced from config.
//
// Each Candidate keeps the three unweighted factors and one readable reason
// per factor, such as "17m before the first symptom", "same service:
// orders-service" and "type deploy (weight 1.0)", so a reader can check any
// score by hand. Ranking is deterministic: score, then time to the symptom,
// then change ID. See docs/ARCHITECTURE.md, section 5.
package correlate
