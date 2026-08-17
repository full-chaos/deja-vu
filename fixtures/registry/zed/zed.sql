-- Zed keeps every agent thread in one SQLite store. This fixture carries both
-- storage encodings a live store mixes, because deja indexes history written
-- before it was installed and Zed rewrites a thread's encoding only on save.
--
-- Row 1 (data_type 'json') is a pre-compression thread in the agent-1 shape:
-- `summary` rather than `title`, and messages as {role, segments}.
-- Row 2 (data_type 'zstd') is what Zed writes today: a zstd frame holding the
-- flattened SerializedThread of version 0.3.0, whose messages are Rust's
-- externally tagged Message enum.
--
-- The blob is opaque on purpose — it is a real frame, not a stand-in. It
-- decompresses to exactly:
--
-- {"version":"0.3.0","title":"read the zed thread store","updated_at":"2026-07-19T09:00:02Z","messages":[{"User":{"id":"registry-user-1","content":[{"Text":"where does zed keep its agent threads?"}]}},{"Agent":{"content":[{"Thinking":{"text":"reasoning is not indexed","signature":null}},{"Text":"in threads.db under the data dir"}],"tool_results":{},"reasoning_details":null}},"Resume"]}
--
-- TestRegistryFixtureBlobMatchesItsDocumentedPlaintext pins that, so the two
-- cannot drift apart. Regenerate with:
--
--   printf '%s' '<the json above>' | zstd -q -3 -c | od -An -v -tx1 | tr -d ' \n'
create table threads (
    id text primary key,
    summary text not null,
    updated_at text not null,
    data_type text not null,
    data blob not null,
    parent_id text,
    folder_paths text,
    folder_paths_order text,
    created_at text
);
insert into threads (id, summary, updated_at, data_type, data, folder_paths, folder_paths_order, created_at)
values (
    'registry-zed-legacy',
    'legacy zed thread',
    '2026-07-19T08:00:02+00:00',
    'json',
    '{"version":"0.2.0","summary":"legacy zed thread","updated_at":"2026-07-19T08:00:02Z","messages":[{"id":1,"role":"user","segments":[{"type":"text","text":"does the pre-compression thread shape still load?"}]},{"id":2,"role":"assistant","segments":[{"type":"thinking","text":"reasoning is not indexed"},{"type":"text","text":"agent-1 threads still parse"}]}]}',
    '/workspace/registry-demo',
    '0',
    '2026-07-19T08:00:00+00:00'
);
insert into threads (id, summary, updated_at, data_type, data, folder_paths, folder_paths_order, created_at)
values (
    'registry-zed-modern',
    'read the zed thread store',
    '2026-07-19T09:00:02+00:00',
    'zstd',
    x'28b52ffd6483003d080086913522508b9a03e868169bd8439416496a1b321994fb4d86cc1a41dfdb85974f23d38c60022b002c002a008b8432a014018552787999a37d5993344efe73ab7f407f2c6aeccfcb6716d7c90a1180505e4e87face6027aea1bf3f1a26676c7ed32d3839a1bc6ba338bc555c1ada597ed7033b09cbfa85a93ee26404855a4aad8562042c175427b74fde12d1ba448a68494ca7ec6f974448e2c3592eb0939ea7f1984037bf30a48b48f243f401004fc38f347a032b836cdf1ce97196759946ff1c8c73ebd478fcb021b6acbdb76c90424324324c93cb7fcd0611005d142e1ceb3ab61d3a1e3ecc0569214a1646ec04a6c13272983779a70826280b313abd1ebad04ca6d82f284f7c2af5cc',
    '/workspace/registry-demo',
    '0',
    '2026-07-19T09:00:00+00:00'
);
