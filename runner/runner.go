package runner

import (
	"fmt"
	"log"
	"os"
)

const configFolder = "formica_conf"
const configInitPrefix = "config_init"

func fetchConfigFolder() error {
	currentDir, err := os.Getwd()
	if err != nil {
		log.Println("Error when getting the current dir")
		return err
	}
	scriptName, err := FindScript(currentDir, configInitPrefix)
	if err != nil {
		log.Println("Error when finding the script")
		return err
	}
	_, err = ExecuteScript(currentDir, scriptName)
	if err != nil {
		log.Println("Error when executing the script")
		return err
	}
	return nil
}

func ensureConfigFolderPresent() error {
	confStat, err := os.Stat(configFolder)
	if err != nil {
		if os.IsNotExist(err) {
			configErr := fetchConfigFolder()
			if configErr != nil {
				return fmt.Errorf("error when fetching configuration folder: %s", configErr.Error())
			}
		} else {
			return fmt.Errorf("unexpected error when checking for permissions of configuration folder '%s': %s", configFolder, err.Error())
		}
	}
	if confStat != nil && !confStat.IsDir() {
		return fmt.Errorf("configuration folder path '%s' was already occupied, and was not a folder", configFolder)
	}

	return nil
}

// Init initializes the job runner
func Init() {
	err := ensureConfigFolderPresent()
	if err != nil {
		log.Fatalf("%s", err)
	}

}
