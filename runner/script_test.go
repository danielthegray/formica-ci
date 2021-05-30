package runner

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func createScript(path string, content string, t *testing.T) {
	err := ioutil.WriteFile(path, []byte(content), 0755)
	if err != nil {
		t.Fatalf("Failed to create test script")
	}
}
func TestFindScript(t *testing.T) {
	t.Parallel()
	scriptTestFolder, err := ioutil.TempDir(".", "script_test")
	defer os.RemoveAll(scriptTestFolder)
	if err != nil {
		t.Fatalf("Error setting up script test folder")
	}
	_, findErr := FindScript(scriptTestFolder, configUpdate)
	if !findErr.IsNoScriptFoundError() {
		t.Fatalf("Non-existent script did not fail!")
	}
	// a folder with just the script should find it
	createScript(filepath.Join(scriptTestFolder, string(configUpdate)+".sh"), "", t)
	script, findErr := FindScript(scriptTestFolder, configUpdate)
	if findErr != nil {
		t.Fatalf("Searching for an existent script failed!")
	}
	if string(script) != string(configUpdate)+".sh" {
		t.Fatalf("The wrong script was found!")
	}
	// a folder with the script and another one with a similar name should also find it well
	createScript(filepath.Join(scriptTestFolder, string(configUpdate)+"a.sh"), "", t)
	script, findErr = FindScript(scriptTestFolder, configUpdate)
	if findErr != nil {
		t.Logf("Searching for an existent script failed! %s", findErr)
		filesInScriptFolder, _ := ioutil.ReadDir(scriptTestFolder)
		for _, file := range filesInScriptFolder {
			t.Logf(file.Name())
		}
		t.FailNow()
	}
	if string(script) != string(configUpdate)+".sh" {
		t.Fatalf("The wrong script was found!")
	}
	// a script collision should fail
	createScript(filepath.Join(scriptTestFolder, string(configUpdate)+".py"), "", t)
	script, findErr = FindScript(scriptTestFolder, configUpdate)
	if findErr == nil {
		t.Fatalf("A script collision did not cause an error!")
	}
	if !findErr.IsManyScriptsFoundError() {
		t.Fatalf("A script collision was not detected correctly!")
	}
}
