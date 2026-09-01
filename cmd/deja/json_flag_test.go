package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vshulcz/deja-vu/internal/index"
	"github.com/vshulcz/deja-vu/internal/jsonout"
)

// `deja fix --json` was refused and `deja friction --json` was accepted, ignored
// and answered in prose (#1932). The second is the worse of the two: a script
// reading it gets no error and no JSON either.
//
// friction's silent-ignore was closed separately by its unknown-flag guard, which
// turned it into a refusal. These pin the answer both commands now give.

func TestFrictionJSONIsAVersionedEnvelope(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions)
	var buf bytes.Buffer
	if err := runFriction(index.DefaultDir(), []string{"--json"}, &buf); err != nil {
		t.Fatal(err)
	}
	var got frictionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.SchemaVersion != jsonout.Version {
		t.Fatalf("schema_version = %d, want %d", got.SchemaVersion, jsonout.Version)
	}
	if len(got.Rows) == 0 {
		t.Fatalf("no rows:\n%s", buf.String())
	}
	if got.Rows[0].Sessions != index.FrictionMinSessions {
		t.Fatalf("sessions = %d, want %d", got.Rows[0].Sessions, index.FrictionMinSessions)
	}
	// The threshold a row had to clear. Without it a consumer cannot tell an
	// empty result meaning "nothing recurs" from one meaning "nothing twice".
	if got.MinSessions != index.FrictionMinSessions {
		t.Fatalf("min_sessions = %d, want %d", got.MinSessions, index.FrictionMinSessions)
	}
}

// The empty case must be the SAME shape, not a different one. The prose path
// answers "nothing recurring" five different ways depending on what the store
// holds and which rule emptied it; a script cannot branch on prose.
func TestFrictionJSONKeepsItsShapeWhenEmpty(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions-1)
	var buf bytes.Buffer
	if err := runFriction(index.DefaultDir(), []string{"--json"}, &buf); err != nil {
		t.Fatal(err)
	}
	_ = root
	var got frictionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Rows == nil {
		t.Fatal("rows is null; an empty result must still be an array")
	}
	if len(got.Rows) != 0 || got.Total != 0 {
		t.Fatalf("want an empty result, got %+v", got)
	}
}

// `--limit` caps the rows; `total` and `truncated` are how a caller learns the
// cap hid something. Reading len(rows) answers neither.
func TestFrictionJSONReportsWhatTheLimitHid(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions)
	_ = root
	var buf bytes.Buffer
	if err := runFriction(index.DefaultDir(), []string{"--json", "--limit", "1"}, &buf); err != nil {
		t.Fatal(err)
	}
	var got frictionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if len(got.Rows) > 1 {
		t.Fatalf("--limit 1 returned %d rows", len(got.Rows))
	}
	if got.Total < len(got.Rows) {
		t.Fatalf("total %d is below the rows returned %d", got.Total, len(got.Rows))
	}
	if got.Truncated != (got.Total > len(got.Rows)) {
		t.Fatalf("truncated=%v disagrees with total=%d rows=%d", got.Truncated, got.Total, len(got.Rows))
	}
}

// The flag was rejected outright, and the comment in fix.go records an earlier
// bug where it was swallowed into the searched text instead.
func TestFixAcceptsJSON(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions)
	var buf bytes.Buffer
	if err := runFix(index.DefaultDir(), []string{"some error text", "--json"}, &buf); err != nil {
		t.Fatalf("fix --json refused: %v", err)
	}
	var got fixJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.SchemaVersion != jsonout.Version {
		t.Fatalf("schema_version = %d, want %d", got.SchemaVersion, jsonout.Version)
	}
	if got.Fixes == nil {
		t.Fatal("fixes is null; an empty result must still be an array")
	}
}

// --json must not become the error text. That is the bug fix.go's comment
// describes, and the reason the flag was refused rather than parsed.
func TestFixJSONIsNotSearchedFor(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions)
	_ = root
	var buf bytes.Buffer
	if err := runFix(index.DefaultDir(), []string{"--json"}, &buf); err == nil {
		t.Fatal("fix --json with no error text was accepted; the flag became the query")
	}
}

// Everything else still refuses. Answering one flag must not reopen the
// dropped-on-the-floor behaviour the unknown-flag guard closed.
func TestBothStillRefuseAnUnknownFlag(t *testing.T) {
	root := frictionEnv(t)
	writeFrictionCorpus(t, root, index.FrictionMinSessions)
	_ = root
	var buf bytes.Buffer
	if err := runFriction(index.DefaultDir(), []string{"--jsonn"}, &buf); err == nil {
		t.Fatal("friction accepted --jsonn")
	}
	if err := runFix(index.DefaultDir(), []string{"err", "--jsonn"}, &buf); err == nil {
		t.Fatal("fix accepted --jsonn")
	}
}
