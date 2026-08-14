#!/usr/bin/env python3
"""Deterministic corpus generator for upstream issue #492 (CJK bigram build
cost re-measurement).

Produces two arms of "claude" harness JSONL transcripts, laid out exactly as
internal/sources/claude.go expects them under DEJA_CLAUDE_ROOT:

    <out>/store-ja/claude-root/<project>/<session>.jsonl
    <out>/store-en/claude-root/<project>/<session>.jsonl

Shape learned from fixtures/registry/claude-code/session.jsonl and
fixtures/synthetic/claude/project/session.jsonl, and cross-checked
against the parser in internal/sources/claude_decode.go:

    {"type": "user"|"assistant",
     "sessionId": "<session id>",
     "timestamp": "<RFC3339>",
     "message": {"role": "user"|"assistant",
                 "content": [{"type": "text", "text": "<body>"}]}}

Only `type` in {"user", "assistant"} lines are counted by the parser
(claude_decode.go:56-58), sessionId overrides the filename-derived id
(claude_decode.go:59-61), timestamp accepts RFC3339 or epoch via
claudeTime/parseTimeAny, and message.content is read by claudeTextKind
(claude_decode.go:154-206) as either a plain string or a list of blocks with
a "text" field. Project directory names are NOT expected to be dash-encoded
absolute paths here on purpose: resolveEncodedPath (claude.go:195-239) only
recurses into filesystem probing when the base name starts with "-", and a
plain name like "proj0007" short-circuits that path immediately
(claude.go:201-204), keeping this benchmark's timing free of unrelated
os.Stat noise from project-name decoding.

Determinism: every byte written here is a function of --seed and --sessions.
No wall-clock read; timestamps are offsets from a fixed 2026-01-01T00:00:00Z
base.
"""
import argparse
import json
import os
import random
import shutil
import sys
from datetime import datetime, timedelta, timezone

BASE_TIME = datetime(2026, 1, 1, 0, 0, 0, tzinfo=timezone.utc)

# Message-count-per-session distribution (user/assistant alternating pairs).
# Weighted so the mean is 3.2 messages/session -> 5000 sessions ~= 16000
# messages, matching the harness's "5000 sessions / ~16000 messages" target.
SESSION_MSG_CHOICES = [2, 4, 6]
SESSION_MSG_WEIGHTS = [0.55, 0.30, 0.15]

MSG_CHAR_MIN = 200
MSG_CHAR_MAX = 600

# --- Japanese vocabulary -----------------------------------------------
# Deliberately mixes kanji compounds, hiragana verb/particle chains and
# katakana loanwords so generated CJK runs look like real dev-chat text:
# unicode.Han + Hiragana + Katakana runs broken only at ASCII punctuation
# ("。" "、" are not in those unicode categories either, which matches how
# real Japanese sentences already break bigram runs at sentence boundaries).
JA_NOUNS = [
    "実装", "設計", "検証", "性能", "計測", "処理", "速度", "品質", "機能", "修正",
    "変更", "確認", "対応", "問題", "原因", "分析", "結果", "状況", "環境", "構成",
    "仕様", "手順", "手法", "方針", "指標", "閾値", "上限", "下限", "範囲", "条件",
    "権限", "設定", "初期化", "終了", "監視", "記録", "履歴", "履歴管理", "統計",
    "文字列", "分割", "統合", "並列", "直列", "同期", "非同期", "排他", "競合",
    "再現", "回帰", "検出", "抽出", "解析", "推定", "予測", "分布", "偏差", "誤差",
    "利用者", "開発者", "運用者", "保守", "移行", "互換", "後方互換", "前方互換",
    "索引", "辞書", "語彙", "頻度", "重複", "断片", "連結", "分節", "境界", "走査",
    "実行時間", "待機時間", "応答時間", "処理時間", "経過時間", "計測結果", "測定値",
]
JA_VERB_PHRASES = [
    "確認します", "確認しました", "実行します", "実行しました", "検証します", "検証しました",
    "対応します", "対応しました", "修正します", "修正しました", "計測します", "計測しました",
    "分析します", "分析しました", "調査します", "調査しました", "反映します", "反映しました",
    "更新します", "更新しました", "削除します", "削除しました", "追加します", "追加しました",
    "統合します", "統合しました", "分割します", "分割しました", "同期します", "同期しました",
    "再現できました", "再現できませんでした", "解消しました", "改善しました", "悪化しました",
]
JA_KATAKANA = [
    "システム", "データ", "インデックス", "パフォーマンス", "テスト", "ユーザー", "サーバー",
    "ファイル", "プロセス", "キャッシュ", "メモリ", "スレッド", "ビルド", "デバッグ", "ログ",
    "クエリ", "トークン", "バイグラム", "パイプライン", "バッファ", "ストリーム", "レイテンシ",
    "スループット", "ベンチマーク", "リグレッション", "コミット", "ブランチ", "リポジトリ",
]
JA_PARTICLES = ["を", "に", "は", "が", "で", "と", "の", "から", "まで", "より", "へ"]
JA_CONNECTORS = ["また、", "さらに、", "一方、", "そのため、", "したがって、", "次に、", "最後に、", "なお、"]


def build_ja_sentence(rng: random.Random) -> str:
    parts = []
    if rng.random() < 0.35:
        parts.append(rng.choice(JA_CONNECTORS))
    n_clauses = rng.randint(1, 3)
    for i in range(n_clauses):
        noun = rng.choice(JA_NOUNS) if rng.random() < 0.6 else rng.choice(JA_KATAKANA)
        particle = rng.choice(JA_PARTICLES)
        parts.append(noun + particle)
    parts.append(rng.choice(JA_VERB_PHRASES))
    return "".join(parts) + "。"


def build_ja_message(rng: random.Random) -> str:
    # Accumulate whole sentences until the target is reached; the joined
    # string may overshoot the target by up to one sentence's length, which
    # is fine for a "200-600 chars, roughly" corpus and keeps every sentence
    # (and therefore every CJK run) intact rather than truncated mid-run.
    target = rng.randint(MSG_CHAR_MIN, MSG_CHAR_MAX)
    out = []
    total = 0
    while total < target:
        s = build_ja_sentence(rng)
        out.append(s)
        total += len(s)
    return "".join(out)


# --- English vocabulary (control arm) -----------------------------------
EN_NOUNS = [
    "implementation", "design", "verification", "performance", "measurement",
    "processing", "speed", "quality", "feature", "fix", "change", "confirmation",
    "response", "issue", "cause", "analysis", "result", "status", "environment",
    "configuration", "specification", "procedure", "method", "policy", "metric",
    "threshold", "upper bound", "lower bound", "range", "condition", "permission",
    "setting", "initialization", "shutdown", "monitoring", "record", "history",
    "statistics", "string", "split", "merge", "parallelism", "serialization",
    "synchronization", "contention", "reproduction", "regression", "detection",
    "extraction", "estimation", "prediction", "distribution", "deviation", "error",
    "user", "developer", "operator", "maintenance", "migration", "compatibility",
    "index", "dictionary", "vocabulary", "frequency", "duplication", "fragment",
    "concatenation", "segment", "boundary", "scan", "runtime", "latency", "elapsed time",
]
EN_VERB_PHRASES = [
    "will confirm this", "confirmed this", "will run this", "ran this",
    "will verify this", "verified this", "will address this", "addressed this",
    "will fix this", "fixed this", "will measure this", "measured this",
    "will analyze this", "analyzed this", "will investigate this", "investigated this",
    "will apply this", "applied this", "will update this", "updated this",
    "will remove this", "removed this", "will add this", "added this",
    "will merge this", "merged this", "will split this", "split this",
    "was reproducible", "was not reproducible", "was resolved", "improved", "regressed",
]
EN_CONNECTORS = ["Also,", "Furthermore,", "On the other hand,", "Therefore,", "Next,", "Finally,", "Note that,"]


def build_en_sentence(rng: random.Random) -> str:
    parts = []
    if rng.random() < 0.35:
        parts.append(rng.choice(EN_CONNECTORS))
    n_clauses = rng.randint(1, 3)
    clause_words = []
    for i in range(n_clauses):
        clause_words.append("the " + rng.choice(EN_NOUNS))
    parts.append(" and ".join(clause_words))
    parts.append(rng.choice(EN_VERB_PHRASES))
    return " ".join(parts) + "."


def build_en_message(rng: random.Random) -> str:
    target = rng.randint(MSG_CHAR_MIN, MSG_CHAR_MAX)
    out = []
    total = 0
    while total < target:
        s = build_en_sentence(rng)
        out.append(s)
        total += len(s) + 1
    return " ".join(out)


def session_message_count(rng: random.Random) -> int:
    return rng.choices(SESSION_MSG_CHOICES, weights=SESSION_MSG_WEIGHTS, k=1)[0]


def gen_arm(out_root: str, arm: str, sessions: int, projects: int, seed: int, build_message):
    rng = random.Random(seed)
    claude_root = os.path.join(out_root, f"store-{arm}", "claude-root")
    if os.path.isdir(claude_root):
        shutil.rmtree(claude_root)
    os.makedirs(claude_root, exist_ok=True)

    total_messages = 0
    t = BASE_TIME
    for i in range(sessions):
        project = f"proj{(i % projects):04d}"
        proj_dir = os.path.join(claude_root, project)
        os.makedirs(proj_dir, exist_ok=True)
        session_id = f"{arm}-session-{i:05d}"
        n_msgs = session_message_count(rng)
        t = t + timedelta(seconds=37)
        lines = []
        for m in range(n_msgs):
            role = "user" if m % 2 == 0 else "assistant"
            body = build_message(rng)
            t = t + timedelta(seconds=rng.randint(3, 45))
            line = {
                "type": role,
                "sessionId": session_id,
                "cwd": f"C:\\fakehome\\projects\\{project}",
                "timestamp": t.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "message": {
                    "role": role,
                    "content": [{"type": "text", "text": body}],
                },
            }
            lines.append(json.dumps(line, ensure_ascii=False))
        total_messages += n_msgs
        path = os.path.join(proj_dir, f"{session_id}.jsonl")
        with open(path, "w", encoding="utf-8", newline="\n") as f:
            f.write("\n".join(lines) + "\n")
    return total_messages


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--sessions", type=int, default=5000, help="sessions per arm")
    ap.add_argument("--projects", type=int, default=50, help="distinct project dirs per arm")
    ap.add_argument("--seed", type=int, default=492, help="base seed (ja uses seed, en uses seed+1)")
    ap.add_argument("--out", default=os.path.dirname(os.path.abspath(__file__)), help="output root (bench492)")
    args = ap.parse_args()

    ja_total = gen_arm(args.out, "ja", args.sessions, args.projects, args.seed, build_ja_message)
    en_total = gen_arm(args.out, "en", args.sessions, args.projects, args.seed + 1, build_en_message)

    print(f"ja: {args.sessions} sessions, {ja_total} messages -> {os.path.join(args.out, 'store-ja', 'claude-root')}")
    print(f"en: {args.sessions} sessions, {en_total} messages -> {os.path.join(args.out, 'store-en', 'claude-root')}")


if __name__ == "__main__":
    sys.exit(main())
