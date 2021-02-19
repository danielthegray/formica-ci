package runner

import (
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

const configFolder string = "formica_conf"

const configInit Prefix = "config_init"
const configRemoteVersion Prefix = "config_version_remote"
const configLocalVersion Prefix = "config_version_local"
const configUpdate Prefix = "config_update"

func currentDir() string {
	log.Info("Initializing configuration folder (loading from SCM)")
	currentDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("Could not fetch current dir: %s", err)
	}
	return currentDir
}

func fetchConfigFolder(runPath string) error {
	_, err := FindAndExecute(runPath, configInit)
	if err != nil {
		log.Errorf("Error when fetching the configuration folder! %s", err.Error())
		return err
	}
	return nil
}

func fetchConfigIfNotPresent(configParent string) error {
	fullConfigPath := filepath.Join(configParent, configFolder)
	confStat, err := os.Stat(fullConfigPath)
	if err == nil {
		if !confStat.IsDir() {
			return fmt.Errorf("configuration folder path '%s' was already occupied, and was not a folder", configFolder)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("unexpected error when checking for permissions of configuration folder '%s': %s", configFolder, err.Error())
	}
	configErr := fetchConfigFolder(configParent)
	if configErr != nil {
		return fmt.Errorf("error when fetching configuration folder: %s", configErr.Error())
	}
	// verify that the config folder has been created by the config_init script
	confStat, err = os.Stat(fullConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("The %s script did not create the configuration folder correctly! Error: %s", configInit, err.Error())
		}
	}
	return nil
}

func updateConfig() error {
	log.Info("Updating the configuration folder")
	updateScriptFile, findErr := FindScript(configFolder, configUpdate)
	if findErr != nil {
		return fmt.Errorf("error while finding update script in configuration: %s", findErr.Error())
	}
	localVersion, err := FindAndExecute(configFolder, configLocalVersion)
	if err != nil {
		return fmt.Errorf("error while checking local version of configuration: %s", err.Error())
	}
	log.WithField("local_version", localVersion).Info("Local config version")
	remoteVersion, err := FindAndExecute(configFolder, configRemoteVersion)
	if err != nil {
		return fmt.Errorf("error while checking remote version of configuration: %s", err.Error())
	}
	log.WithField("remote_version", localVersion).Info("Upstream config version")
	if localVersion != remoteVersion {
		log.Info("Config out-of-date: updating config")
		_, updateErr := OutputOfExecuting(configFolder, updateScriptFile)
		if updateErr != nil {
			return fmt.Errorf("error while updating configuration: %s", err.Error())
		}
	}
	// after updating the configuration, we reload the jobs that exist
	return reloadJobDefs()
}
