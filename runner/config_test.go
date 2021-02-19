package runner

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateInit(t *testing.T) {
	updateTestFolder, err := ioutil.TempDir(".", "update_test")
	defer os.RemoveAll(updateTestFolder)
	if err != nil {
		t.Fatalf("Error setting up update test folder")
	}
	err = fetchConfigIfNotPresent(updateTestFolder)
	if err == nil {
		t.Fatalf("Initializing without a config_init did not fail!")
	}
	badConfigGen := "#!/bin/sh"
	ioutil.WriteFile(filepath.Join(updateTestFolder, string(configInit)+".sh"), []byte(badConfigGen), 0755)
	err = fetchConfigIfNotPresent(updateTestFolder)
	if err == nil {
		t.Fatalf("Initializing with a bad config_init (which did not create a config folder) did not fail!")
	}
	emptyConfigGen := `#!/bin/sh
	mkdir formica_conf`
	ioutil.WriteFile(filepath.Join(updateTestFolder, string(configInit)+".sh"), []byte(emptyConfigGen), 0755)
	err = fetchConfigIfNotPresent(updateTestFolder)
	if err != nil {
		t.Fatalf("Initializing with a basic config_init failed!")
	}
}
