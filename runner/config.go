package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

const configFolder string = "formica_conf"

const configInit Prefix = "config_init"
const configRemoteVersion Prefix = "config_version_remote"
const configLocalVersion Prefix = "config_version_local"
const configUpdate Prefix = "config_update"

type jobDef struct {
	jobName       string
	jobConfigPath string
	branchName    string
}

type versionChecker struct {
	jobFolder string
	script    FormicaScript
}

type jobDefinitions struct {
	allJobs                 []jobDef
	singleVersionCheckers   []versionChecker
	branchedVersionCheckers []versionChecker
}

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
	_, err = os.Stat(fullConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("the %s script did not create the configuration folder correctly! Error: %s", configInit, err.Error())
		}
	}
	return nil
}

func updateConfig() (*jobDefinitions, error) {
	log.Info("Updating the configuration folder")
	updateScriptFile, findErr := FindScript(configFolder, configUpdate)
	if findErr != nil {
		return nil, fmt.Errorf("error while finding update script in configuration: %s", findErr.Error())
	}
	localVersion, err := FindAndExecute(configFolder, configLocalVersion)
	if err != nil {
		return nil, fmt.Errorf("error while checking local version of configuration: %s", err.Error())
	}
	log.WithField("local_version", localVersion).Info("Local config version")
	remoteVersion, err := FindAndExecute(configFolder, configRemoteVersion)
	if err != nil {
		return nil, fmt.Errorf("error while checking remote version of configuration: %s", err.Error())
	}
	log.WithField("remote_version", remoteVersion).Info("Upstream config version")
	if localVersion != remoteVersion {
		log.Info("Config out-of-date: updating config")
		_, updateErr := OutputOfExecuting(configFolder, updateScriptFile)
		if updateErr != nil {
			return nil, fmt.Errorf("error while updating configuration: %s", updateErr.Error())
		}
	}
	// after updating the configuration, we reload the jobs that exist
	return reloadJobDefs()
}

func firstLevelUnderConfig() ([]string, error) {
	foldersToCheck := []string{}
	foldersUnderConfig, err := os.ReadDir(configFolder)
	if err != nil {
		return nil, err
	}
	for _, parentJobFolder := range foldersUnderConfig {
		if !parentJobFolder.IsDir() || strings.HasPrefix(parentJobFolder.Name(), ".") {
			continue
		}
		log.WithField("job_folder", parentJobFolder.Name()).Debug("Found job at first level under config")
		foldersToCheck = append(foldersToCheck, filepath.Join(configFolder, parentJobFolder.Name()))
	}
	for _, jobFolder := range foldersToCheck {
		log.WithField("job_folder", jobFolder).Debug("root job")
	}
	return foldersToCheck, nil
}

func reloadJobDefs() (*jobDefinitions, error) {
	log.Info("Reloading all job definitions from config folder")
	jobDefs := jobDefinitions{
		allJobs:                 nil,
		singleVersionCheckers:   nil,
		branchedVersionCheckers: nil,
	}

	// first we need to find all the branches & versions
	foldersToCheck, err := firstLevelUnderConfig()
	if err != nil {
		return nil, err
	}
	branchesByJob := make(map[string][]string)
	for len(foldersToCheck) > 0 {
		checkedFolder := foldersToCheck[len(foldersToCheck)-1]
		foldersToCheck = foldersToCheck[:len(foldersToCheck)-1]
		log.WithField("job_folder", checkedFolder).Debug("Traversed folder for branch definitions")

		foldersUnderJob, err := os.ReadDir(checkedFolder)
		if err != nil {
			return nil, err
		}
		for _, subFolder := range foldersUnderJob {
			if !subFolder.IsDir() || strings.HasPrefix(subFolder.Name(), ".") {
				continue
			}
			fullJobPath := filepath.Join(checkedFolder, subFolder.Name())
			//jobPathRelative := strings.SplitN(fullpath, string(os.PathSeparator), 2)[1]
			foldersToCheck = append(foldersToCheck, fullJobPath)

			if branchScript, findErr := FindScript(fullJobPath, branchedVersionCheck); findErr == nil {
				log.WithField("job_def_path", fullJobPath).Info("Found branch definition")
				err := validateNoNestedBranches(&branchesByJob, fullJobPath)
				if err != nil {
					return nil, err
				}
				branchList, branchErr := listBranchesOfJob(fullJobPath)
				if branchErr != nil {
					return nil, fmt.Errorf("error while checking for branches of %s: %s", fullJobPath, branchErr.Error())
				}
				jobDefs.branchedVersionCheckers = append(jobDefs.branchedVersionCheckers, versionChecker{
					jobFolder: fullJobPath,
					script:    branchScript,
				})
				branchesByJob[fullJobPath] = branchList
			}
			if versionScript, findErr := FindScript(fullJobPath, singleVersionCheck); findErr == nil {
				log.WithField("job_def_path", fullJobPath).Info("Found simple job definition")
				jobDefs.singleVersionCheckers = append(jobDefs.singleVersionCheckers, versionChecker{
					jobFolder: fullJobPath,
					script:    versionScript,
				})
			}
		}
	}
	// then, we find the actual job executors (to be replicated per found branch)
	foldersToCheck, err = firstLevelUnderConfig()
	if err != nil {
		return nil, err
	}
	for len(foldersToCheck) > 0 {
		checkedFolder := foldersToCheck[len(foldersToCheck)-1]
		foldersToCheck = foldersToCheck[:len(foldersToCheck)-1]
		log.WithField("job_folder", checkedFolder).Debug("Traversed folder for job definitions")

		foldersUnderJob, err := os.ReadDir(checkedFolder)
		if err != nil {
			return nil, err
		}
		for _, subFolder := range foldersUnderJob {
			if !subFolder.IsDir() || strings.HasPrefix(subFolder.Name(), ".") {
				continue
			}
			fullpath := filepath.Join(checkedFolder, subFolder.Name())
			jobPathRelative := strings.SplitN(fullpath, string(os.PathSeparator), 2)[1]
			foldersToCheck = append(foldersToCheck, fullpath)
			if _, findErr := FindScript(fullpath, agentInit); findErr == nil {
				log.WithField("job_def_path", jobPathRelative).Info("Found job initializer")
				foundJobs, err := generateAllJobs(jobPathRelative, &branchesByJob)
				if err != nil {
					return nil, err
				}

				jobDefs.allJobs = append(jobConfig.allJobs, foundJobs...)
			}
		}
	}
	return &jobDefs, nil
}
