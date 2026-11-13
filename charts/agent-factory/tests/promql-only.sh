#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The chart's rules and dashboards run on Prometheus: no MetricsQL-only function in files/ (v4 step 6,
# D21). promtool parses the rules too (monitoring.sh); this also covers the dashboards' queries.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
# MetricsQL's functions that PromQL lacks (docs.victoriametrics.com/metricsql), and its WITH templates.
fns='increase_pure|increases_over_time|decreases_over_time|tfirst_over_time|tlast_over_time|tlast_change_over_time|tmin_over_time|tmax_over_time|lag|lifetime|rollup|rollup_[a-z_]+|range_[a-z_]+|running_[a-z_]+|label_set|label_del|label_keep|label_copy|label_move|label_transform|label_value|label_match|label_mismatch|label_uppercase|label_lowercase|label_map|label_graphite_group|alias|union|limit_offset|keep_last_value|keep_next_value|interpolate|prometheus_buckets|buckets_limit|histogram_share|histogram_avg|histogram_stddev|histogram_stdvar|share_le_over_time|share_gt_over_time|share_eq_over_time|count_le_over_time|count_gt_over_time|count_eq_over_time|count_ne_over_time|sum_le_over_time|sum_gt_over_time|sum_eq_over_time|median_over_time|mode_over_time|distinct_over_time|ascent_over_time|descent_over_time|integrate|rate_over_sum|remove_resets|zscore_over_time|zscore|aggr_over_time|duration_over_time|outlier_iqr_over_time|outliersk|outliers_mad|outliers_iqr|hoeffding_bound_lower|hoeffding_bound_upper|ru|ttf|bitmap_and|bitmap_or|bitmap_xor|sort_by_label_numeric|sort_by_label_numeric_desc|any|bottomk_avg|bottomk_max|bottomk_min|bottomk_median|bottomk_last|topk_avg|topk_max|topk_min|topk_median|topk_last|count_values_over_time|geomean|geomean_over_time|mad|mad_over_time|range_normalize|first_over_time'
re="(^|[^a-zA-Z0-9_])($fns)[[:space:]]*\(|(^|[^a-zA-Z0-9_])WITH[[:space:]]*\("
# self-check: the pattern catches what it must, and leaves PromQL alone
grep -qE "$re" <<<'sum(increase_pure(x[5m]))' || { echo "FAIL: the pattern misses increase_pure" >&2; exit 1; }
! grep -qE "$re" <<<'sum(increase(x[5m])) and max_over_time(timestamp(x)[5m:1m]) and label_replace(x, "a", "$1", "b", "(.*)")' || { echo "FAIL: the pattern flags PromQL" >&2; exit 1; }
hits="$(grep -rnE "$re" files/ || true)"
[[ -z $hits ]] || { echo "FAIL: MetricsQL-only functions in files/ (PromQL only, D21):" >&2; echo "$hits" >&2; exit 1; }
echo "agent-factory PromQL-only check passed"
