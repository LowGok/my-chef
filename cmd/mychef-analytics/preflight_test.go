package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

type preflightFixture struct {
	root, app, config, evidence, binary, source string
	manifest                                    map[string]any
	records                                     map[string]map[string]any
	paths                                       map[string]string
}

func privateJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func newPreflightFixture(t *testing.T) preflightFixture {
	t.Helper()
	f := preflightFixture{root: t.TempDir(), source: strings.Repeat("a", 40), records: map[string]map[string]any{}, paths: map[string]string{}}
	if err := os.Chmod(f.root, 0700); err != nil {
		t.Fatal(err)
	}
	f.app, f.config, f.evidence, f.binary = filepath.Join(f.root, "app.sqlite"), filepath.Join(f.root, "proposed.json"), filepath.Join(f.root, "evidence.json"), filepath.Join(f.root, "candidate")
	app, err := store.Open(f.app, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err = app.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var binding string
	if err = app.SQL.QueryRow("SELECT binding FROM lifecycle_binding WHERE singleton=1").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	key := filepath.Join(f.root, "backup.key")
	if err = os.WriteFile(key, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	configuration := analytics.Config{NoticeVersion: "fictional-core-v1", NoticeHash: analytics.Digest([]byte(analytics.NoticeText)),
		Assessment: analytics.Assessment{Status: "approved", Basis: "legitimate_interest", Version: "fictional-assessment-v1", Scope: analytics.ScopeDigest(),
			NoticeVersion: "fictional-core-v1", Jurisdiction: "BE-fictional", Audience: "fictional adults", DeviceAccess: "fictional reviewed", ValidUntil: now.Add(2 * time.Hour).Unix()}}
	runtime := analytics.RuntimeFile{Root: filepath.Join(f.root, "analytics"), PrivacyPath: filepath.Join(f.root, "privacy.sqlite"),
		AnchorPath: filepath.Join(f.root, "anchor"), BackupKeyFile: key, Collection: configuration}
	service, err := analytics.Open(t.Context(), app.DB, analytics.OpenOptions{Root: runtime.Root, PrivacyPath: runtime.PrivacyPath,
		AnchorPath: runtime.AnchorPath, BackupKey: bytes.Repeat([]byte{7}, 32), Config: configuration, Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	// The proposed file is distinct from service state. Reading it must never
	// change Enabled, initialize a second service, reconcile or start a worker.
	runtime.Collection.Enabled = true
	configRaw := privateJSON(t, f.config, runtime)
	if err = os.WriteFile(f.binary, []byte("fictional candidate bytes, never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := map[string]any{"sourceCommit": f.source, "environment": "fixture-arch-1", "appBinding": binding,
		"binarySha256": analytics.Digest([]byte("fictional candidate bytes, never executed")), "configSha256": analytics.Digest(configRaw),
		"apiSha256": strings.Repeat("b", 64), "androidArtifactSha256": strings.Repeat("c", 64), "iosArtifactSha256": strings.Repeat("d", 64),
		"noticeVersion": configuration.NoticeVersion, "noticeSha256": configuration.NoticeHash,
		"scopeSha256": analytics.ScopeDigest(), "assessmentVersion": configuration.Assessment.Version}
	identityRaw, _ := json.Marshal(identity)
	evidence := map[string]any{}
	for _, kind := range []string{"controller", "privacy", "providers", "deviceAccess", "nativeAndroid", "nativeIOS", "ci", "storage", "recovery", "implementationReview", "activation"} {
		document := filepath.Join(f.root, kind+"-support.txt")
		if err = os.WriteFile(document, []byte("fictional concluded supporting evidence for "+kind), 0600); err != nil {
			t.Fatal(err)
		}
		record := map[string]any{"schemaVersion": 1, "kind": kind, "status": "passed", "bindingSha256": analytics.Digest(identityRaw),
			"verifiedUnix": now.Add(-time.Minute).Unix(), "expiresUnix": now.Add(time.Hour).Unix(),
			"document": map[string]any{"path": document, "sha256": analytics.Digest([]byte("fictional concluded supporting evidence for " + kind))}}
		if kind == "controller" || kind == "activation" {
			record["status"] = "approved"
		}
		if kind == "nativeAndroid" || kind == "nativeIOS" {
			record["fullJourneys"] = true
			record["spokenAccessibility"] = true
		}
		if kind == "privacy" {
			record["unresolved"] = []string{}
		}
		f.records[kind], f.paths[kind] = record, filepath.Join(f.root, kind+".json")
		raw := privateJSON(t, f.paths[kind], record)
		evidence[kind] = map[string]any{"path": f.paths[kind], "sha256": analytics.Digest(raw)}
	}
	f.manifest = map[string]any{"schemaVersion": 1, "identity": identity, "evidence": evidence}
	privateJSON(t, f.evidence, f.manifest)
	// Keep a genuine WAL open during every preflight. Success must not create,
	// alter or delete its DB/WAL/SHM files or overlook a binding in the WAL.
	if _, err = app.SQL.Exec("PRAGMA wal_autocheckpoint=0; UPDATE analytics_installation SET required=required"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(f.app + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("fixture needs an actual active WAL", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Chmod(f.app+suffix, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f preflightFixture) updateRecord(t *testing.T, kind string) {
	t.Helper()
	raw := privateJSON(t, f.paths[kind], f.records[kind])
	f.manifest["evidence"].(map[string]any)[kind] = map[string]any{"path": f.paths[kind], "sha256": analytics.Digest(raw)}
	privateJSON(t, f.evidence, f.manifest)
}

type preflightFile struct {
	Mode fs.FileMode
	Data string
	Size int64
	Time int64
}

func preflightSnapshot(t *testing.T, root string) map[string]preflightFile {
	t.Helper()
	files := map[string]preflightFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if entry.Type()&os.ModeSymlink != 0 {
			link, e := os.Readlink(path)
			data, err = []byte(link), e
		} else if !entry.IsDir() {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			return err
		}
		files[path] = preflightFile{info.Mode(), analytics.Digest(data), info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (f preflightFixture) check(t *testing.T, ready bool) {
	t.Helper()
	before := preflightSnapshot(t, f.root)
	var out, diagnostic bytes.Buffer
	err := liveCommand(t.Context(), []string{"preflight", "--app", f.app, "--config", f.config, "--evidence", f.evidence,
		"--binary", f.binary, "--source", f.source, "--environment", "fixture-arch-1"}, &out, &diagnostic)
	if !reflect.DeepEqual(before, preflightSnapshot(t, f.root)) {
		t.Fatal("read-only preflight changed inputs, filesystem, database, WAL or SHM")
	}
	if (err == nil) != ready {
		var hint struct {
			Codes []string `json:"codes"`
		}
		_ = json.Unmarshal(out.Bytes(), &hint)
		for _, code := range hint.Codes {
			if regexp.MustCompile(`^[a-z_]{1,60}$`).MatchString(code) {
				t.Log("coarse refusal:", code)
			}
		}
		t.Fatalf("unexpected readiness, ready=%v error=%v", ready, err)
	}
	var result struct {
		Status  string            `json:"status"`
		Codes   []string          `json:"codes"`
		Digests map[string]string `json:"digests"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal("preflight must always return the bounded coarse result", err)
	}
	if ready && (result.Status != "ready" || len(result.Codes) != 0) || !ready && (result.Status != "blocked" || len(result.Codes) == 0) {
		t.Fatal("untruthful preflight result")
	}
	for _, secret := range []string{f.root, "fictional candidate", "fictional concluded", "fictional adults", "BE-fictional", "sub_", "evt_", "Bearer", string(bytes.Repeat([]byte{7}, 32))} {
		if strings.Contains(out.String()+diagnostic.String(), secret) {
			t.Fatal("preflight leaked private inputs")
		}
	}
	for _, code := range result.Codes {
		if !regexp.MustCompile(`^[a-z_]{1,60}$`).MatchString(code) {
			t.Fatal("preflight emitted a non-coarse refusal code")
		}
	}
	for key, value := range result.Digests {
		if !regexp.MustCompile(`^[a-zA-Z]{1,40}$`).MatchString(key) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(value) {
			t.Fatal("preflight emitted private or unbounded evidence fields")
		}
	}
	if strings.Contains(out.String()+diagnostic.String(), f.manifest["identity"].(map[string]any)["appBinding"].(string)) {
		t.Fatal("preflight exposed the private source binding")
	}
}

func TestLivePreflightReadyDoesNotOpenOrMaintainStorage(t *testing.T) {
	newPreflightFixture(t).check(t, true)
}

func TestLivePreflightRefusesAmbiguousDuplicateFields(t *testing.T) {
	f := newPreflightFixture(t)
	raw, err := os.ReadFile(f.evidence)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":0,"schemaVersion":1`), 1)
	if err = os.WriteFile(f.evidence, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.check(t, false)
}

func TestPreflightSQLSnapshotReadsCommittedWALAndRefusesWrites(t *testing.T) {
	f := newPreflightFixture(t)
	writer, err := store.Open(f.app, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	for _, statement := range []string{"CREATE TABLE fixture_preflight_metadata(value INTEGER)", "INSERT INTO fixture_preflight_metadata VALUES(1)", "PRAGMA wal_checkpoint(TRUNCATE)", "UPDATE fixture_preflight_metadata SET value=2"} {
		if _, err = writer.SQL.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	before := preflightSnapshot(t, f.root)
	inputs := preflightInputs{}
	reader, err := inputs.database(t.Context(), f.app)
	if err != nil {
		t.Fatal("snapshot reader unavailable", err)
	}
	var value int
	if err = reader.QueryRow("SELECT value FROM fixture_preflight_metadata").Scan(&value); err != nil || value != 2 {
		_ = reader.Close()
		t.Fatal("read-only reader missed committed WAL metadata", err)
	}
	if _, err = reader.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Exec("UPDATE fixture_preflight_metadata SET value=3"); err == nil {
		_ = reader.Close()
		t.Fatal("read-only snapshot permitted a write")
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !inputs.unchanged() || !reflect.DeepEqual(before, preflightSnapshot(t, f.root)) {
		t.Fatal("snapshot read or refused write modified source files")
	}
}

func TestPreflightDoesNotForgetAnObservedFileThatDisappears(t *testing.T) {
	f := newPreflightFixture(t)
	inputs := preflightInputs{}
	if err := inputs.observe(f.app+"-shm", 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.app + "-shm"); err != nil {
		t.Fatal(err)
	}
	reader, err := inputs.database(t.Context(), f.app)
	if err == nil {
		_ = reader.Close()
		t.Fatal("disappearing witnessed file was silently treated as absent")
	}
	if inputs.unchanged() {
		t.Fatal("changed input witness was overwritten")
	}
}

func TestPreflightRefusesExpiryOrBackwardClockDuringInspection(t *testing.T) {
	for _, delta := range []time.Duration{3 * time.Hour, -time.Hour} {
		t.Run(delta.String(), func(t *testing.T) {
			f := newPreflightFixture(t)
			before := preflightSnapshot(t, f.root)
			start := time.Now().UTC()
			calls := 0
			clock := func() time.Time {
				calls++
				if calls == 1 {
					return start
				}
				return start.Add(delta)
			}
			code := checkLivePreflight(t.Context(), f.app, f.config, f.evidence, f.binary, f.source, "fixture-arch-1", preflightInputs{}, map[string]string{}, &preflightValidity{clock: clock})
			if code == "" {
				t.Fatal("inspection returned ready after evidence expiry or clock rollback")
			}
			if !reflect.DeepEqual(before, preflightSnapshot(t, f.root)) {
				t.Fatal("clock refusal changed source files")
			}
		})
	}
}

func TestLivePreflightRefusesIncompleteStaleOrMismatchedEvidenceWithoutWrites(t *testing.T) {
	cases := map[string]func(*testing.T, preflightFixture){
		"missing activation": func(t *testing.T, f preflightFixture) {
			delete(f.manifest["evidence"].(map[string]any), "activation")
			privateJSON(t, f.evidence, f.manifest)
		},
		"pending controller": func(t *testing.T, f preflightFixture) {
			f.records["controller"]["status"] = "pending"
			f.updateRecord(t, "controller")
		},
		"expired approval": func(t *testing.T, f preflightFixture) {
			f.records["activation"]["expiresUnix"] = time.Now().Unix() - 1
			f.updateRecord(t, "activation")
		},
		"future approval": func(t *testing.T, f preflightFixture) {
			f.records["activation"]["verifiedUnix"] = time.Now().Unix() + 3600
			f.updateRecord(t, "activation")
		},
		"assessment hash drift": func(t *testing.T, f preflightFixture) {
			_ = os.WriteFile(f.paths["controller"], []byte("changed private conclusion"), 0600)
		},
		"supporting document drift": func(t *testing.T, f preflightFixture) {
			_ = os.WriteFile(filepath.Join(f.root, "providers-support.txt"), []byte("changed private provider settings"), 0600)
		},
		"provider incomplete": func(t *testing.T, f preflightFixture) {
			f.records["providers"]["status"] = "pending"
			f.updateRecord(t, "providers")
		},
		"native incomplete": func(t *testing.T, f preflightFixture) {
			f.records["nativeIOS"]["fullJourneys"] = false
			f.updateRecord(t, "nativeIOS")
		},
		"native speech missing": func(t *testing.T, f preflightFixture) {
			delete(f.records["nativeAndroid"], "spokenAccessibility")
			f.updateRecord(t, "nativeAndroid")
		},
		"privacy unresolved": func(t *testing.T, f preflightFixture) {
			f.records["privacy"]["unresolved"] = []string{"provider review pending"}
			f.updateRecord(t, "privacy")
		},
		"binding mismatch": func(t *testing.T, f preflightFixture) {
			f.records["ci"]["bindingSha256"] = strings.Repeat("0", 64)
			f.updateRecord(t, "ci")
		},
		"actual app binding changed in WAL": func(t *testing.T, f preflightFixture) {
			app, err := store.Open(f.app, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			// Deliberately corrupt a disposable copy; normal product writes
			// correctly refuse to change this immutable binding.
			if _, err = app.SQL.Exec("DROP TRIGGER lifecycle_binding_immutable"); err != nil {
				t.Fatal(err)
			}
			if _, err = app.SQL.Exec("UPDATE lifecycle_binding SET binding=? WHERE singleton=1", strings.Repeat("f", 32)); err != nil {
				t.Fatal(err)
			}
		},
		"binary drift": func(t *testing.T, f preflightFixture) {
			_ = os.WriteFile(f.binary, []byte("different unapproved candidate"), 0600)
		},
		"config drift": func(t *testing.T, f preflightFixture) {
			var value map[string]any
			raw, _ := os.ReadFile(f.config)
			_ = json.Unmarshal(raw, &value)
			value["collection"].(map[string]any)["Enabled"] = false
			privateJSON(t, f.config, value)
		},
		"source drift": func(t *testing.T, f preflightFixture) {
			f.manifest["identity"].(map[string]any)["sourceCommit"] = strings.Repeat("b", 40)
			privateJSON(t, f.evidence, f.manifest)
		},
		"private payload": func(t *testing.T, f preflightFixture) {
			f.records["controller"]["email"] = "private@example.test"
			f.updateRecord(t, "controller")
		},
		"public evidence permissions": func(t *testing.T, f preflightFixture) { _ = os.Chmod(f.paths["ci"], 0644) },
		"symlinked evidence": func(t *testing.T, f preflightFixture) {
			p := f.paths["recovery"]
			_ = os.Rename(p, p+".real")
			if err := os.Symlink(p+".real", p); err != nil {
				t.Fatal(err)
			}
		},
		"relative path": func(t *testing.T, f preflightFixture) {
			f.manifest["evidence"].(map[string]any)["storage"].(map[string]any)["path"] = "storage.json"
			privateJSON(t, f.evidence, f.manifest)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { f := newPreflightFixture(t); mutate(t, f); f.check(t, false) })
	}
}
