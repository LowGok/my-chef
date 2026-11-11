package httpapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRecipeImportFixtureCannotReuseExternalWorkerOrLifecycleState(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	program := `import os,sys
sys.path.insert(0,os.path.join(sys.argv[1],"scripts"))
from dev_server import configure
value=configure({"MYCHEF_IMPORT_WORKER_SOCKET":"/outside/socket","MYCHEF_IMPORT_CHANNEL_KEY_FILE":"/outside/key","MYCHEF_IMPORT_TEMP_DIR":"/outside/spool","MYCHEF_LIFECYCLE_DB":"/outside/lifecycle"},fixture=sys.argv[2])
if any(k.startswith("MYCHEF_IMPORT_") for k in value) or value.get("MYCHEF_LIFECYCLE_DB")!=os.path.join(sys.argv[2],"mychef.db.lifecycle"):sys.exit(7)
`
	command := exec.Command("python3", "-c", program, root, t.TempDir())
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1"}
	if err := command.Run(); err != nil {
		t.Fatal("synthetic fixture can inherit external import/lifecycle state", err)
	}
}
